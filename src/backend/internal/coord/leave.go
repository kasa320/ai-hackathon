package coord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// LeaveGroup は本人をグループから脱退させる（docs/api-endpoint.md）。
//
// 脱退できるのは本人だけで、他人を外す操作はない。管理者は脱退できない（先に交代が必要）。
// 在籍者が MinGroupSize を下回る脱退も断る。
//
// メンバーの行は消さず left_at を入れる。開催回に固定された参照（session_members・
// preparations・tasks）を壊さず、過去の開催回に誰が担当したかを残すため。
// 開始前の開催回からは欠席として外れ、辞退と同じ経路で再調整が始まる。開始済みの回は触らない。
func (c *Coordinator) LeaveGroup(ctx context.Context, userID, groupID string, idem *store.IdemKey) (store.Response, error) {
	now := c.now()
	res, err := c.st.Idempotent(ctx, idem, now, func(tx *store.Tx) (store.Response, error) {
		g, me, err := c.groupAccess(ctx, tx, userID, groupID)
		if err != nil {
			return store.Response{}, err
		}
		if me.Role == RoleOwner {
			return store.Response{}, apperr.InvalidStateErr("管理者は脱退できません。先に管理者を交代してください。")
		}
		members, err := tx.Members(ctx, g.ID)
		if err != nil {
			return store.Response{}, err
		}
		if len(members)-1 < MinGroupSize {
			return store.Response{}, apperr.InvalidStateErr("メンバーが%d人を下回るため脱退できません。", MinGroupSize)
		}
		if _, err := tx.LeaveGroup(ctx, me.ID, now); err != nil {
			return store.Response{}, err
		}

		affected, err := c.leaveUpcomingSessions(ctx, tx, g.ID, me, now)
		if err != nil {
			return store.Response{}, err
		}
		return store.Response{
			Status: http.StatusOK,
			Body:   encode(apitypes.LeaveGroupResult{GroupID: g.ID, AffectedSessionIDs: affected}),
		}, nil
	})
	if err == nil {
		c.Wake()
	}
	return res, err
}

// leaveUpcomingSessions は開始前の開催回から本人を欠席として外し、再調整を始める。
// 戻り値は実際に手を入れた開催回のID。
func (c *Coordinator) leaveUpcomingSessions(ctx context.Context, tx *store.Tx, groupID string, me store.Member, now time.Time) ([]string, error) {
	sessions, err := tx.SessionsByGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	affected := []string{}
	for _, sess := range sessions {
		// 開始済みの回は履歴として残す。
		if !now.Before(sess.StartsAt) {
			continue
		}
		fixed, err := tx.SessionMembers(ctx, sess.ID)
		if err != nil {
			return nil, err
		}
		if !containsMember(fixed, me.ID) {
			continue
		}
		pb, err := c.playbook(sess.PlaybookID)
		if err != nil {
			return nil, err
		}
		s, err := c.snapshot(ctx, tx, sess, nil)
		if err != nil {
			return nil, err
		}
		var curData []byte
		if cur := s.Preparation(me.ID); cur != nil {
			curData = cur.Data
		}
		data, err := pb.ApplyWithdrawal(ctx, s, WithdrawAttendance, curData)
		if err != nil {
			return nil, err
		}
		if err := tx.PutPreparation(ctx, store.Preparation{
			SessionID: sess.ID, MemberID: me.ID, Attendance: AttendanceAbsent, Data: data, UpdatedAt: now,
		}); err != nil {
			return nil, err
		}
		// 本人宛ての未回答の依頼は、もう答えようがないので無効にする。
		if err := closeOpenTasksForMember(ctx, tx, sess.ID, me.ID); err != nil {
			return nil, err
		}
		if err := bump(ctx, tx, &sess, now); err != nil {
			return nil, err
		}
		cs, err := c.onInputChanged(ctx, tx, &sess, me.ID, now)
		if err != nil {
			return nil, err
		}
		if err := c.activity(ctx, tx, sess, cs.ID, "input_received",
			fmt.Sprintf("%sさんがグループを脱退しました。", me.DisplayName), "", now); err != nil {
			return nil, err
		}
		affected = append(affected, sess.ID)
	}
	return affected, nil
}

// closeOpenTasksForMember は開催回のうち本人宛ての未回答タスクを obsolete にする。
func closeOpenTasksForMember(ctx context.Context, tx *store.Tx, sessionID, memberID string) error {
	cs, err := tx.LatestCase(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	tasks, err := tx.TasksByCase(ctx, cs.ID)
	if err != nil {
		return err
	}
	for _, tk := range tasks {
		if tk.MemberID == memberID && tk.Status == store.TaskOpen {
			if err := tx.SetTaskStatus(ctx, tk.ID, store.TaskObsolete); err != nil {
				return err
			}
		}
	}
	return nil
}

func containsMember(members []store.Member, id string) bool {
	for _, m := range members {
		if m.ID == id {
			return true
		}
	}
	return false
}
