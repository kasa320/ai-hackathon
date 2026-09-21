package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// ブックの計画で個人へ送るDMの種別。本人だけに届き、共通チャンネルへは退避しない。
const (
	NotifyBookPlanProposed     = "book_plan_proposed"
	NotifyBookPlanApproved     = "book_plan_approved"
	NotifyBookAttention        = "book_attention"
	NotifyBookAssigneeChange   = "book_assignee_change"
	NotifyBookAssigneeConfirm  = "book_assignee_confirm"
	NotifyBookAssigneeRemind   = "book_assignee_reminder"
	NotifyBookAssigneeEscalate = "book_assignee_escalation"
	NotifyAvailabilityRequest  = "availability_requested"
)

const (
	// bookPlanMaxAttempts は一時的な障害でAIの計画に失敗しても再試行する回数（超えたら管理者判断待ち）。
	bookPlanMaxAttempts = 3
	// 直前確認は開催のこの時間前に始め、その後に催促・管理者へのエスカレーションを行う。
	confirmLead    = 72 * time.Hour
	remindLead     = 48 * time.Hour
	escalationLead = 24 * time.Hour
	// confirmStageGap は確認・催促・エスカレーションを一度に連続して送らないための最小の間隔。
	confirmStageGap = time.Hour
)

// adjustmentStart は枠の日程調整を始める日（開催目安の開始日のリード日数前の0時、JST）。
func adjustmentStart(b store.ReadingBook, s store.ReadingBookSlot) time.Time {
	day, err := time.ParseInLocation("2006-01-02", s.PeriodStart, displayZone)
	if err != nil {
		return time.Time{}
	}
	return day.AddDate(0, 0, -b.AdjustmentLeadDays)
}

func (c *Coordinator) bookURL(b store.ReadingBook) string {
	return fmt.Sprintf("%s/book.html?group_id=%s&id=%s", c.opts.PublicBaseURL, url.QueryEscape(b.GroupID), url.QueryEscape(b.ID))
}

// enqueueBookDM は本人宛てのDMを1件ずつ登録する。同じ operationKey・受信者へは二重に登録しない。
func (c *Coordinator) enqueueBookDM(ctx context.Context, tx *store.Tx, b store.ReadingBook, kind, operationKey, text string, to []store.Member, now time.Time) error {
	g, err := tx.Group(ctx, b.GroupID)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("【%s／%s】%s\n%s", g.Name, b.Title, text, c.bookURL(b))
	_, err = enqueueGroupDM(ctx, tx, b.GroupID, kind, operationKey, content, to, now)
	return err
}

func ownerMember(members []store.Member) (store.Member, bool) {
	for _, m := range members {
		if m.Role == RoleOwner {
			return m, true
		}
	}
	return store.Member{}, false
}

func memberByID(members []store.Member, id string) (store.Member, bool) {
	for _, m := range members {
		if m.ID == id {
			return m, true
		}
	}
	return store.Member{}, false
}

func (c *Coordinator) bookPlannerFor(ctx context.Context, tx *store.Tx, b store.ReadingBook) (BookPlanner, error) {
	g, err := tx.Group(ctx, b.GroupID)
	if err != nil {
		return nil, err
	}
	return c.bookPlanner(g)
}

func bookSections(raw json.RawMessage) []BookSection {
	var xs []BookSection
	_ = json.Unmarshal(raw, &xs)
	return xs
}

// loadsFor は同じグループで同時進行中の全ブックを合算した担当負担と、日程が近い別ブックの担当を返す。
// exceptSlot の枠と、exceptBook のブック（計画を作り直す対象）の枠は数えない。
func loadsFor(all []store.GroupSlot, exceptBook, exceptSlot string) (map[string]BookMemberLoad, []BookOtherSlot) {
	loads := map[string]BookMemberLoad{}
	var others []BookOtherSlot
	for _, gs := range all {
		if gs.AssigneeMemberID == "" || gs.ID == exceptSlot || gs.BookID == exceptBook {
			continue
		}
		l := loads[gs.AssigneeMemberID]
		if gs.Status == "completed" {
			l.Completed++
		} else if gs.BookStatus == "in_progress" {
			l.Concurrent++
			others = append(others, BookOtherSlot{BookTitle: gs.BookTitle, PeriodStart: gs.PeriodStart, PeriodEnd: gs.PeriodEnd, AssigneeMemberID: gs.AssigneeMemberID})
		}
		loads[gs.AssigneeMemberID] = l
	}
	return loads, others
}

