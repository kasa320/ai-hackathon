package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// GroupNotification は、開催回に属さないグループ操作を1人へ知らせるDM配送。
type GroupNotification struct {
	ID                     string
	GroupID                string
	Kind                   string
	RecipientDiscordUserID string
	DedupeKey              string
	Content                string
	// Components は通知に添えるボタン。
	Components []NotifyButton
	Status     string
	ErrorCode  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const groupNotificationCols = "id, group_id, kind, recipient_discord_user_id, dedupe_key, content, components, status, error_code, created_at, updated_at"

func scanGroupNotification(row interface{ Scan(...any) error }) (GroupNotification, error) {
	var n GroupNotification
	var code sql.NullString
	var components, created, updated string
	if err := row.Scan(&n.ID, &n.GroupID, &n.Kind, &n.RecipientDiscordUserID, &n.DedupeKey, &n.Content, &components, &n.Status, &code, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return n, ErrNotFound
		}
		return n, err
	}
	_ = json.Unmarshal([]byte(components), &n.Components)
	n.ErrorCode = code.String
	n.CreatedAt, n.UpdatedAt = parseTS(created), parseTS(updated)
	return n, nil
}

// EnqueueGroupNotification は受信者1人分のDMを登録する。同じ操作・受信者への二重登録はしない。
func (t *Tx) EnqueueGroupNotification(ctx context.Context, n GroupNotification) error {
	seq, err := t.nextSeq(ctx, "group_notifications")
	if err != nil {
		return err
	}
	components, _ := json.Marshal(nonNilButtons(n.Components))
	return t.exec(ctx, `INSERT INTO group_notifications (`+groupNotificationCols+`, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', NULL, ?, ?, ?)
		ON CONFLICT (dedupe_key) DO NOTHING`, n.ID, n.GroupID, n.Kind, n.RecipientDiscordUserID,
		n.DedupeKey, n.Content, string(components), ts(n.CreatedAt), ts(n.CreatedAt), seq)
}

func (t *Tx) ClaimGroupNotification(ctx context.Context, now time.Time) (GroupNotification, error) {
	n, err := scanGroupNotification(t.row(ctx, "SELECT "+groupNotificationCols+" FROM group_notifications WHERE status = 'pending' ORDER BY seq LIMIT 1"))
	if err != nil {
		return n, err
	}
	if err := t.exec(ctx, "UPDATE group_notifications SET status = 'sending', updated_at = ? WHERE id = ?", ts(now), n.ID); err != nil {
		return n, err
	}
	n.Status = NotifySending
	return n, nil
}

func (t *Tx) SetGroupNotificationStatus(ctx context.Context, id, status, errorCode string, now time.Time) error {
	return t.exec(ctx, "UPDATE group_notifications SET status = ?, error_code = ?, updated_at = ? WHERE id = ?", status, nullStr(errorCode), ts(now), id)
}

func (t *Tx) MarkInterruptedGroupNotificationsUnknown(ctx context.Context, now time.Time) ([]GroupNotification, error) {
	rows, err := t.query(ctx, "SELECT "+groupNotificationCols+" FROM group_notifications WHERE status = 'sending'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupNotification
	for rows.Next() {
		n, err := scanGroupNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, n := range out {
		if err := t.SetGroupNotificationStatus(ctx, n.ID, NotifyUnknown, "delivery_unknown", now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (t *Tx) GroupNotifications(ctx context.Context, groupID string) ([]GroupNotification, error) {
	rows, err := t.query(ctx, "SELECT "+groupNotificationCols+" FROM group_notifications WHERE group_id = ? ORDER BY seq", groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupNotification
	for rows.Next() {
		n, err := scanGroupNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
