package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// 担当・確認への回答の値。
const (
	DecisionAccept        = "accept"
	DecisionRequestChange = "request_change"
	DecisionConfirm       = "confirm"
)

// bookSlotOf はブックに属する枠を返す。別のブックの枠は存在しないものとして扱う。
func bookSlotOf(ctx context.Context, tx *store.Tx, b store.ReadingBook, slotID string) (store.ReadingBookSlot, error) {
	s, err := tx.ReadingBookSlot(ctx, slotID)
	if errors.Is(err, store.ErrNotFound) || err == nil && s.BookID != b.ID {
		return s, apperr.NotFoundErr()
	}
	return s, err
}

func requestChange(s *store.ReadingBookSlot, memberID string) {
	s.AssignmentStatus, s.ProposedAssigneeID = store.AssignChangeRequested, ""
	s.ExcludedMemberIDs = appendUnique(s.ExcludedMemberIDs, memberID)
	s.ChangeAttempts, s.ChangeNextAt, s.AttentionReason = 0, nil, ""
}

// RespondBookAssignment は本人が、自分に割り当てられた担当（または担当変更の候補）へ回答する。
//   - slot_id を省略：自分に割り当てられた未回答の初期担当すべてが対象。担当変更の候補は含めない。
//   - slot_id を指定：その枠で自分が「未回答の担当者」か「変更候補」のときだけ作用する。
//
// 他人の担当には作用できない。回答しなかった人を承認扱いにせず、管理者の承認件数も要求しない。
// 担当の変更は候補本人が承認した時点で確定し、他の担当者の承認は取り直さない。
func (c *Coordinator) RespondBookAssignment(ctx context.Context, userID, groupID, bookID string, in apitypes.BookAssignmentInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		_, me, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		if in.Decision != DecisionAccept && in.Decision != DecisionRequestChange {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "decision", Message: "accept または request_change を指定してください"})
		}
		if b.PlanStatus != store.BookPlanAwaitingApproval && b.PlanStatus != store.BookPlanApproved {
			return store.Response{}, apperr.InvalidStateErr("担当の回答を受け付けられる状態ではありません（全体計画の作成中、または管理者の判断待ちです）。")
		}
		slots, e := tx.ReadingBookSlots(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		type target struct {
			slot      store.ReadingBookSlot
			candidate bool
		}
		var targets []target
		if in.SlotID == "" {
			for _, s := range slots {
				if s.AssigneeMemberID == me.ID && s.AssignmentStatus == store.AssignPending && s.Status == "planned" {
					targets = append(targets, target{slot: s})
				}
			}
		} else {
			s, e := bookSlotOf(ctx, tx, b, in.SlotID)
			if e != nil {
				return store.Response{}, e
			}
			switch {
			case s.AssigneeMemberID == me.ID && s.AssignmentStatus == store.AssignPending && s.Status == "planned":
				targets = append(targets, target{slot: s})
			case s.ProposedAssigneeID == me.ID && s.AssignmentStatus == store.AssignChangeProposed && s.Status != "completed":
				targets = append(targets, target{slot: s, candidate: true})
			case s.AssigneeMemberID != me.ID && s.ProposedAssigneeID != me.ID:
				return store.Response{}, apperr.New(apperr.Forbidden, "自分に割り当てられた担当ではありません。他の人の担当には回答できません。")
			}
		}
		if len(targets) == 0 {
			return store.Response{}, apperr.InvalidStateErr("回答待ちの担当がありません。")
		}
		members, e := tx.Members(ctx, groupID)
		if e != nil {
			return store.Response{}, e
		}
		for _, t := range targets {
			s := t.slot
			switch {
			case !t.candidate && in.Decision == DecisionAccept:
				s.AssignmentStatus = store.AssignAccepted
				e = tx.UpdateReadingBookSlot(ctx, s)
				if e == nil {
					e = bookLog(ctx, tx, b, s.ID, me.ID, "assignment_accepted", fmt.Sprintf("第%d回の担当を本人が承認しました。", s.SequenceNumber), now)
				}
			case !t.candidate:
				requestChange(&s, me.ID)
				e = tx.UpdateReadingBookSlot(ctx, s)
				if e == nil {
					e = bookLog(ctx, tx, b, s.ID, me.ID, "change_requested", fmt.Sprintf("第%d回の担当の変更を本人が希望しました。候補を探します。", s.SequenceNumber), now)
				}
			case in.Decision == DecisionAccept:
				e = c.acceptReplacement(ctx, tx, b, s, me, members, now)
			default:
				requestChange(&s, me.ID)
				e = tx.UpdateReadingBookSlot(ctx, s)
				if e == nil {
					e = bookLog(ctx, tx, b, s.ID, me.ID, "candidate_declined", fmt.Sprintf("第%d回の担当変更の候補を本人が辞退しました。別の候補を探します。", s.SequenceNumber), now)
				}
			}
			if e != nil {
				return store.Response{}, e
			}
		}
		// 承認が揃ったかを判定する。担当の変更だけでは他の人の承認は取り直さない。
		if e := c.reviewBookApproval(ctx, tx, b, now); e != nil {
			return store.Response{}, e
		}
		cur, e := tx.ReadingBook(ctx, b.ID)
		if e != nil {
			return store.Response{}, e
		}
		out, e := c.bookDetail(ctx, tx, cur, me)
		return store.Response{Status: http.StatusOK, Body: encode(out)}, e
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// acceptReplacement は変更候補本人の承認で担当を入れ替える。実セッションがあれば、担当を固定した
// 開催回データを更新して再調整（担当だけの変更なので全員の再承認は不要）を始める。
func (c *Coordinator) acceptReplacement(ctx context.Context, tx *store.Tx, b store.ReadingBook, s store.ReadingBookSlot, me store.Member, members []store.Member, now time.Time) error {
	old := s.AssigneeMemberID
	s.AssigneeMemberID, s.ProposedAssigneeID, s.AssignmentStatus = me.ID, "", store.AssignAccepted
	s.ChangeAttempts, s.ChangeNextAt, s.AttentionReason = 0, nil, ""
	if err := tx.UpdateReadingBookSlot(ctx, s); err != nil {
		return err
	}
	if err := bookLog(ctx, tx, b, s.ID, me.ID, "assignee_changed", fmt.Sprintf("第%d回の担当が、候補本人の承認により変更されました。", s.SequenceNumber), now); err != nil {
		return err
	}
	confs, err := tx.SlotConfirmations(ctx, s.ID)
	if err != nil {
		return err
	}
	for _, cf := range confs {
		if cf.MemberID != me.ID && cf.Status != store.ConfirmSuperseded {
			cf.Status = store.ConfirmSuperseded
			if err := tx.UpdateSlotConfirmation(ctx, cf); err != nil {
				return err
			}
		}
	}
	if s.SessionID != nil {
		sess, err := tx.Session(ctx, *s.SessionID)
		if err != nil {
			return err
		}
		if err := c.retargetSession(ctx, tx, &sess, b, s, me, now); err != nil {
			return err
		}
	}
	var to []store.Member
	if m, ok := memberByID(members, old); ok && old != me.ID {
		to = append(to, m)
	}
	if owner, ok := ownerMember(members); ok {
		to = append(to, owner)
	}
	text := fmt.Sprintf("第%d回の担当が %s さんに変更されました（本人が承認済み）。", s.SequenceNumber, me.DisplayName)
	return c.enqueueBookDM(ctx, tx, b, NotifyBookAssigneeChange, fmt.Sprintf("book_changed:%s:%s", s.ID, me.ID), text, to, now)
}

// retargetSession は担当が変わった実セッションの担当を更新し、担当だけの再調整を始める。
// 新しい担当者は候補として本人が承認済みなので、開催3日前の窓に入っていれば確認済みとして記録する。
func (c *Coordinator) retargetSession(ctx context.Context, tx *store.Tx, sess *store.Session, b store.ReadingBook, s store.ReadingBookSlot, next store.Member, now time.Time) error {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(sess.Data, &data); err != nil {
		return err
	}
	id, _ := json.Marshal(next.ID)
	data["assignee_member_id"] = id
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	sess.Data = raw
	if err := tx.UpdateSessionData(ctx, sess.ID, raw, now); err != nil {
		return err
	}
	if err := bump(ctx, tx, sess, now); err != nil {
		return err
	}
	cs, err := c.onMembershipChanged(ctx, tx, sess, "", now)
	if err != nil {
		return err
	}
	if err := c.activity(ctx, tx, *sess, cs.ID, "assignee_changed", fmt.Sprintf("担当が %s さんに変更されました。担当だけの変更として再調整します。", next.DisplayName), "", now); err != nil {
		return err
	}
	if scheduleStatus(*sess) == store.ScheduleConfirmed && !now.Before(sess.StartsAt.Add(-confirmLead)) && now.Before(sess.StartsAt) {
		ok, err := tx.CreateSlotConfirmation(ctx, store.SlotConfirmation{ID: store.NewID("conf"), SlotID: s.ID, MemberID: next.ID, Status: store.ConfirmConfirmed, RequestedAt: now, AnsweredAt: &now})
		if err != nil || !ok {
			return err
		}
		return bookLog(ctx, tx, b, s.ID, next.ID, "confirmation_confirmed", "担当変更を本人が承認したため、直前確認は済みとして記録しました。", now)
	}
	return nil
}

// RespondAssigneeConfirmation は開催3日前の最終確認に、現在の担当者本人だけが回答する。
func (c *Coordinator) RespondAssigneeConfirmation(ctx context.Context, userID, groupID, bookID, slotID string, in apitypes.AssigneeConfirmationInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		_, me, e := c.groupAccess(ctx, tx, userID, groupID)
		if e != nil {
			return store.Response{}, e
		}
		b, e := tx.ReadingBook(ctx, bookID)
		if errors.Is(e, store.ErrNotFound) || b.GroupID != groupID {
			return store.Response{}, apperr.NotFoundErr()
		}
		if e != nil {
			return store.Response{}, e
		}
		s, e := bookSlotOf(ctx, tx, b, slotID)
		if e != nil {
			return store.Response{}, e
		}
		if in.Decision != DecisionConfirm && in.Decision != DecisionRequestChange {
			return store.Response{}, apperr.Validation(apperr.Field{Path: "decision", Message: "confirm または request_change を指定してください"})
		}
		if s.AssigneeMemberID != me.ID {
			return store.Response{}, apperr.New(apperr.Forbidden, "現在の担当者本人だけが回答できます。")
		}
		conf, e := tx.SlotConfirmation(ctx, s.ID, me.ID)
		if errors.Is(e, store.ErrNotFound) {
			return store.Response{}, apperr.InvalidStateErr("まだ担当者確認の時期ではありません。")
		}
		if e != nil {
			return store.Response{}, e
		}
		if conf.Status != store.ConfirmOpen && conf.Status != store.ConfirmNeedsOwner {
			return store.Response{}, apperr.InvalidStateErr("この確認にはすでに回答済みです。")
		}
		conf.AnsweredAt = &now
		if in.Decision == DecisionConfirm {
			conf.Status = store.ConfirmConfirmed
			if e := bookLog(ctx, tx, b, s.ID, me.ID, "confirmation_confirmed", fmt.Sprintf("第%d回の担当を本人が直前に確認しました。", s.SequenceNumber), now); e != nil {
				return store.Response{}, e
			}
		} else {
			conf.Status = store.ConfirmChangeRequested
			requestChange(&s, me.ID)
			if e := tx.UpdateReadingBookSlot(ctx, s); e != nil {
				return store.Response{}, e
			}
			if e := bookLog(ctx, tx, b, s.ID, me.ID, "change_requested", fmt.Sprintf("第%d回の直前確認で、担当の変更を本人が希望しました。候補を探します。", s.SequenceNumber), now); e != nil {
				return store.Response{}, e
			}
		}
		if e := tx.UpdateSlotConfirmation(ctx, conf); e != nil {
			return store.Response{}, e
		}
		out, e := c.bookDetail(ctx, tx, b, me)
		return store.Response{Status: http.StatusOK, Body: encode(out)}, e
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// replacementInput は担当変更の候補を選ぶ判断材料を組み立てる。
func (c *Coordinator) replacementInput(ctx context.Context, tx *store.Tx, b store.ReadingBook, s store.ReadingBookSlot) (ReplacementInput, error) {
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return ReplacementInput{}, err
	}
	all, err := tx.GroupSlots(ctx, b.GroupID)
	if err != nil {
		return ReplacementInput{}, err
	}
	loads, _ := loadsFor(all, "", s.ID)
	in := ReplacementInput{BookTitle: b.Title, Sequence: s.SequenceNumber, PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd, SectionIDs: nonNil(s.TargetSectionIDs),
		Current: s.AssigneeMemberID, Excluded: nonNil(s.ExcludedMemberIDs), Members: eligibleMembers(members), Loads: loads, BookAssignees: map[int]string{}}
	for _, m := range in.Members {
		if _, ok := in.Loads[m.ID]; !ok {
			in.Loads[m.ID] = BookMemberLoad{}
		}
	}
	for _, gs := range all {
		if gs.BookID == b.ID && gs.AssigneeMemberID != "" {
			in.BookAssignees[gs.SequenceNumber] = gs.AssigneeMemberID
		}
	}
	return in, nil
}

func (c *Coordinator) slotNeedsAttention(ctx context.Context, tx *store.Tx, b store.ReadingBook, s store.ReadingBookSlot, reason, summary string, now time.Time) error {
	s.AssignmentStatus, s.AttentionReason, s.ChangeNextAt = store.AssignNeedsAttention, reason, nil
	if err := tx.UpdateReadingBookSlot(ctx, s); err != nil {
		return err
	}
	if err := bookLog(ctx, tx, b, s.ID, "", "change_needs_attention", summary, now); err != nil {
		return err
	}
	members, err := tx.Members(ctx, b.GroupID)
	if err != nil {
		return err
	}
	if owner, ok := ownerMember(members); ok {
		key := fmt.Sprintf("book_slot_attention:%s:%d:%s", s.ID, len(s.ExcludedMemberIDs), reason)
		return c.enqueueBookDM(ctx, tx, b, NotifyBookAttention, key, fmt.Sprintf("第%d回：%s\n管理者の判断が必要です。", s.SequenceNumber, summary), []store.Member{owner}, now)
	}
	return nil
}

// processDepartedAssignees は担当者や変更候補が脱退した枠を、担当変更が必要な状態へ戻す。
func (c *Coordinator) processDepartedAssignees(ctx context.Context) (int, error) {
	n := 0
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		slots, err := tx.SlotsWithDepartedMember(ctx)
		if err != nil {
			return err
		}
		now := c.now()
		for _, s := range slots {
			b, err := tx.ReadingBook(ctx, s.BookID)
			if err != nil {
				return err
			}
			for _, id := range []string{s.AssigneeMemberID, s.ProposedAssigneeID} {
				if id == "" {
					continue
				}
				if m, err := tx.Member(ctx, id); err == nil && m.LeftAt != nil {
					s.ExcludedMemberIDs = appendUnique(s.ExcludedMemberIDs, id)
					if id == s.ProposedAssigneeID {
						s.ProposedAssigneeID = ""
					}
					s.AssignmentStatus, s.ChangeAttempts, s.ChangeNextAt, s.AttentionReason = store.AssignChangeRequested, 0, nil, ""
					if err := bookLog(ctx, tx, b, s.ID, id, "member_left", fmt.Sprintf("第%d回に関わるメンバーが脱退したため、担当の変更候補を探します。", s.SequenceNumber), now); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}
			}
			if err := tx.UpdateReadingBookSlot(ctx, s); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// processReplacements は変更希望のある枠へ、AIに担当変更の候補を提案させる。
// 提案は候補本人が承認するまで担当を入れ替えず、AIが無承認で担当者を交代させることはない。
func (c *Coordinator) processReplacements(ctx context.Context) (int, error) {
	var slots []store.ReadingBookSlot
	if err := c.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		slots, err = tx.SlotsToReplace(ctx, c.now(), 10)
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, s := range slots {
		changed, err := c.proposeReplacement(ctx, s)
		if err != nil {
			return n, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

func (c *Coordinator) proposeReplacement(ctx context.Context, s store.ReadingBookSlot) (bool, error) {
	var (
		in      ReplacementInput
		bp      BookPlanner
		skipped bool
	)
	if err := c.st.Tx(ctx, func(tx *store.Tx) error {
		cur, err := tx.ReadingBookSlot(ctx, s.ID)
		if errors.Is(err, store.ErrNotFound) || err == nil && (cur.AssignmentStatus != store.AssignChangeRequested || len(cur.ExcludedMemberIDs) != len(s.ExcludedMemberIDs)) {
			skipped = true
			return nil
		}
		if err != nil {
			return err
		}
		b, err := tx.ReadingBook(ctx, cur.BookID)
		if err != nil {
			return err
		}
		if bp, err = c.bookPlannerFor(ctx, tx, b); err != nil {
			return err
		}
		in, err = c.replacementInput(ctx, tx, b, cur)
		return err
	}); err != nil {
		return false, err
	}
	if skipped {
		return false, nil
	}
	candidate, usage, planErr := c.bookAgent.ProposeReplacement(ctx, ReplacementRequest{
		Input: in, Planner: bp, MaxLLMCalls: MaxLLMCallsPerRun,
		Check: func(id string) error { return bp.ValidateReplacement(in, id) },
	})

	changed := false
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		if err := recordBookUsage(ctx, tx, s.BookID, usage, now); err != nil {
			return err
		}
		cur, err := tx.ReadingBookSlot(ctx, s.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if cur.AssignmentStatus != store.AssignChangeRequested || len(cur.ExcludedMemberIDs) != len(s.ExcludedMemberIDs) {
			return nil // 処理中に状態が変わった。古い候補は使わない
		}
		b, err := tx.ReadingBook(ctx, cur.BookID)
		if err != nil {
			return err
		}
		changed = true
		if planErr != nil {
			c.log.Warn("担当変更の候補の作成に失敗", "slot", cur.ID, "attempts", cur.ChangeAttempts, "err", planErr)
		}
		switch {
		case errors.Is(planErr, ErrNoReplacement):
			return c.slotNeedsAttention(ctx, tx, b, cur, "no_replacement_candidate", "担当変更の候補になれるメンバーがいません。", now)
		case errors.Is(planErr, ErrTransient):
			cur.ChangeAttempts++
			if cur.ChangeAttempts >= bookPlanMaxAttempts {
				return c.slotNeedsAttention(ctx, tx, b, cur, "ai_unavailable", "AIの応答に繰り返し失敗したため、担当変更の候補を作れませんでした。", now)
			}
			next := now.Add(c.retryDelay(cur.ChangeAttempts))
			cur.ChangeNextAt = &next
			return tx.UpdateReadingBookSlot(ctx, cur)
		case errors.Is(planErr, ErrBudgetExceeded):
			return c.slotNeedsAttention(ctx, tx, b, cur, "budget_exceeded", "AIの呼び出し回数・費用の上限に達したため、担当変更の候補を作れませんでした。", now)
		case planErr != nil:
			return c.slotNeedsAttention(ctx, tx, b, cur, "model_error", "AIが条件を満たす担当変更の候補を返せませんでした。", now)
		}
		fresh, err := c.replacementInput(ctx, tx, b, cur)
		if err != nil {
			return err
		}
		if err := bp.ValidateReplacement(fresh, candidate); err != nil {
			return c.slotNeedsAttention(ctx, tx, b, cur, "plan_invalid", "担当変更の候補が最新の条件を満たさなくなったため、確定できませんでした。", now)
		}
		members, err := tx.Members(ctx, b.GroupID)
		if err != nil {
			return err
		}
		cm, ok := memberByID(members, candidate)
		if !ok {
			return fmt.Errorf("coord: 候補のメンバーが見つかりません")
		}
		cur.ProposedAssigneeID, cur.AssignmentStatus, cur.ChangeAttempts, cur.ChangeNextAt = candidate, store.AssignChangeProposed, 0, nil
		if err := tx.UpdateReadingBookSlot(ctx, cur); err != nil {
			return err
		}
		if err := bookLog(ctx, tx, b, cur.ID, candidate, "candidate_proposed", fmt.Sprintf("第%d回の担当変更の候補を提案しました。候補本人の承認を待っています。", cur.SequenceNumber), now); err != nil {
			return err
		}
		text := fmt.Sprintf("第%d回の担当の候補として提案されました。あなたが承認するまで担当は変わりません。引き受けられるか回答してください。", cur.SequenceNumber)
		return c.enqueueBookDM(ctx, tx, b, NotifyBookAssigneeChange, fmt.Sprintf("book_candidate:%s:%s:%d", cur.ID, candidate, len(cur.ExcludedMemberIDs)), text, []store.Member{cm}, now)
	})
	return changed, err
}
