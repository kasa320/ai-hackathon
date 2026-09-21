package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// 開催日時の決まり具合。
const (
	// ScheduleProposed は期間だけが決まっていて、日時はまだ合意していない状態。
	ScheduleProposed = "proposed"
	// ScheduleConfirmed は日時が決まっている状態（人が指定した回も含む）。
	ScheduleConfirmed = "confirmed"
)

type Session struct {
	ID         string
	GroupID    string
	PlaybookID string
	Title      string
	// StartsAt は常に値を持つ。ScheduleStatus が proposed の間は仮の候補で、確定した日時ではない。
	StartsAt time.Time
	// PeriodStart / PeriodEnd は「この期間のどこかで開く」という登録のときの範囲（YYYY-MM-DD）。
	// 日時を直接指定して登録した回では空。
	PeriodStart         string
	PeriodEnd           string
	ScheduleStatus      string
	DurationMinutes     int
	Revision            int64
	Status              string
	Data                json.RawMessage
	ConfirmedProposalID string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const sessionCols = "id, group_id, playbook_id, title, starts_at, period_start, period_end, schedule_status, duration_minutes, revision, status, data, confirmed_proposal_id, created_at, updated_at"

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	return scanSessionWith(row)
}

// scanSessionWith は開催回の列に続けて、結合したクエリの追加の列を読む。
func scanSessionWith(row interface{ Scan(...any) error }, extra ...any) (Session, error) {
	var s Session
	var starts, created, updated, data string
	var confirmed sql.NullString
	dest := append([]any{&s.ID, &s.GroupID, &s.PlaybookID, &s.Title, &starts, &s.PeriodStart, &s.PeriodEnd, &s.ScheduleStatus,
		&s.DurationMinutes, &s.Revision, &s.Status, &data, &confirmed, &created, &updated}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s, ErrNotFound
		}
		return s, err
	}
	s.StartsAt, s.CreatedAt, s.UpdatedAt = parseTS(starts), parseTS(created), parseTS(updated)
	s.Data = json.RawMessage(data)
	s.ConfirmedProposalID = confirmed.String
	return s, nil
}

func (t *Tx) CreateSession(ctx context.Context, s Session, memberIDs []string) error {
	if s.ScheduleStatus == "" {
		s.ScheduleStatus = ScheduleConfirmed
	}
	err := t.exec(ctx, "INSERT INTO sessions ("+sessionCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.ID, s.GroupID, s.PlaybookID, s.Title, ts(s.StartsAt), s.PeriodStart, s.PeriodEnd, s.ScheduleStatus,
		s.DurationMinutes, s.Revision, s.Status, string(s.Data),
		nullStr(s.ConfirmedProposalID), ts(s.CreatedAt), ts(s.UpdatedAt))
	if err != nil {
		return err
	}
	for _, id := range memberIDs {
		if err := t.exec(ctx, "INSERT INTO session_members (session_id, member_id) VALUES (?, ?)", s.ID, id); err != nil {
			return err
		}
	}
	return nil
}

// CountSessions はグループの開催回の数を返す。名前を自動で振るときの通し番号に使う。
func (t *Tx) CountSessions(ctx context.Context, groupID string) (int, error) {
	var n int
	if err := t.row(ctx, "SELECT COUNT(*) FROM sessions WHERE group_id = ?", groupID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (t *Tx) Session(ctx context.Context, id string) (Session, error) {
	return scanSession(t.row(ctx, "SELECT "+sessionCols+" FROM sessions WHERE id = ?", id))
}

// SessionsByGroup は開催回を starts_at の降順で返す。
func (t *Tx) SessionsByGroup(ctx context.Context, groupID string) ([]Session, error) {
	rows, err := t.query(ctx, "SELECT "+sessionCols+" FROM sessions WHERE group_id = ? ORDER BY starts_at DESC, id DESC", groupID)
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

// UpdateSessionState は revision・status・確定計画と、開催日時の決まり具合を更新する。
func (t *Tx) UpdateSessionState(ctx context.Context, s Session) error {
	if s.ScheduleStatus == "" {
		s.ScheduleStatus = ScheduleConfirmed
	}
	return t.exec(ctx, "UPDATE sessions SET revision = ?, status = ?, confirmed_proposal_id = ?, starts_at = ?, schedule_status = ?, updated_at = ? WHERE id = ?",
		s.Revision, s.Status, nullStr(s.ConfirmedProposalID), ts(s.StartsAt), s.ScheduleStatus, ts(s.UpdatedAt), s.ID)
}

// SessionMembers は開催回に固定したメンバーを表示順で返す。
func (t *Tx) SessionMembers(ctx context.Context, sessionID string) ([]Member, error) {
	rows, err := t.query(ctx, `
		SELECT m.id, m.group_id, m.discord_user_id, m.user_id, m.display_name, m.role
		FROM session_members sm JOIN members m ON m.id = sm.member_id
		WHERE sm.session_id = ? ORDER BY m.seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type Preparation struct {
	SessionID  string
	MemberID   string
	Attendance string
	Data       json.RawMessage
	UpdatedAt  time.Time
}

// Preparations は回答済みの参加条件をメンバーIDで引ける形で返す。
func (t *Tx) Preparations(ctx context.Context, sessionID string) (map[string]Preparation, error) {
	rows, err := t.query(ctx, "SELECT session_id, member_id, attendance, data, updated_at FROM preparations WHERE session_id = ?", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Preparation{}
	for rows.Next() {
		var p Preparation
		var data, updated string
		if err := rows.Scan(&p.SessionID, &p.MemberID, &p.Attendance, &data, &updated); err != nil {
			return nil, err
		}
		p.Data, p.UpdatedAt = json.RawMessage(data), parseTS(updated)
		out[p.MemberID] = p
	}
	return out, rows.Err()
}

// PutPreparation は本人の参加条件を全置換する。
func (t *Tx) PutPreparation(ctx context.Context, p Preparation) error {
	return t.exec(ctx, `
		INSERT INTO preparations (session_id, member_id, attendance, data, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (session_id, member_id) DO UPDATE SET attendance = excluded.attendance, data = excluded.data, updated_at = excluded.updated_at`,
		p.SessionID, p.MemberID, p.Attendance, string(p.Data), ts(p.UpdatedAt))
}
