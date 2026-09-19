package coord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// Run は保存済みのイベントを処理し続ける。再起動後も未処理イベントから再開する。
func (c *Coordinator) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := c.ProcessDue(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("イベント処理に失敗", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.wake:
		}
	}
}

// ProcessDue は実行時刻を過ぎたイベントをすべて処理し、処理した件数を返す。
func (c *Coordinator) ProcessDue(ctx context.Context) (int, error) {
	n := 0
	for {
		var due []store.Event
		if err := c.st.Tx(ctx, func(tx *store.Tx) error {
			var err error
			due, err = tx.DueEvents(ctx, c.now(), 20)
			return err
		}); err != nil {
			return n, err
		}
		if len(due) == 0 {
			return n, nil
		}
		for _, e := range due {
			var err error
			switch e.Kind {
			case store.EventPlan:
				err = c.handlePlan(ctx, e)
			case store.EventDeadline:
				err = c.handleDeadline(ctx, e)
			case store.EventReminder:
				err = c.handleReminder(ctx, e)
			default:
				err = c.st.Tx(ctx, func(tx *store.Tx) error { return tx.SetEventStatus(ctx, e.ID, "cancelled") })
			}
			if err != nil {
				return n, fmt.Errorf("イベント %s（%s）: %w", e.ID, e.Kind, err)
			}
			n++
		}
	}
}

