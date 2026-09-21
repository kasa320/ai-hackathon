package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// GroupNotification は開催回に依存しない通知。本文と宛先は操作確定時のもの。
type GroupNotification struct {
	ID, GroupID, Kind, DedupeKey, Content string
	Mentions []string
	Status, ErrorCode string
	CreatedAt, UpdatedAt time.Time
}

const groupNotificationCols = "id, group_id, kind, dedupe_key, content, mentions, status, error_code, created_at, updated_at"

func scanGroupNotification(row interface{ Scan(...any) error }) (GroupNotification, error) {
	var n GroupNotification
	var mentions, created, updated string
	var code sql.NullString
	if err := row.Scan(&n.ID, &n.GroupID, &n.Kind, &n.DedupeKey, &n.Content, &mentions, &n.Status, &code, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) { return n, ErrNotFound }
		return n, err
	}
	if err := json.Unmarshal([]byte(mentions), &n.Mentions); err != nil { return n, err }
	n.ErrorCode, n.CreatedAt, n.UpdatedAt = code.String, parseTS(created), parseTS(updated)
	return n, nil
}

func (t *Tx) EnqueueGroupNotification(ctx context.Context, n GroupNotification) error {
	seq, err := t.nextSeq(ctx, "group_notifications")
	if err != nil { return err }
	mentions, err := json.Marshal(nonNilStrings(n.Mentions))
	if err != nil { return err }
	return t.exec(ctx, "INSERT INTO group_notifications ("+groupNotificationCols+", seq) VALUES (?, ?, ?, ?, ?, ?, 'pending', NULL, ?, ?, ?) ON CONFLICT (dedupe_key) DO NOTHING",
		n.ID, n.GroupID, n.Kind, n.DedupeKey, n.Content, string(mentions), ts(n.CreatedAt), ts(n.CreatedAt), seq)
}

func (t *Tx) ClaimGroupNotification(ctx context.Context, now time.Time) (GroupNotification, error) {
	n, err := scanGroupNotification(t.row(ctx, "SELECT "+groupNotificationCols+" FROM group_notifications WHERE status = 'pending' ORDER BY seq LIMIT 1"))
	if err != nil { return n, err }
	if err := t.SetGroupNotificationStatus(ctx, n.ID, NotifySending, "", now); err != nil { return n, err }
	n.Status = NotifySending
	return n, nil
}

func (t *Tx) SetGroupNotificationStatus(ctx context.Context, id, status, code string, now time.Time) error {
	return t.exec(ctx, "UPDATE group_notifications SET status = ?, error_code = ?, updated_at = ? WHERE id = ?", status, nullStr(code), ts(now), id)
}

func (t *Tx) RecoverGroupNotifications(ctx context.Context, now time.Time) error {
	return t.exec(ctx, "UPDATE group_notifications SET status = 'unknown', error_code = 'delivery_unknown', updated_at = ? WHERE status = 'sending'", ts(now))
}

func (t *Tx) GroupNotifications(ctx context.Context, groupID string) ([]GroupNotification, error) {
	rows, err := t.query(ctx, "SELECT "+groupNotificationCols+" FROM group_notifications WHERE group_id = ? ORDER BY seq", groupID)
	if err != nil { return nil, err }
	defer rows.Close()
	out := []GroupNotification{}
	for rows.Next() {
		n, err := scanGroupNotification(rows)
		if err != nil { return nil, err }
		out = append(out, n)
	}
	return out, rows.Err()
}
