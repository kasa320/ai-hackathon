package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// NotifyButton は通知に添えるボタン1個。CustomID はサーバー側で再検証できる opaque な文字列にする
// （値や識別子を直接埋め込まない）。
type NotifyButton struct {
	Label    string `json:"label"`
	CustomID string `json:"custom_id"`
	Primary  bool   `json:"primary,omitempty"`
}

// NotifyAction は通知のボタンから直接始める操作の参照
// （.agent/kasa/decisions/discord-availability-home-refresh.md）。
// Ref は種別ごとの対象（task_id・group_id/book_id/slot_id 等）を持つ JSON で、store は中身を解釈しない。
type NotifyAction struct {
	ID         string
	UserID     string
	Kind       string
	Ref        json.RawMessage
	Decisions  json.RawMessage
	ConsumedAt *time.Time
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

const notifyActionCols = "id, user_id, kind, ref, decisions, consumed_at, expires_at, created_at"

func scanNotifyAction(row interface{ Scan(...any) error }) (NotifyAction, error) {
	var a NotifyAction
	var ref, decisions string
	var consumed sql.NullString
	var expires, created string
	if err := row.Scan(&a.ID, &a.UserID, &a.Kind, &ref, &decisions, &consumed, &expires, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	a.Ref = json.RawMessage(ref)
	a.Decisions = json.RawMessage(decisions)
	if consumed.Valid {
		t := parseTS(consumed.String)
		a.ConsumedAt = &t
	}
	a.ExpiresAt, a.CreatedAt = parseTS(expires), parseTS(created)
	return a, nil
}

// InsertNotifyAction は通知のボタンが参照する操作を登録する。通知の作成と同じトランザクションで
// 呼び、束縛した本人以外には解決できないようにする。
func (t *Tx) InsertNotifyAction(ctx context.Context, a NotifyAction) error {
	if a.Decisions == nil {
		a.Decisions = json.RawMessage("[]")
	}
	return t.exec(ctx, `INSERT INTO notify_actions (`+notifyActionCols+`) VALUES (?, ?, ?, ?, ?, NULL, ?, ?)`,
		a.ID, a.UserID, a.Kind, string(a.Ref), string(a.Decisions), ts(a.ExpiresAt), ts(a.CreatedAt))
}

// NotifyAction は id で1件引く。見つからなければ ErrNotFound。
func (t *Tx) NotifyAction(ctx context.Context, id string) (NotifyAction, error) {
	return scanNotifyAction(t.row(ctx, "SELECT "+notifyActionCols+" FROM notify_actions WHERE id = ?", id))
}

// ConsumeNotifyAction は未消費・未期限切れの操作を消費済みにする。連打・イベント再送で二重に
// 処理しないよう、成功したのは最初の1回だけになる（ok=true）。
func (t *Tx) ConsumeNotifyAction(ctx context.Context, id string, now time.Time) (bool, error) {
	n, err := t.execN(ctx, "UPDATE notify_actions SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL AND expires_at > ?",
		ts(now), id, ts(now))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
