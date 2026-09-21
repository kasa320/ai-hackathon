package store

import (
	"context"
	"time"
)

// PreparationTarget は本人が参加条件を更新できる開催回。Discord の対話で対象を選ぶのに使う。
// 開催回をまたいで引くため、案件単位の TasksByCase では代わりにならない。
type PreparationTarget struct {
	Session  Session
	MemberID string
	// HasOpenTask は本人宛ての未回答・期限内の参加条件確認タスクがあること。
	HasOpenTask bool
}

// PreparationTargetsByUser は本人が所属し、まだ開催していない開催回を開催が近い順に返す。
// 所属していない開催回は返さない（存在も知らせない）。
func (t *Tx) PreparationTargetsByUser(ctx context.Context, userID string, now time.Time, limit int) ([]PreparationTarget, error) {
	const cols = "s.id, s.group_id, s.playbook_id, s.title, s.starts_at, s.duration_minutes, s.revision, s.status, s.data, s.confirmed_proposal_id, s.created_at, s.updated_at"
	rows, err := t.query(ctx, `SELECT `+cols+`, m.id,
    EXISTS (SELECT 1 FROM tasks tk WHERE tk.session_id = s.id AND tk.member_id = m.id
            AND tk.kind = 'preparation' AND tk.status = 'open' AND tk.due_at > ?)
FROM sessions s
JOIN session_members sm ON sm.session_id = s.id
JOIN members m ON m.id = sm.member_id
WHERE m.user_id = ? AND s.starts_at > ? AND m.left_at IS NULL
  AND EXISTS (SELECT 1 FROM groups g WHERE g.id = s.group_id AND g.deleted_at IS NULL)
ORDER BY s.starts_at, s.id
LIMIT ?`, ts(now), userID, ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PreparationTarget{}
	for rows.Next() {
		var tg PreparationTarget
		s, err := scanSessionWith(rows, &tg.MemberID, &tg.HasOpenTask)
		if err != nil {
			return nil, err
		}
		tg.Session = s
		out = append(out, tg)
	}
	return out, rows.Err()
}