// handlePlan は AI に次の一手を選ばせる。LLM 呼び出しはトランザクションの外で行い、
// 保存時に生成元の revision と案件の状態を確認して、古い状態に基づく結果で上書きしない。
func (c *Coordinator) handlePlan(ctx context.Context, e store.Event) error {
	var (
		sess  store.Session
		cs    store.Case
		snap  Snapshot
		pb    Playbook
		limit int
		stale bool
	)
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		var err error
		if cs, err = tx.Case(ctx, e.CaseID); err != nil {
			return err
		}
		if sess, err = tx.Session(ctx, e.SessionID); err != nil {
			return err
		}
		if cs.Status != store.CasePlanning {
			stale = true
			return tx.SetEventStatus(ctx, e.ID, "cancelled")
		}
		if now.After(retryCutoff(sess.StartsAt)) {
			stale = true
			if err := tx.SetEventStatus(ctx, e.ID, "done"); err != nil {
				return err
			}
			return c.needsOwner(ctx, tx, sess, &cs, "deadline_expired", "開催までに回答期限を確保できないため、管理者の判断が必要です。", now)
		}
		limit = min(MaxLLMCallsPerRun, MaxLLMCallsPerCase-cs.LLMCallCount)
		if limit <= 0 || cs.ToolCallCount >= MaxToolCallsPerCase {
			stale = true
			if err := tx.SetEventStatus(ctx, e.ID, "done"); err != nil {
				return err
			}
			return c.needsOwner(ctx, tx, sess, &cs, "budget_exceeded", "AIの呼び出し回数の上限に達したため、管理者の判断が必要です。", now)
		}
		if pb, err = c.playbook(sess.PlaybookID); err != nil {
			return err
		}
		snap, err = c.snapshot(ctx, tx, sess, &cs)
		return err
	})
	if err != nil || stale {
		return err
	}

	changeKind := changeKindFor(snap)
	outcome, usage, planErr := c.planner.Plan(ctx, PlanRequest{
		Snapshot: snap, Playbook: pb, ChangeKind: changeKind, MaxLLMCalls: limit,
		Check: func(ctx context.Context, o Outcome) error { return c.checkOutcome(ctx, pb, snap, changeKind, o) },
	})

	return c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		// 呼び出しの記録と回数は、結果を使うかどうかによらず必ず残す。
		cur, err := tx.Case(ctx, cs.ID)
		if err != nil {
			return err
		}
		for _, call := range usage.LLMCalls {
			if err := tx.AddLLMCall(ctx, store.LLMCall{
				CaseID: cur.ID, Model: call.Model, InputTokens: call.InputTokens, OutputTokens: call.OutputTokens, Currency: call.Currency,
				EstimatedAmount: call.EstimatedAmount, BilledAmount: call.BilledAmount, Succeeded: call.Succeeded, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		cur.LLMCallCount += len(usage.LLMCalls)
		cur.ToolCallCount += usage.ToolCalls
		cur.UpdatedAt = now
		if err := tx.UpdateCase(ctx, cur); err != nil {
			return err
		}
		if err := tx.SetEventStatus(ctx, e.ID, "done"); err != nil {
			return err
		}
		latest, err := tx.Session(ctx, sess.ID)
		if err != nil {
			return err
		}
		// 生成中に参加条件の更新や別案の提出があれば、この結果は使わない（新しい入力で再計画が予定済み）。
		if cur.Status != store.CasePlanning || latest.Revision != snap.Revision {
			c.log.Info("古い状態に基づく計画結果を破棄", "case", cur.ID)
			return nil
		}

		switch {
		case errors.Is(planErr, ErrTransient):
			delay := c.retryDelay(cur.RetryCount)
			next := now.Add(delay)
			if next.After(retryCutoff(latest.StartsAt)) {
				return c.needsOwner(ctx, tx, latest, &cur, "deadline_expired", "AIの応答に失敗し、開催までに再試行できないため、管理者の判断が必要です。", now)
			}
			cur.RetryCount++
			cur.NextRetryAt = &next
			cur.Summary = fmt.Sprintf("AIの応答に失敗したため、%s に再試行します。", formatClock(next))
			if err := tx.UpdateCase(ctx, cur); err != nil {
				return err
			}
			if err := tx.CreateEvent(ctx, store.Event{ID: store.NewID("evt"), SessionID: latest.ID, CaseID: cur.ID, Kind: store.EventPlan, RunAt: next, CreatedAt: now}); err != nil {
				return err
			}
			return c.activity(ctx, tx, latest, cur.ID, "agent_retry_scheduled", cur.Summary, "", now)
		case errors.Is(planErr, ErrBudgetExceeded):
			return c.needsOwner(ctx, tx, latest, &cur, "budget_exceeded", "AIの呼び出し回数・費用の上限に達したため、管理者の判断が必要です。", now)
		case planErr != nil:
			c.log.Warn("AIの出力が不正", "case", cur.ID, "err", planErr)
			return c.needsOwner(ctx, tx, latest, &cur, "model_error", "AIが条件を満たす出力を返せなかったため、管理者の判断が必要です。", now)
		}

		cur.RetryCount, cur.NextRetryAt = 0, nil
		// 保存直前に最新の状態で再検証する。
		s, err := c.snapshot(ctx, tx, latest, &cur)
		if err != nil {
			return err
		}
		if err := c.checkOutcome(ctx, pb, s, changeKind, outcome); err != nil {
			c.log.Warn("計画結果の再検証に失敗", "case", cur.ID, "err", err)
			return c.needsOwner(ctx, tx, latest, &cur, "model_error", "AIの案が条件を満たさなかったため、管理者の判断が必要です。", now)
		}
		switch outcome.Kind {
		case DraftProposal:
			_, err := c.createProposal(ctx, tx, &latest, &cur, s, "agent", outcome.Summary, outcome.Plan, now)
			switch {
			case errors.Is(err, errNoEligibleVoters):
				return c.needsOwner(ctx, tx, latest, &cur, "no_feasible_plan", "参加予定者がいないため、管理者の判断が必要です。", now)
			case errors.Is(err, errDeadline):
				return c.needsOwner(ctx, tx, latest, &cur, "deadline_expired", "開催までに回答期限を確保できないため、管理者の判断が必要です。", now)
			case errors.As(err, new(*ValidationError)):
				return c.needsOwner(ctx, tx, latest, &cur, "model_error", "AIの案が条件を満たさなかったため、管理者の判断が必要です。", now)
			}
			return err
		case DraftAsk:
			return c.ask(ctx, tx, latest, &cur, outcome, now)
		default:
			summary := outcome.Summary
			if summary == "" {
				summary = "条件を満たす案を作れなかったため、管理者の判断が必要です。"
			}
			return c.needsOwner(ctx, tx, latest, &cur, "no_feasible_plan", summary, now)
		}
	})
}

