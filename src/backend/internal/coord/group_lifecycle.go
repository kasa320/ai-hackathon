package coord

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

type preparedLeave struct {
	session store.Session
	data    []byte
}

func isRunning(sess store.Session, now time.Time) bool {
	return scheduleStatus(sess) == store.ScheduleConfirmed && !now.Before(sess.StartsAt) && now.Before(sess.StartsAt.Add(time.Duration(sess.DurationMinutes)*time.Minute))
}

func ensureNoRunningSession(sessions []store.Session, now time.Time) error {
	for _, sess := range sessions {
		if isRunning(sess, now) {
			return apperr.InvalidStateErr("「%s」の開催中は、この操作を行えません。終了後にもう一度お試しください。", sess.Title)
		}
	}
	return nil
}

func enqueueGroupDM(ctx context.Context, tx *store.Tx, groupID, kind, operationKey, content string, recipients []store.Member, now time.Time) (int, error) {
	queued := 0
	for _, m := range recipients {
		if m.DiscordUserID == "" {
			continue
		}
		if err := tx.EnqueueGroupNotification(ctx, store.GroupNotification{
			ID:                     store.NewID("gntf"),
			GroupID:                groupID,
			Kind:                   kind,
			RecipientDiscordUserID: m.DiscordUserID,
			DedupeKey:              operationKey + ":" + m.DiscordUserID,
			Content:                content,
			CreatedAt:              now,
		}); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// LeaveGroup は一般メンバー本人を脱退させ、開始前の開催回を欠席・再調整にする。
func (c *Coordinator) LeaveGroup(ctx context.Context, userID, groupID string, _ apitypes.LeaveGroupInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, me, err := c.groupAccess(ctx, tx, userID, groupID)
		if err != nil {
			return store.Response{}, err
		}
		if me.Role == RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		all, err := tx.SessionsByGroup(ctx, groupID)
		if err != nil {
			return store.Response{}, err
		}
		if err := ensureNoRunningSession(all, now); err != nil {
			return store.Response{}, err
		}

		memberSessions, err := tx.SessionsForMember(ctx, groupID, me.ID)
		if err != nil {
			return store.Response{}, err
		}
		prepared := make([]preparedLeave, 0, len(memberSessions))
		for _, sess := range memberSessions {
			if sess.StartsAt.Before(now) {
				continue
			}
			pb, err := c.playbook(sess.PlaybookID)
			if err != nil {
				return store.Response{}, err
			}
			snap, err := c.snapshot(ctx, tx, sess, nil)
			if err != nil {
				return store.Response{}, err
			}
			preps, err := tx.Preparations(ctx, sess.ID)
			if err != nil {
				return store.Response{}, err
			}
			var current []byte
			if p, ok := preps[me.ID]; ok {
				current = p.Data
			}
			data, err := pb.ApplyWithdrawal(ctx, snap, WithdrawAttendance, current)
			if err != nil {
				return store.Response{}, err
			}
			prepared = append(prepared, preparedLeave{session: sess, data: data})
		}

		members, err := tx.Members(ctx, groupID)
		if err != nil {
			return store.Response{}, err
		}
		remaining := make([]store.Member, 0, len(members)-1)
		for _, m := range members {
			if m.ID != me.ID {
				remaining = append(remaining, m)
			}
		}
		if ok, err := tx.LeaveMember(ctx, me.ID, now); err != nil {
			return store.Response{}, err
		} else if !ok {
			return store.Response{}, apperr.NotFoundErr()
		}

		for _, item := range prepared {
			sess := item.session
			if err := tx.PutPreparation(ctx, store.Preparation{SessionID: sess.ID, MemberID: me.ID, Attendance: AttendanceAbsent, Data: item.data, UpdatedAt: now}); err != nil {
				return store.Response{}, err
			}
			if err := tx.CloseOpenTasksForMember(ctx, sess.ID, me.ID); err != nil {
				return store.Response{}, err
			}
			if err := tx.CancelPendingMemberNotifications(ctx, sess.ID, me.DiscordUserID, now); err != nil {
				return store.Response{}, err
			}
			if err := bump(ctx, tx, &sess, now); err != nil {
				return store.Response{}, err
			}
			cs, err := c.onMembershipChanged(ctx, tx, &sess, me.ID, now)
			if err != nil {
				return store.Response{}, err
			}
			if err := c.activity(ctx, tx, sess, cs.ID, "member_left", me.DisplayName+"さんの脱退に伴い、欠席として再調整を開始しました。", "", now); err != nil {
				return store.Response{}, err
			}
		}

		content := fmt.Sprintf("【%s】%sさんがグループを脱退しました。", g.Name, me.DisplayName)
		queued, err := enqueueGroupDM(ctx, tx, groupID, "member_left", "member_left:"+me.ID, content, remaining, now)
		if err != nil {
			return store.Response{}, err
		}
		return store.Response{Status: http.StatusOK, Body: encode(apitypes.GroupLifecycleResult{
			GroupID: groupID, Status: "left", AffectedSessionCount: len(prepared), NotificationCount: queued,
		})}, nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// DeleteGroup は管理者がグループを論理削除し、削除直前の在籍者へ個別DMを登録する。
func (c *Coordinator) DeleteGroup(ctx context.Context, userID, groupID string, in apitypes.DeleteGroupInput, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	return c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, me, err := c.groupAccess(ctx, tx, userID, groupID)
		if err != nil {
			return store.Response{}, err
		}
		if me.Role != RoleOwner {
			return store.Response{}, apperr.ForbiddenErr()
		}
		sessions, err := tx.SessionsByGroup(ctx, groupID)
		if err != nil {
			return store.Response{}, err
		}
		if err := ensureNoRunningSession(sessions, now); err != nil {
			return store.Response{}, err
		}
		members, err := tx.Members(ctx, groupID)
		if err != nil {
			return store.Response{}, err
		}
		content := fmt.Sprintf("【%s】管理者によりグループが削除されました。", g.Name)
		queued, err := enqueueGroupDM(ctx, tx, groupID, "group_deleted", "group_deleted:"+groupID, content, members, now)
		if err != nil {
			return store.Response{}, err
		}
		if err := tx.CancelGroupWork(ctx, groupID, now); err != nil {
			return store.Response{}, err
		}
		if ok, err := tx.DeleteGroup(ctx, groupID, now); err != nil {
			return store.Response{}, err
		} else if !ok {
			return store.Response{}, apperr.NotFoundErr()
		}
		return store.Response{Status: http.StatusOK, Body: encode(apitypes.GroupLifecycleResult{
			GroupID: groupID, Status: "deleted", AffectedSessionCount: len(sessions), NotificationCount: queued,
		})}, nil
	})
}
