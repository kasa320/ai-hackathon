package store

import (
	"context"
	"time"
)

// SessionsForMember はグループ内で、指定メンバーが固定されている開催回を返す。
func (t *Tx) SessionsForMember(ctx context.Context, groupID, memberID string) ([]Session, error) {
	rows, err := t.query(ctx, `SELECT `+sessionCols+` FROM sessions s
		JOIN session_members sm ON sm.session_id = s.id
		WHERE s.group_id = ? AND sm.member_id = ?
		ORDER BY s.starts_at, s.id`, groupID, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (t *Tx) CloseOpenTasksForMember(ctx context.Context, sessionID, memberID string) error {
	if err := t.exec(ctx, `UPDATE events SET status = 'cancelled'
		WHERE session_id = ? AND status = 'pending'
		AND ref_id IN (SELECT id FROM tasks WHERE session_id = ? AND member_id = ?)`, sessionID, sessionID, memberID); err != nil {
		return err
	}
	return t.exec(ctx, "UPDATE tasks SET status = 'obsolete' WHERE session_id = ? AND member_id = ? AND status = 'open'", sessionID, memberID)
}

func (t *Tx) CancelPendingMemberNotifications(ctx context.Context, sessionID, discordUserID string, now time.Time) error {
	return t.exec(ctx, `UPDATE notifications SET status = 'cancelled', error_code = 'member_left', updated_at = ?
		WHERE session_id = ? AND status = 'pending'
		AND EXISTS (SELECT 1 FROM json_each(notifications.mentions) WHERE value = ?)`, ts(now), sessionID, discordUserID)
}

// CancelGroupWork は論理削除されたグループで、まだ外部作用を起こしていない処理を止める。
func (t *Tx) CancelGroupWork(ctx context.Context, groupID string, now time.Time) error {
	queries := []struct {
		q    string
		args []any
	}{
		{"UPDATE tasks SET status = 'obsolete' WHERE status = 'open' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)", []any{groupID}},
		{"UPDATE events SET status = 'cancelled' WHERE status = 'pending' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)", []any{groupID}},
		{"UPDATE notifications SET status = 'cancelled', error_code = 'group_deleted', updated_at = ? WHERE status = 'pending' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)", []any{ts(now), groupID}},
		{"UPDATE proposals SET status = 'superseded' WHERE status = 'pending' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)", []any{groupID}},
		{"UPDATE reading_toc_lookups SET status = 'failed', reason_code = 'group_deleted' WHERE group_id = ? AND status IN ('resolving_book', 'searching', 'verifying', 'reading_image')", []any{groupID}},
	}
	for _, item := range queries {
		if err := t.exec(ctx, item.q, item.args...); err != nil {
			return err
		}
	}
	return nil
}