func eligibleMembers(members []store.Member) []BookPlanMember {
	var out []BookPlanMember
	for _, m := range members {
		if m.Joined() {
			out = append(out, BookPlanMember{ID: m.ID, DisplayName: m.DisplayName, Role: m.Role})
		}
	}
	return out
}

// bookPlanInput は全体計画の判断材料を組み立てる。ブック自身の既存の割り当ては負担に数えない。
func (c *Coordinator) bookPlanInput(ctx context.Context, tx *store.Tx, b store.ReadingBook) (BookPlanInput, error) {
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return BookPlanInput{}, err
	}
	all, err := tx.GroupSlots(ctx, b.GroupID)
	if err != nil {
		return BookPlanInput{}, err
	}
	loads, others := loadsFor(all, b.ID, "")
	in := BookPlanInput{Title: b.Title, Sections: bookSections(b.Sections), PeriodStart: b.PeriodStart, PeriodEnd: b.PeriodEnd, SlotCount: b.PlannedSessionCount,
		DurationMinutes: b.DurationMinutes, Members: eligibleMembers(members), Loads: loads, Others: others}
	for _, m := range in.Members {
		if _, ok := in.Loads[m.ID]; !ok {
			in.Loads[m.ID] = BookMemberLoad{}
		}
	}
	return in, nil
}

// bookNeedsAttention は計画を確定せず、理由を残して管理者へ戻す。
func (c *Coordinator) bookNeedsAttention(ctx context.Context, tx *store.Tx, b *store.ReadingBook, reason, summary string, now time.Time) error {
	b.PlanStatus, b.PlanReason, b.PlanSummary, b.PlanNextAt, b.UpdatedAt = store.BookPlanNeedsAttention, reason, summary, nil, now
	if err := tx.UpdateReadingBook(ctx, *b); err != nil {
		return err
	}
	if err := bookLog(ctx, tx, *b, "", "", "plan_needs_attention", summary, now); err != nil {
		return err
	}
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return err
	}
	if owner, ok := ownerMember(members); ok {
		key := fmt.Sprintf("book_attention:%s:%d:%s", b.ID, b.PlanVersion, reason)
		return c.enqueueBookDM(ctx, tx, *b, NotifyBookAttention, key, summary+"\n管理者の判断が必要です。計画の再生成または編集を行ってください。", []store.Member{owner}, now)
	}
	return nil
}

// recordBookUsage はAIの呼び出しを、結果を使うかどうかによらず記録する。ブックの計画の費用は
// llm_calls の lookup_id にブックのIDを入れて追跡する（案件に属さないため）。
func recordBookUsage(ctx context.Context, tx *store.Tx, bookID string, u Usage, now time.Time) error {
	for _, call := range u.LLMCalls {
		if err := tx.AddLLMCall(ctx, store.LLMCall{LookupID: bookID, Model: call.Model, InputTokens: call.InputTokens, OutputTokens: call.OutputTokens,
			Currency: call.Currency, EstimatedAmount: call.EstimatedAmount, BilledAmount: call.BilledAmount, Succeeded: call.Succeeded, CreatedAt: now}); err != nil {
			return err
		}
	}
	return nil
}

