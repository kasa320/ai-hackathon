package coord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// processBookWork はブックの自動進行を1回分進める。状態はすべてDBから導くため、再起動後も
// 同じイベントを再処理しても、セッション・タスク・通知は重複して作られない。
// 進めた件数を返す（0なら、いま進められるものはない）。
func (c *Coordinator) processBookWork(ctx context.Context) (int, error) {
	total := 0
	for _, step := range []func(context.Context) (int, error){
		c.processDepartedAssignees,
		c.processBookPlans,
		c.processReplacements,
		c.processSlotStarts,
		c.processAssigneeConfirmations,
	} {
		n, err := step(ctx)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

type dueSlot struct{ bookID, slotID string }

// processSlotStarts は、計画が成立したブックで、調整開始日になった枠だけの実セッションと
// 参加条件タスクを一度だけ作る。開始日を過ぎていた最初の枠は、計画の成立後に開始される。
func (c *Coordinator) processSlotStarts(ctx context.Context) (int, error) {
	var due []dueSlot
	if err := c.st.Tx(ctx, func(tx *store.Tx) error {
		books, err := tx.ApprovedBooks(ctx)
		if err != nil {
			return err
		}
		now := c.now()
		for _, b := range books {
			slots, err := tx.ReadingBookSlots(ctx, b.ID)
			if err != nil {
				return err
			}
			for _, s := range slots {
				if slotDue(b, s, now) {
					due = append(due, dueSlot{b.ID, s.ID})
				}
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		changed, err := c.startSlot(ctx, d)
		if err != nil {
			return n, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// slotDue は枠の自動開始の条件：未開始で、担当者が本人の承認済みで、調整開始日を迎えている。
func slotDue(b store.ReadingBook, s store.ReadingBookSlot, now time.Time) bool {
	if b.PlanStatus != store.BookPlanApproved || b.Status != "in_progress" {
		return false
	}
	if s.Status != "planned" || s.SessionID != nil || s.AssigneeMemberID == "" || s.AssignmentStatus != store.AssignAccepted {
		return false
	}
	at := adjustmentStart(b, s)
	return !at.IsZero() && !now.Before(at)
}

// errSkipSlot は開始をやめて、そのトランザクションでの書き込みをすべて取り消すための印。
var errSkipSlot = errors.New("coord: 枠を開始しない")

func (c *Coordinator) startSlot(ctx context.Context, d dueSlot) (bool, error) {
	var (
		attention *apperr.Error
		started   bool
	)
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		b, err := tx.ReadingBook(ctx, d.bookID)
		if errors.Is(err, store.ErrNotFound) {
			return errSkipSlot
		}
		if err != nil {
			return err
		}
		s, err := tx.ReadingBookSlot(ctx, d.slotID)
		if err != nil {
			return err
		}
		// 条件を最新の状態で確認し直す（二重の開始・担当変更中の開始を防ぐ）。
		if !slotDue(b, s, now) {
			return errSkipSlot
		}
		g, err := tx.Group(ctx, b.GroupID)
		if err != nil {
			return err
		}
		members, err := tx.Members(ctx, b.GroupID)
		if err != nil {
			return err
		}
		if _, ok := memberByID(members, s.AssigneeMemberID); !ok {
			return errSkipSlot // 担当者が脱退した。担当変更の処理が先に行われる
		}
		sess, err := c.createReadingSessionTx(ctx, tx, g, members, b, &s, apitypes.ReadingBookSessionInput{
			PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd, DurationMinutes: b.DurationMinutes, TargetSectionIDs: s.TargetSectionIDs,
		}, s.AssigneeMemberID)
		if err != nil {
			var ae *apperr.Error
			if errors.As(err, &ae) {
				attention = ae
				return errSkipSlot
			}
			return err
		}
		ok, err := tx.StartReadingBookSlot(ctx, s.ID, sess.ID)
		if err != nil {
			return err
		}
		if !ok {
			return errSkipSlot
		}
		if s.AttentionReason != "" {
			s.AttentionReason = ""
			if err := tx.UpdateReadingBookSlot(ctx, s); err != nil {
				return err
			}
		}
		started = true
		return bookLog(ctx, tx, b, s.ID, s.AssigneeMemberID, "session_started", fmt.Sprintf("第%d回の調整開始日になったため、実セッションと参加条件の確認を始めました。", s.SequenceNumber), now)
	})
	if err != nil && !errors.Is(err, errSkipSlot) {
		return false, err
	}
	if attention == nil {
		return started, nil
	}
	return c.markSlotAttention(ctx, d, attention)
}

// markSlotAttention は自動開始できない理由を枠に残し、管理者へ一度だけ知らせる。
// 条件が解消されるまで枠は未開始のまま残り、解消すれば次の処理で開始される。
func (c *Coordinator) markSlotAttention(ctx context.Context, d dueSlot, ae *apperr.Error) (bool, error) {
	changed := false
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		b, err := tx.ReadingBook(ctx, d.bookID)
		if err != nil {
			return err
		}
		s, err := tx.ReadingBookSlot(ctx, d.slotID)
		if err != nil {
			return err
		}
		if s.AttentionReason == ae.Code {
			return nil
		}
		s.AttentionReason = ae.Code
		if err := tx.UpdateReadingBookSlot(ctx, s); err != nil {
			return err
		}
		changed = true
		summary := fmt.Sprintf("第%d回を自動で開始できませんでした：%s", s.SequenceNumber, ae.Message)
		if err := bookLog(ctx, tx, b, s.ID, "", "session_start_blocked", summary, now); err != nil {
			return err
		}
		members, err := tx.Members(ctx, b.GroupID)
		if err != nil {
			return err
		}
		if owner, ok := ownerMember(members); ok {
			return c.enqueueBookDM(ctx, tx, b, NotifyBookAttention, fmt.Sprintf("book_slot_attention:%s:%s", s.ID, ae.Code), summary+"\n管理者の対応が必要です。", []store.Member{owner}, now)
		}
		return nil
	})
	return changed, err
}

// stageAt は各段階の実行時刻。前の段階との間に最低限の間隔を空け、一度に連続して送らない。
func stageAt(start time.Time, lead time.Duration, previous time.Time) time.Time {
	at := start.Add(-lead)
	if min := previous.Add(confirmStageGap); at.Before(min) {
		return min
	}
	return at
}

// processAssigneeConfirmations は日時が確定した枠の担当者へ、開催3日前に最終確認を一度だけ依頼し、
// 2日前に未回答なら一度だけ催促し、1日前でも未回答なら管理者判断待ちとして管理者へ知らせる。
// 自動で別の担当者に確定することはしない。
func (c *Coordinator) processAssigneeConfirmations(ctx context.Context) (int, error) {
	n := 0
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		now := c.now()
		books, err := tx.ApprovedBooks(ctx)
		if err != nil {
			return err
		}
		for _, b := range books {
			slots, err := tx.ReadingBookSlots(ctx, b.ID)
			if err != nil {
				return err
			}
			members, err := tx.Members(ctx, b.GroupID)
			if err != nil {
				return err
			}
			for _, s := range slots {
				if s.Status != "active" || s.SessionID == nil || s.AssigneeMemberID == "" || s.AssignmentStatus != store.AssignAccepted {
					continue
				}
				sess, err := tx.Session(ctx, *s.SessionID)
				if err != nil {
					return err
				}
				if scheduleStatus(sess) != store.ScheduleConfirmed || sess.Status != sessionConfirm || !now.Before(sess.StartsAt) {
					continue
				}
				assignee, ok := memberByID(members, s.AssigneeMemberID)
				if !ok {
					continue
				}
				did, err := c.stepConfirmation(ctx, tx, b, s, sess, assignee, members, now)
				if err != nil {
					return err
				}
				if did {
					n++
				}
			}
		}
		return nil
	})
	return n, err
}

func (c *Coordinator) stepConfirmation(ctx context.Context, tx *store.Tx, b store.ReadingBook, s store.ReadingBookSlot, sess store.Session, assignee store.Member, members []store.Member, now time.Time) (bool, error) {
	when := formatClock(sess.StartsAt)
	conf, err := tx.SlotConfirmation(ctx, s.ID, assignee.ID)
	if errors.Is(err, store.ErrNotFound) {
		if now.Before(sess.StartsAt.Add(-confirmLead)) {
			return false, nil
		}
		created, err := tx.CreateSlotConfirmation(ctx, store.SlotConfirmation{ID: store.NewID("conf"), SlotID: s.ID, MemberID: assignee.ID, Status: store.ConfirmOpen, RequestedAt: now})
		if err != nil || !created {
			return false, err
		}
		if err := bookLog(ctx, tx, b, s.ID, assignee.ID, "confirmation_requested", fmt.Sprintf("第%d回の開催3日前になったため、担当者へ最終確認を依頼しました。", s.SequenceNumber), now); err != nil {
			return false, err
		}
		text := fmt.Sprintf("第%d回は %s に開催予定です。担当を予定どおり務められるか確認してください。難しい場合は変更を希望できます。", s.SequenceNumber, when)
		return true, c.enqueueBookDM(ctx, tx, b, NotifyBookAssigneeConfirm, fmt.Sprintf("book_confirm:%s:%s", s.ID, assignee.ID), text, []store.Member{assignee}, now)
	}
	if err != nil {
		return false, err
	}
	if conf.Status != store.ConfirmOpen {
		return false, nil
	}
	switch {
	case conf.RemindedAt == nil:
		if now.Before(stageAt(sess.StartsAt, remindLead, conf.RequestedAt)) {
			return false, nil
		}
		conf.RemindedAt = &now
		if err := tx.UpdateSlotConfirmation(ctx, conf); err != nil {
			return false, err
		}
		if err := bookLog(ctx, tx, b, s.ID, assignee.ID, "confirmation_reminded", fmt.Sprintf("第%d回の担当者が未回答のため、一度だけ催促しました。", s.SequenceNumber), now); err != nil {
			return false, err
		}
		text := fmt.Sprintf("第%d回（%s 開催予定）の担当の確認がまだです。ご回答をお願いします。", s.SequenceNumber, when)
		return true, c.enqueueBookDM(ctx, tx, b, NotifyBookAssigneeRemind, fmt.Sprintf("book_remind:%s:%s", s.ID, assignee.ID), text, []store.Member{assignee}, now)
	case conf.EscalatedAt == nil:
		if now.Before(stageAt(sess.StartsAt, escalationLead, *conf.RemindedAt)) {
			return false, nil
		}
		conf.Status, conf.EscalatedAt = store.ConfirmNeedsOwner, &now
		if err := tx.UpdateSlotConfirmation(ctx, conf); err != nil {
			return false, err
		}
		if err := bookLog(ctx, tx, b, s.ID, assignee.ID, "confirmation_escalated", fmt.Sprintf("開催1日前でも担当者が未回答のため、管理者判断待ちにしました。第%d回", s.SequenceNumber), now); err != nil {
			return false, err
		}
		owner, ok := ownerMember(members)
		if !ok {
			return true, nil
		}
		text := fmt.Sprintf("第%d回（%s 開催予定）の担当者 %s さんが、開催1日前でも未回答です。管理者の判断が必要です（自動では担当を交代しません）。", s.SequenceNumber, when, assignee.DisplayName)
		return true, c.enqueueBookDM(ctx, tx, b, NotifyBookAssigneeEscalate, fmt.Sprintf("book_escalate:%s:%s", s.ID, assignee.ID), text, []store.Member{owner}, now)
	}
	return false, nil
}