// ask は AI が選んだ相手に参加条件の確認を依頼する。同じ案件で同じ人に再依頼しない。
func (c *Coordinator) ask(ctx context.Context, tx *store.Tx, sess store.Session, cs *store.Case, o Outcome, now time.Time) error {
	due, ok := dueAt(now, sess.StartsAt)
	if !ok {
		return c.needsOwner(ctx, tx, sess, cs, "deadline_expired", "開催までに回答期限を確保できないため、管理者の判断が必要です。", now)
	}
	for _, id := range o.AskMemberIDs {
		m, err := tx.Member(ctx, id)
		if err != nil {
			return err
		}
		if err := c.createTask(ctx, tx, sess, *cs, m, store.TaskPreparation, "今回担当できる範囲と時間を確認させてください。", nil, "agent", due, now); err != nil {
			return err
		}
	}
	cs.Status, cs.Summary, cs.UpdatedAt = store.CaseCollecting, o.Summary, now
	if err := tx.UpdateCase(ctx, *cs); err != nil {
		return err
	}
	return c.activity(ctx, tx, sess, cs.ID, "input_received", "AIが確認を依頼しました："+o.Summary, "", now)
}

// handleDeadline は回答期限を過ぎたタスクを期限切れにし、必要条件が揃わなければ管理者へ戻す。
// 未回答を賛成や欠席として扱わない。
func (c *Coordinator) handleDeadline(ctx context.Context, e store.Event) error {
	return c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		if err := tx.SetEventStatus(ctx, e.ID, "done"); err != nil {
			return err
		}
		tk, err := tx.Task(ctx, e.RefID)
		if err != nil {
			return err
		}
		if tk.Status != store.TaskOpen {
			return nil
		}
		if err := tx.SetTaskStatus(ctx, tk.ID, store.TaskExpired); err != nil {
			return err
		}
		cs, err := tx.Case(ctx, tk.CaseID)
		if err != nil {
			return err
		}
		sess, err := tx.Session(ctx, tk.SessionID)
		if err != nil {
			return err
		}
		if !cs.Open() {
			return nil
		}
		if tk.Kind == store.TaskPreparation {
			preps, err := tx.Preparations(ctx, sess.ID)
			if err != nil {
				return err
			}
			if _, ok := preps[tk.MemberID]; !ok {
				return c.needsOwner(ctx, tx, sess, &cs, "deadline_expired", "参加条件の回答が期限までに揃わなかったため、管理者の判断が必要です。", now)
			}
			// AI の確認依頼に回答がなかった場合は、既存の参加条件のまま次へ進む（同じ依頼は繰り返さない）。
			return c.advance(ctx, tx, sess, &cs, now)
		}
		p, err := tx.Proposal(ctx, tk.ProposalID)
		if err != nil {
			return err
		}
		if p.Status != store.ProposalPending || cs.Status != store.CaseAwaitingConsent {
			return nil
		}
		if err := tx.CloseOpenTasksByProposal(ctx, p.ID, store.TaskExpired); err != nil {
			return err
		}
		return c.needsOwner(ctx, tx, sess, &cs, "deadline_expired", fmt.Sprintf("案（版%d）への必要な回答が期限までに揃わなかったため、管理者の判断が必要です。", p.Version), now)
	})
}

// handleReminder は期限の中間時点で未回答者に1回だけ催促する。
func (c *Coordinator) handleReminder(ctx context.Context, e store.Event) error {
	return c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		if err := tx.SetEventStatus(ctx, e.ID, "done"); err != nil {
			return err
		}
		tk, err := tx.Task(ctx, e.RefID)
		if err != nil {
			return err
		}
		ok, err := tx.MarkTaskReminded(ctx, tk.ID, now)
		if err != nil || !ok {
			return err
		}
		sess, err := tx.Session(ctx, tk.SessionID)
		if err != nil {
			return err
		}
		m, err := tx.Member(ctx, tk.MemberID)
		if err != nil {
			return err
		}
		text := fmt.Sprintf("未回答の依頼があります：%s（回答期限：%s）", tk.Title, formatClock(tk.DueAt))
		return c.notify(ctx, tx, sess, tk.CaseID, "reminder", "reminder:"+tk.ID, text, []store.Member{m}, now)
	})
}