// processBookPlans は計画待ちのブックについて、AIに全体計画（章割り・開催目安・担当）を作らせる。
// LLM呼び出しはトランザクションの外で行い、保存時にブックの版と状態を確認して古い結果を使わない。
func (c *Coordinator) processBookPlans(ctx context.Context) (int, error) {
	var books []store.ReadingBook
	if err := c.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		books, err = tx.PlanningBooks(ctx, c.now(), 10)
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, b := range books {
		changed, err := c.planBook(ctx, b)
		if err != nil {
			return n, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

func (c *Coordinator) planBook(ctx context.Context, b store.ReadingBook) (bool, error) {
	var (
		in      BookPlanInput
		bp      BookPlanner
		skipped bool
	)
	noMembers := false
	if err := c.st.Tx(ctx, func(tx *store.Tx) error {
		cur, err := tx.ReadingBook(ctx, b.ID)
		if errors.Is(err, store.ErrNotFound) || err == nil && (cur.PlanStatus != store.BookPlanPlanning || cur.PlanVersion != b.PlanVersion) {
			skipped = true
			return nil
		}
		if err != nil {
			return err
		}
		if bp, err = c.bookPlannerFor(ctx, tx, cur); err != nil {
			return err
		}
		if in, err = c.bookPlanInput(ctx, tx, cur); err != nil {
			return err
		}
		if len(in.Members) == 0 {
			// 担当を割り当てられる人（ログイン済みの在籍者）がいない。AIを呼ばず管理者へ戻す。
			noMembers = true
			return c.bookNeedsAttention(ctx, tx, &cur, "no_eligible_members", "担当を割り当てられるログイン済みのメンバーがいないため、全体計画を作れません。", c.now())
		}
		return nil
	}); err != nil {
		return false, err
	}
	if skipped {
		return false, nil
	}
	if noMembers {
		return true, nil
	}

	plan, usage, planErr := c.bookAgent.PlanBook(ctx, BookPlanRequest{
		Input: in, Planner: bp, MaxLLMCalls: MaxLLMCallsPerRun,
		Check: func(p BookPlan) error { return bp.ValidateBookPlan(in, p) },
	})

	changed := false
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		if err := recordBookUsage(ctx, tx, b.ID, usage, now); err != nil {
			return err
		}
		cur, err := tx.ReadingBook(ctx, b.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // 処理中にグループが削除された。費用の記録だけ残す
		}
		if err != nil {
			return err
		}
		if cur.PlanStatus != store.BookPlanPlanning || cur.PlanVersion != b.PlanVersion {
			c.log.Info("古い状態に基づくブック計画を破棄", "book", b.ID)
			return nil
		}
		changed = true
		if planErr != nil {
			c.log.Warn("ブックの全体計画に失敗", "book", cur.ID, "attempts", cur.PlanAttempts, "err", planErr)
		}
		switch {
		case errors.Is(planErr, ErrTransient):
			cur.PlanAttempts++
			if cur.PlanAttempts >= bookPlanMaxAttempts {
				return c.bookNeedsAttention(ctx, tx, &cur, "ai_unavailable", "AIの応答に繰り返し失敗したため、全体計画を作れませんでした。", now)
			}
			next := now.Add(c.retryDelay(cur.PlanAttempts))
			cur.PlanNextAt, cur.UpdatedAt = &next, now
			return tx.UpdateReadingBook(ctx, cur)
		case errors.Is(planErr, ErrBudgetExceeded):
			return c.bookNeedsAttention(ctx, tx, &cur, "budget_exceeded", "AIの呼び出し回数・費用の上限に達したため、全体計画を作れませんでした。", now)
		case planErr != nil:
			return c.bookNeedsAttention(ctx, tx, &cur, "model_error", "AIが条件を満たす全体計画を作れませんでした。", now)
		}
		// 保存直前に最新の状態で再検証する。メンバーの脱退などで条件が変わっていれば使わない。
		fresh, err := c.bookPlanInput(ctx, tx, cur)
		if err != nil {
			return err
		}
		if err := bp.ValidateBookPlan(fresh, plan); err != nil {
			c.log.Warn("ブック計画の再検証に失敗", "book", cur.ID, "err", err)
			return c.bookNeedsAttention(ctx, tx, &cur, "plan_invalid", "AIの全体計画が最新の条件を満たさなくなったため、計画を確定できませんでした。", now)
		}
		return c.storeBookPlan(ctx, tx, cur, plan, now)
	})
	return changed, err
}

// storeBookPlan は検証済みの計画を仮の割り当てとして保存し、割り当てた各担当者へ承認を依頼する。
// 承認前の計画は確定ではない（awaiting_approval）。
func (c *Coordinator) storeBookPlan(ctx context.Context, tx *store.Tx, b store.ReadingBook, plan BookPlan, now time.Time) error {
	slots, err := tx.ReadingBookSlots(ctx, b.ID)
	if err != nil {
		return err
	}
	bySeq := map[int]BookPlanSlot{}
	for _, ps := range plan.Slots {
		bySeq[ps.Sequence] = ps
	}
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return err
	}
	assigned := map[string][]int{}
	var order []string
	for _, s := range slots {
		ps, ok := bySeq[s.SequenceNumber]
		if !ok || s.Status != "planned" || s.SessionID != nil {
			return fmt.Errorf("coord: 計画と枠が対応しません（第%d回）", s.SequenceNumber)
		}
		s.PeriodStart, s.PeriodEnd, s.TargetSectionIDs = ps.PeriodStart, ps.PeriodEnd, ps.SectionIDs
		s.AssigneeMemberID, s.AssignmentStatus, s.ProposedAssigneeID = ps.AssigneeMemberID, store.AssignPending, ""
		s.ExcludedMemberIDs, s.ChangeAttempts, s.ChangeNextAt, s.AttentionReason = nil, 0, nil, ""
		if err := tx.UpdateReadingBookSlot(ctx, s); err != nil {
			return err
		}
		if _, seen := assigned[ps.AssigneeMemberID]; !seen {
			order = append(order, ps.AssigneeMemberID)
		}
		assigned[ps.AssigneeMemberID] = append(assigned[ps.AssigneeMemberID], s.SequenceNumber)
		if err := bookLog(ctx, tx, b, s.ID, ps.AssigneeMemberID, "assignment_proposed", fmt.Sprintf("第%d回の担当を仮に割り当てました。本人の承認を待っています。", s.SequenceNumber), now); err != nil {
			return err
		}
	}
	b.PlanStatus, b.PlanReason, b.PlanAttempts, b.PlanNextAt, b.UpdatedAt = store.BookPlanAwaitingApproval, "", 0, nil, now
	b.PlanSummary = plan.Summary
	if err := tx.UpdateReadingBook(ctx, b); err != nil {
		return err
	}
	if err := bookLog(ctx, tx, b, "", "", "plan_created", "全体計画（章割り・開催目安・担当の仮割り当て）を作成しました。全担当者の承認を待っています。", now); err != nil {
		return err
	}
	for _, id := range order {
		m, ok := memberByID(members, id)
		if !ok {
			continue
		}
		key := fmt.Sprintf("book_plan:%s:%d", b.ID, b.PlanVersion)
		text := fmt.Sprintf("担当の割り当て案が届きました（第%s回）。内容を確認し、担当を引き受けられるか回答してください。回答があるまで確定しません。", joinInts(assigned[id]))
		if err := c.enqueueBookDM(ctx, tx, b, NotifyBookPlanProposed, key, text, []store.Member{m}, now); err != nil {
			return err
		}
	}
	return c.reviewBookApproval(ctx, tx, b, now)
}

