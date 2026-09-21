package store

import (
	"context"
	"time"
)

// LeaveMember は履歴の参照先を残し、所属と本人宛ての未処理依頼を無効にする。
func (t *Tx) LeaveMember(ctx context.Context, m Member, now time.Time) error {
	if err := t.exec(ctx, "UPDATE members SET left_at = ? WHERE id = ? AND role = 'member' AND left_at IS NULL", ts(now), m.ID); err != nil {
		return err
	}
	if err := t.exec(ctx, "UPDATE tasks SET status = 'obsolete' WHERE member_id = ? AND status = 'open'", m.ID); err != nil {
		return err
	}
	if err := t.exec(ctx, "UPDATE events SET status = 'cancelled' WHERE status = 'pending' AND ref_id IN (SELECT id FROM tasks WHERE member_id = ?)", m.ID); err != nil {
		return err
	}
	return t.exec(ctx, `UPDATE notifications SET status = 'cancelled', updated_at = ?
		WHERE status = 'pending' AND kind IN ('task_requested', 'reminder')
		AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)
		AND EXISTS (SELECT 1 FROM json_each(notifications.mentions) WHERE value = ?)`, ts(now), m.GroupID, m.DiscordUserID)
}

// DeleteGroup は論理削除する。削除のお知らせ用キューと過去の業務データは保持する。
func (t *Tx) DeleteGroup(ctx context.Context, groupID string, now time.Time) error {
	for _, q := range []string{
		"UPDATE groups SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
		"UPDATE sessions SET revision = revision + 1, updated_at = ? WHERE group_id = ?",
		"UPDATE notifications SET status = 'cancelled', updated_at = ? WHERE status = 'pending' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)",
		"UPDATE group_notifications SET status = 'cancelled', updated_at = ? WHERE status = 'pending' AND group_id = ?",
	} {
		if err := t.exec(ctx, q, ts(now), groupID); err != nil {
			return err
		}
	}
	for _, q := range []string{
		"UPDATE events SET status = 'cancelled' WHERE status = 'pending' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)",
		"UPDATE tasks SET status = 'obsolete' WHERE status = 'open' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)",
		"UPDATE proposals SET status = 'superseded' WHERE status = 'pending' AND session_id IN (SELECT id FROM sessions WHERE group_id = ?)",
	} {
		if err := t.exec(ctx, q, groupID); err != nil {
			return err
		}
	}
	return nil
}

// EventActive は先に取得したイベントも含め、削除・取消後に実行しないための再確認。
func (t *Tx) EventActive(ctx context.Context, eventID string) (bool, error) {
	var active bool
	err := t.row(ctx, `SELECT EXISTS (SELECT 1 FROM events e
		JOIN sessions s ON s.id = e.session_id JOIN groups g ON g.id = s.group_id
		WHERE e.id = ? AND e.status = 'pending' AND g.deleted_at IS NULL)`, eventID).Scan(&active)
	return active, err
}
