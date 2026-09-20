package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// 処理イベントの種類。
const (
	EventPlan     = "plan"     // AI による計画（再試行も同じ種類で予定する）
	EventDeadline = "deadline" // タスクの回答期限
	EventReminder = "reminder" // 期限の中間時点の催促
)

type Event struct {
	ID        string
	SessionID string
	CaseID    string
	Kind      string
	RefID     string
	RunAt     time.Time
	Status    string
	CreatedAt time.Time
}

const eventCols = "id, session_id, case_id, kind, ref_id, run_at, status, created_at"

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	var ref sql.NullString
	var run, created string
	if err := row.Scan(&e.ID, &e.SessionID, &e.CaseID, &e.Kind, &ref, &run, &e.Status, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return e, ErrNotFound
		}
		return e, err
	}
	e.RefID, e.RunAt, e.CreatedAt = ref.String, parseTS(run), parseTS(created)
	return e, nil
}

func (t *Tx) CreateEvent(ctx context.Context, e Event) error {
	seq, err := t.nextSeq(ctx, "events")
	if err != nil {
		return err
	}
	if e.Status == "" {
		e.Status = "pending"
	}
	return t.exec(ctx, "INSERT INTO events ("+eventCols+", seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.ID, e.SessionID, e.CaseID, e.Kind, nullStr(e.RefID), ts(e.RunAt), e.Status, ts(e.CreatedAt), seq)
}

// DueEvents は実行時刻を過ぎた未処理のイベントを古い順に返す。
func (t *Tx) DueEvents(ctx context.Context, now time.Time, limit int) ([]Event, error) {
	rows, err := t.query(ctx, "SELECT "+eventCols+" FROM events WHERE status = 'pending' AND run_at <= ? ORDER BY run_at, seq LIMIT ?", ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (t *Tx) Event(ctx context.Context, id string) (Event, error) {
	return scanEvent(t.row(ctx, "SELECT "+eventCols+" FROM events WHERE id = ?", id))
}

func (t *Tx) SetEventStatus(ctx context.Context, id, status string) error {
	return t.exec(ctx, "UPDATE events SET status = ? WHERE id = ?", status, id)
}

// CancelPendingEvents は案件の未処理イベントのうち指定種類のものを取り消す。
func (t *Tx) CancelPendingEvents(ctx context.Context, caseID, kind string) error {
	return t.exec(ctx, "UPDATE events SET status = 'cancelled' WHERE case_id = ? AND kind = ? AND status = 'pending'", caseID, kind)
}

// 通知の状態。
const (
	NotifyPending = "pending"
	NotifySending = "sending"
	NotifySent    = "sent"
	NotifyFailed  = "failed"
	NotifyUnknown = "unknown"
)

type Notification struct {
	ID        string
	SessionID string
	CaseID    string
	Kind      string
	DedupeKey string
	Content   string
	Mentions  []string // Discord のユーザーID
	Status    string
	ErrorCode string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const notificationCols = "id, session_id, case_id, kind, dedupe_key, content, mentions, status, error_code, created_at, updated_at"

func scanNotification(row interface{ Scan(...any) error }) (Notification, error) {
	var n Notification
	var mentions, created, updated string
	var code sql.NullString
	if err := row.Scan(&n.ID, &n.SessionID, &n.CaseID, &n.Kind, &n.DedupeKey, &n.Content, &mentions, &n.Status, &code, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return n, ErrNotFound
		}
		return n, err
	}
	_ = json.Unmarshal([]byte(mentions), &n.Mentions)
	n.ErrorCode, n.CreatedAt, n.UpdatedAt = code.String, parseTS(created), parseTS(updated)
	return n, nil
}

// EnqueueNotification は通知待ちを登録する。同じ dedupe_key は二重に登録しない。
func (t *Tx) EnqueueNotification(ctx context.Context, n Notification) error {
	seq, err := t.nextSeq(ctx, "notifications")
	if err != nil {
		return err
	}
	mentions, _ := json.Marshal(nonNilStrings(n.Mentions))
	return t.exec(ctx, "INSERT INTO notifications ("+notificationCols+", seq) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', NULL, ?, ?, ?) ON CONFLICT (dedupe_key) DO NOTHING",
		n.ID, n.SessionID, n.CaseID, n.Kind, n.DedupeKey, n.Content, string(mentions), ts(n.CreatedAt), ts(n.CreatedAt), seq)
}

// ClaimNotification は最も古い pending の通知を sending にして返す。なければ ErrNotFound。
func (t *Tx) ClaimNotification(ctx context.Context, now time.Time) (Notification, error) {
	n, err := scanNotification(t.row(ctx, "SELECT "+notificationCols+" FROM notifications WHERE status = 'pending' ORDER BY seq LIMIT 1"))
	if err != nil {
		return n, err
	}
	if err := t.exec(ctx, "UPDATE notifications SET status = 'sending', updated_at = ? WHERE id = ?", ts(now), n.ID); err != nil {
		return n, err
	}
	n.Status = NotifySending
	return n, nil
}

func (t *Tx) SetNotificationStatus(ctx context.Context, id, status, errorCode string, now time.Time) error {
	return t.exec(ctx, "UPDATE notifications SET status = ?, error_code = ?, updated_at = ? WHERE id = ?", status, nullStr(errorCode), ts(now), id)
}

// MarkInterruptedNotificationsUnknown は送信途中で停止した通知を成否不明にする。再起動時に呼ぶ。
func (t *Tx) MarkInterruptedNotificationsUnknown(ctx context.Context, now time.Time) ([]Notification, error) {
	rows, err := t.query(ctx, "SELECT "+notificationCols+" FROM notifications WHERE status = 'sending'")
	if err != nil {
		return nil, err
	}
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, n := range out {
		if err := t.SetNotificationStatus(ctx, n.ID, NotifyUnknown, "delivery_unknown", now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (t *Tx) NotificationsByCase(ctx context.Context, caseID string) ([]Notification, error) {
	rows, err := t.query(ctx, "SELECT "+notificationCols+" FROM notifications WHERE case_id = ? ORDER BY seq", caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type Activity struct {
	ID         string
	SessionID  string
	CaseID     string
	Kind       string
	Summary    string
	ProposalID string
	OccurredAt time.Time
}

func (t *Tx) AddActivity(ctx context.Context, a Activity) error {
	if a.ID == "" {
		a.ID = NewID("act")
	}
	return t.exec(ctx, "INSERT INTO activity (id, session_id, case_id, kind, summary, proposal_id, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		a.ID, a.SessionID, a.CaseID, a.Kind, a.Summary, nullStr(a.ProposalID), ts(a.OccurredAt))
}

// ActivityBySession は開催回の履歴を新しい順に返す。同時刻は登録順で固定する。
func (t *Tx) ActivityBySession(ctx context.Context, sessionID string, limit int) ([]Activity, error) {
	rows, err := t.query(ctx, "SELECT id, session_id, case_id, kind, summary, proposal_id, occurred_at FROM activity WHERE session_id = ? ORDER BY occurred_at DESC, seq DESC LIMIT ?", sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		var prop sql.NullString
		var occurred string
		if err := rows.Scan(&a.ID, &a.SessionID, &a.CaseID, &a.Kind, &a.Summary, &prop, &occurred); err != nil {
			return nil, err
		}
		a.ProposalID, a.OccurredAt = prop.String, parseTS(occurred)
		out = append(out, a)
	}
	return out, rows.Err()
}

// LLMCall は LLM 呼び出し1回の記録。金額が分からなければ nil。
type LLMCall struct {
	ID              string
	CaseID          string
	LookupID        string
	Model           string
	InputTokens     *int
	OutputTokens    *int
	Currency        string
	EstimatedAmount *string
	BilledAmount    *string
	Succeeded       bool
	CreatedAt       time.Time
}

func (t *Tx) AddLLMCall(ctx context.Context, c LLMCall) error {
	if c.ID == "" {
		c.ID = NewID("llm")
	}
	if c.Currency == "" {
		c.Currency = "unknown"
	}
	return t.exec(ctx, "INSERT INTO llm_calls (id, case_id, lookup_id, model, input_tokens, output_tokens, currency, estimated_amount, billed_amount, succeeded, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, nullStr(c.CaseID), nullStr(c.LookupID), c.Model, c.InputTokens, c.OutputTokens, c.Currency, c.EstimatedAmount, c.BilledAmount, c.Succeeded, ts(c.CreatedAt))
}

// CountLLMCallsByCase は1つの案件で使った LLM 呼び出しの数を返す（自由文の解釈を含む）。
func (t *Tx) CountLLMCallsByCase(ctx context.Context, caseID string) (int, error) {
	var n int
	err := t.row(ctx, "SELECT COUNT(*) FROM llm_calls WHERE case_id = ?", caseID).Scan(&n)
	return n, err
}

func (t *Tx) LLMCallsByCase(ctx context.Context, caseID string) ([]LLMCall, error) {
	rows, err := t.query(ctx, "SELECT id, model, input_tokens, output_tokens, currency, estimated_amount, billed_amount, succeeded, created_at FROM llm_calls WHERE case_id = ? ORDER BY created_at", caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMCall
	for rows.Next() {
		c := LLMCall{CaseID: caseID}
		var in, outTok sql.NullInt64
		var est, billed sql.NullString
		var created string
		if err := rows.Scan(&c.ID, &c.Model, &in, &outTok, &c.Currency, &est, &billed, &c.Succeeded, &created); err != nil {
			return nil, err
		}
		if in.Valid {
			v := int(in.Int64)
			c.InputTokens = &v
		}
		if outTok.Valid {
			v := int(outTok.Int64)
			c.OutputTokens = &v
		}
		if est.Valid {
			c.EstimatedAmount = &est.String
		}
		if billed.Valid {
			c.BilledAmount = &billed.String
		}
		c.CreatedAt = parseTS(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteNotifications はすべての通知待ちを削除する（開発用の初期データ投入で、投入時の依頼を送らないために使う）。
func (t *Tx) DeleteNotifications(ctx context.Context) error {
	return t.exec(ctx, "DELETE FROM notifications")
}