func joinInts(xs []int) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "・"
		}
		out += fmt.Sprint(x)
	}
	return out
}

// reviewBookApproval は割り当てられた全員が承認したときだけ、計画を成立（approved）にする。
// 未回答・変更希望・変更候補の承認待ちが1つでもあれば成立させない。
func (c *Coordinator) reviewBookApproval(ctx context.Context, tx *store.Tx, b store.ReadingBook, now time.Time) error {
	if b.PlanStatus != store.BookPlanAwaitingApproval {
		return nil
	}
	slots, err := tx.ReadingBookSlots(ctx, b.ID)
	if err != nil {
		return err
	}
	if len(slots) == 0 {
		return nil
	}
	for _, s := range slots {
		if s.AssigneeMemberID == "" || s.AssignmentStatus != store.AssignAccepted {
			return nil
		}
	}
	b.PlanStatus, b.UpdatedAt = store.BookPlanApproved, now
	if err := tx.UpdateReadingBook(ctx, b); err != nil {
		return err
	}
	if err := bookLog(ctx, tx, b, "", "", "plan_approved", "全担当者が自分の担当を承認したため、計画が成立しました。各回は調整開始日に自動で始まります。", now); err != nil {
		return err
	}
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return err
	}
	if owner, ok := ownerMember(members); ok {
		key := fmt.Sprintf("book_approved:%s:%d", b.ID, b.PlanVersion)
		return c.enqueueBookDM(ctx, tx, b, NotifyBookPlanApproved, key, "全担当者が承認し、全体計画が成立しました。各回は調整開始日になると自動で日程調整を始めます。", []store.Member{owner}, now)
	}
	return nil
}

// availabilityStaleAfter を過ぎた週間空き時間は、再確認を依頼する。
const availabilityStaleAfter = 30 * 24 * time.Hour

// RequestAvailabilityUpdate は管理者が、メンバーへ普段の空き時間の登録または再確認を依頼する。
// 未登録の人には登録を、登録が古い人には再確認をDMで依頼し、最近登録した人には送らない。
// 同じ日に同じ相手へは1回だけ依頼する（重複を送らない）。
func (c *Coordinator) RequestAvailabilityUpdate(ctx context.Context, userID, groupID, bookID string, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	return c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		_, m, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		if m.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		out, e := c.requestAvailabilityUpdateTx(ctx, tx, b, m.ID, now)
		if e != nil {
			return store.Response{}, e
		}
		return store.Response{Status: http.StatusOK, Body: encode(out)}, nil
	})
}

// requestAvailabilityUpdateTx はブック登録直後と管理者の再依頼で共用する。
// 空き時間はユーザー共通なので、同じグループ・同じ日の複数ブックからの依頼は1通にまとめる。
func (c *Coordinator) requestAvailabilityUpdateTx(ctx context.Context, tx *store.Tx, b store.ReadingBook, requestedBy string, now time.Time) (apitypes.AvailabilityRequestResult, error) {
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return apitypes.AvailabilityRequestResult{}, err
	}
	day := now.In(displayZone).Format("2006-01-02")
	var out apitypes.AvailabilityRequestResult
	for _, member := range members {
		if member.LeftAt != nil || member.DiscordUserID == "" {
			out.SkippedCount++
			continue
		}
		text := "普段の空き時間（曜日と時間帯）を登録してください。日程調整の候補づくりに使います。回答は他のグループ・ブックでも使われます。"
		if member.UserID != "" {
			a, lookupErr := tx.WeeklyAvailability(ctx, member.UserID)
			switch {
			case errors.Is(lookupErr, store.ErrNotFound):
			case lookupErr != nil:
				return out, lookupErr
			case now.Sub(a.UpdatedAt) < availabilityStaleAfter:
				out.SkippedCount++
				continue
			default:
				text = "登録済みの普段の空き時間が古くなっています。今も合っているか再確認し、変わっていれば更新してください。"
			}
		}
		key := fmt.Sprintf("availability:%s:%s", b.GroupID, day)
		if err := c.enqueueBookDM(ctx, tx, b, NotifyAvailabilityRequest, key, text, []store.Member{member}, now); err != nil {
			return out, err
		}
		out.RequestedCount++
	}
	if err := bookLog(ctx, tx, b, "", requestedBy, "availability_requested", fmt.Sprintf("空き時間の登録・再確認を%d人へ依頼しました。", out.RequestedCount), now); err != nil {
		return out, err
	}
	return out, nil
}
