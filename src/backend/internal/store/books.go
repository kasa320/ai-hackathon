package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ブックの計画状態（reading_books.plan_status）。
const (
	// BookPlanLegacy は自動進行の前に作られた旧ブック。計画・担当・自動開始の対象にしない。
	BookPlanLegacy = "legacy"
	// BookPlanPlanning はブック登録後、AIの全体計画を待っている状態。
	BookPlanPlanning = "planning"
	// BookPlanAwaitingApproval は計画案があり、割り当てられた全員の承認を待っている状態。
	BookPlanAwaitingApproval = "awaiting_approval"
	// BookPlanApproved は全担当者が承認し、各回の調整開始日に自動で進行する状態。
	BookPlanApproved = "approved"
	// BookPlanNeedsAttention はAIの失敗などで、管理者の判断が必要な状態。
	BookPlanNeedsAttention = "needs_attention"
)

// 枠の担当の状態（reading_book_slots.assignment_status）。
const (
	AssignUnassigned      = "unassigned"
	AssignPending         = "pending"
	AssignAccepted        = "accepted"
	AssignChangeRequested = "change_requested"
	AssignChangeProposed  = "change_proposed"
	AssignNeedsAttention  = "needs_attention"
)

// 直前確認の状態（reading_slot_confirmations.status）。
const (
	ConfirmOpen            = "open"
	ConfirmConfirmed       = "confirmed"
	ConfirmChangeRequested = "change_requested"
	ConfirmNeedsOwner      = "needs_owner"
	ConfirmSuperseded      = "superseded"
)

type ReadingBook struct {
	ID, GroupID, Title          string
	ISBN                        *string
	TocSource, Sections         json.RawMessage
	PlannedSessionCount         int
	SessionCreationMode, Status string
	CompletedSectionIDs         []string
	CreatedAt, UpdatedAt        time.Time

	PeriodStart, PeriodEnd string
	DurationMinutes        int
	AdjustmentLeadDays     int
	PlanStatus             string
	PlanVersion            int
	PlanSummary            string
	PlanAttempts           int
	PlanNextAt             *time.Time
	PlanReason             string
}

type ReadingBookSlot struct {
	ID, BookID        string
	SequenceNumber    int
	Status            string
	SessionID         *string
	CoveredSectionIDs []string

	PeriodStart, PeriodEnd string
	TargetSectionIDs       []string
	// AssigneeMemberID は現在の担当者（空なら未割り当て）。変更候補が承認されるまで入れ替えない。
	AssigneeMemberID string
	AssignmentStatus string
	// ProposedAssigneeID は担当変更の候補（本人の承認待ち）。
	ProposedAssigneeID string
	ExcludedMemberIDs  []string
	ChangeAttempts     int
	ChangeNextAt       *time.Time
	AttentionReason    string
}

const bookCols = "id, group_id, title, isbn, toc_source, sections, planned_session_count, session_creation_mode, status, completed_section_ids, created_at, updated_at, period_start, period_end, duration_minutes, adjustment_lead_days, plan_status, plan_version, plan_summary, plan_attempts, plan_next_at, plan_reason"

func scanBook(r interface{ Scan(...any) error }) (b ReadingBook, err error) {
	var isbn, planNext sql.NullString
	var toc, secs, done, created, updated string
	err = r.Scan(&b.ID, &b.GroupID, &b.Title, &isbn, &toc, &secs, &b.PlannedSessionCount, &b.SessionCreationMode, &b.Status, &done, &created, &updated,
		&b.PeriodStart, &b.PeriodEnd, &b.DurationMinutes, &b.AdjustmentLeadDays, &b.PlanStatus, &b.PlanVersion, &b.PlanSummary, &b.PlanAttempts, &planNext, &b.PlanReason)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err == nil {
		if isbn.Valid {
			b.ISBN = &isbn.String
		}
		b.TocSource = json.RawMessage(toc)
		b.Sections = json.RawMessage(secs)
		_ = json.Unmarshal([]byte(done), &b.CompletedSectionIDs)
		b.CreatedAt, b.UpdatedAt = parseTS(created), parseTS(updated)
		b.PlanNextAt = parseNullTS(planNext)
	}
	return
}

func (t *Tx) CreateReadingBook(ctx context.Context, b ReadingBook) error {
	done, _ := json.Marshal(nonNilStrings(b.CompletedSectionIDs))
	return t.exec(ctx, "INSERT INTO reading_books ("+bookCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		b.ID, b.GroupID, b.Title, nullStrPtr(b.ISBN), string(b.TocSource), string(b.Sections), b.PlannedSessionCount, b.SessionCreationMode, b.Status, string(done), ts(b.CreatedAt), ts(b.UpdatedAt),
		b.PeriodStart, b.PeriodEnd, b.DurationMinutes, b.AdjustmentLeadDays, b.PlanStatus, b.PlanVersion, b.PlanSummary, b.PlanAttempts, nullTS(b.PlanNextAt), b.PlanReason)
}

func nullStrPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func (t *Tx) ReadingBooks(ctx context.Context, groupID string) ([]ReadingBook, error) {
	rows, e := t.query(ctx, "SELECT "+bookCols+" FROM reading_books WHERE group_id=? ORDER BY created_at DESC, id DESC", groupID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReadingBook
	for rows.Next() {
		b, e := scanBook(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (t *Tx) ReadingBook(ctx context.Context, id string) (ReadingBook, error) {
	return scanBook(t.row(ctx, "SELECT "+bookCols+" FROM reading_books WHERE id=? AND EXISTS (SELECT 1 FROM groups g WHERE g.id=reading_books.group_id AND g.deleted_at IS NULL)", id))
}

// UpdateReadingBook はブックの可変な列をすべて更新する（id・group_id・作成日時は変えない）。
func (t *Tx) UpdateReadingBook(ctx context.Context, b ReadingBook) error {
	done, _ := json.Marshal(nonNilStrings(b.CompletedSectionIDs))
	return t.exec(ctx, `UPDATE reading_books SET title=?, isbn=?, toc_source=?, sections=?, planned_session_count=?, status=?, completed_section_ids=?, updated_at=?,
		period_start=?, period_end=?, duration_minutes=?, adjustment_lead_days=?, plan_status=?, plan_version=?, plan_summary=?, plan_attempts=?, plan_next_at=?, plan_reason=? WHERE id=?`,
		b.Title, nullStrPtr(b.ISBN), string(b.TocSource), string(b.Sections), b.PlannedSessionCount, b.Status, string(done), ts(b.UpdatedAt),
		b.PeriodStart, b.PeriodEnd, b.DurationMinutes, b.AdjustmentLeadDays, b.PlanStatus, b.PlanVersion, b.PlanSummary, b.PlanAttempts, nullTS(b.PlanNextAt), b.PlanReason, b.ID)
}

// PlanningBooks はAIの全体計画を待っているブックを返す。再試行の待ち時間中のものは除く。
func (t *Tx) PlanningBooks(ctx context.Context, now time.Time, limit int) ([]ReadingBook, error) {
	return t.booksWhere(ctx, "plan_status = 'planning' AND (plan_next_at IS NULL OR plan_next_at <= ?)", []any{ts(now)}, limit)
}

// ApprovedBooks は自動進行中（全担当者が承認済み）で未完了のブックを返す。
func (t *Tx) ApprovedBooks(ctx context.Context) ([]ReadingBook, error) {
	return t.booksWhere(ctx, "plan_status = 'approved' AND status = 'in_progress'", nil, 1000)
}

func (t *Tx) booksWhere(ctx context.Context, cond string, args []any, limit int) ([]ReadingBook, error) {
	q := "SELECT " + bookCols + " FROM reading_books WHERE " + cond +
		" AND EXISTS (SELECT 1 FROM groups g WHERE g.id = reading_books.group_id AND g.deleted_at IS NULL) ORDER BY created_at, id LIMIT ?"
	rows, err := t.query(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReadingBook
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const slotCols = "id, book_id, sequence_number, status, session_id, covered_section_ids, period_start, period_end, target_section_ids, assignee_member_id, assignment_status, proposed_assignee_member_id, excluded_member_ids, change_attempts, change_next_at, attention_reason"

func (t *Tx) CreateReadingBookSlot(ctx context.Context, s ReadingBookSlot) error {
	if s.AssignmentStatus == "" {
		s.AssignmentStatus = AssignUnassigned
	}
	covered, _ := json.Marshal(nonNilStrings(s.CoveredSectionIDs))
	target, _ := json.Marshal(nonNilStrings(s.TargetSectionIDs))
	excluded, _ := json.Marshal(nonNilStrings(s.ExcludedMemberIDs))
	return t.exec(ctx, "INSERT INTO reading_book_slots ("+slotCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.ID, s.BookID, s.SequenceNumber, s.Status, nullStrPtr(s.SessionID), string(covered), s.PeriodStart, s.PeriodEnd, string(target),
		nullStr(s.AssigneeMemberID), s.AssignmentStatus, nullStr(s.ProposedAssigneeID), string(excluded), s.ChangeAttempts, nullTS(s.ChangeNextAt), s.AttentionReason)
}

func scanSlot(r interface{ Scan(...any) error }) (s ReadingBookSlot, e error) {
	var sid, assignee, proposed, next sql.NullString
	var covered, target, excluded string
	e = r.Scan(&s.ID, &s.BookID, &s.SequenceNumber, &s.Status, &sid, &covered, &s.PeriodStart, &s.PeriodEnd, &target,
		&assignee, &s.AssignmentStatus, &proposed, &excluded, &s.ChangeAttempts, &next, &s.AttentionReason)
	if errors.Is(e, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if e == nil {
		if sid.Valid {
			s.SessionID = &sid.String
		}
		_ = json.Unmarshal([]byte(covered), &s.CoveredSectionIDs)
		_ = json.Unmarshal([]byte(target), &s.TargetSectionIDs)
		_ = json.Unmarshal([]byte(excluded), &s.ExcludedMemberIDs)
		s.AssigneeMemberID, s.ProposedAssigneeID = assignee.String, proposed.String
		s.ChangeNextAt = parseNullTS(next)
	}
	return
}

func (t *Tx) ReadingBookSlots(ctx context.Context, bookID string) ([]ReadingBookSlot, error) {
	rows, e := t.query(ctx, "SELECT "+slotCols+" FROM reading_book_slots WHERE book_id=? ORDER BY sequence_number", bookID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	return scanSlots(rows)
}

// ReadingBookSlotBySession returns the trusted book slot linked to a real session.
// Session JSON alone is not proof that its assignee was approved in the book flow.
func (t *Tx) ReadingBookSlotBySession(ctx context.Context, sessionID string) (ReadingBookSlot, error) {
	return scanSlot(t.row(ctx, "SELECT "+slotCols+" FROM reading_book_slots WHERE session_id = ?", sessionID))
}

func scanSlots(rows *sql.Rows) ([]ReadingBookSlot, error) {
	var out []ReadingBookSlot
	for rows.Next() {
		s, e := scanSlot(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (t *Tx) ReadingBookSlot(ctx context.Context, id string) (ReadingBookSlot, error) {
	return scanSlot(t.row(ctx, "SELECT "+slotCols+" FROM reading_book_slots WHERE id=?", id))
}

// UpdateReadingBookSlot は計画・担当に関する可変な列を更新する。開始・完了の状態遷移は
// StartReadingBookSlot / CompleteReadingBookSlot だけが行い、ここでは status・session_id を変えない。
func (t *Tx) UpdateReadingBookSlot(ctx context.Context, s ReadingBookSlot) error {
	target, _ := json.Marshal(nonNilStrings(s.TargetSectionIDs))
	excluded, _ := json.Marshal(nonNilStrings(s.ExcludedMemberIDs))
	return t.exec(ctx, `UPDATE reading_book_slots SET period_start=?, period_end=?, target_section_ids=?, assignee_member_id=?, assignment_status=?,
		proposed_assignee_member_id=?, excluded_member_ids=?, change_attempts=?, change_next_at=?, attention_reason=? WHERE id=?`,
		s.PeriodStart, s.PeriodEnd, string(target), nullStr(s.AssigneeMemberID), s.AssignmentStatus,
		nullStr(s.ProposedAssigneeID), string(excluded), s.ChangeAttempts, nullTS(s.ChangeNextAt), s.AttentionReason, s.ID)
}

// DeleteReadingBookSlots はブックの枠をすべて削除する。実セッションを持つ枠がある場合は何もせず false を返す。
func (t *Tx) DeleteReadingBookSlots(ctx context.Context, bookID string) (bool, error) {
	var n int
	if err := t.row(ctx, "SELECT COUNT(*) FROM reading_book_slots WHERE book_id = ? AND (session_id IS NOT NULL OR status <> 'planned')", bookID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	return true, t.exec(ctx, "DELETE FROM reading_book_slots WHERE book_id = ?", bookID)
}

func (t *Tx) StartReadingBookSlot(ctx context.Context, id, sessionID string) (bool, error) {
	n, err := t.execN(ctx, "UPDATE reading_book_slots SET status='active', session_id=? WHERE id=? AND status='planned'", sessionID, id)
	return n == 1, err
}

func (t *Tx) CompleteReadingBookSlot(ctx context.Context, id string, covered []string) (bool, error) {
	v, _ := json.Marshal(nonNilStrings(covered))
	n, err := t.execN(ctx, "UPDATE reading_book_slots SET status='completed', covered_section_ids=? WHERE id=? AND status='active'", string(v), id)
	return n == 1, err
}

// GroupSlot はグループ内の全ブックの枠を、負担の集計のためにブックの情報と並べたもの。
type GroupSlot struct {
	ReadingBookSlot
	BookTitle      string
	BookStatus     string
	BookPlanStatus string
}

// GroupSlots はグループ内のブックの枠を返す。plan_status が legacy のブックの枠も含む。
func (t *Tx) GroupSlots(ctx context.Context, groupID string) ([]GroupSlot, error) {
	rows, err := t.query(ctx, `SELECT s.id, s.book_id, s.sequence_number, s.status, s.session_id, s.covered_section_ids, s.period_start, s.period_end, s.target_section_ids,
		s.assignee_member_id, s.assignment_status, s.proposed_assignee_member_id, s.excluded_member_ids, s.change_attempts, s.change_next_at, s.attention_reason,
		b.title, b.status, b.plan_status
		FROM reading_book_slots s JOIN reading_books b ON b.id = s.book_id
		WHERE b.group_id = ? ORDER BY b.created_at, b.id, s.sequence_number`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupSlot
	for rows.Next() {
		var g GroupSlot
		var sid, assignee, proposed, next sql.NullString
		var covered, target, excluded string
		if err := rows.Scan(&g.ID, &g.BookID, &g.SequenceNumber, &g.Status, &sid, &covered, &g.PeriodStart, &g.PeriodEnd, &target,
			&assignee, &g.AssignmentStatus, &proposed, &excluded, &g.ChangeAttempts, &next, &g.AttentionReason,
			&g.BookTitle, &g.BookStatus, &g.BookPlanStatus); err != nil {
			return nil, err
		}
		if sid.Valid {
			g.SessionID = &sid.String
		}
		_ = json.Unmarshal([]byte(covered), &g.CoveredSectionIDs)
		_ = json.Unmarshal([]byte(target), &g.TargetSectionIDs)
		_ = json.Unmarshal([]byte(excluded), &g.ExcludedMemberIDs)
		g.AssigneeMemberID, g.ProposedAssigneeID = assignee.String, proposed.String
		g.ChangeNextAt = parseNullTS(next)
		out = append(out, g)
	}
	return out, rows.Err()
}

// SlotsToReplace は担当の変更候補を待っている枠（実行できる状態のブックのもの）を返す。
func (t *Tx) SlotsToReplace(ctx context.Context, now time.Time, limit int) ([]ReadingBookSlot, error) {
	rows, err := t.query(ctx, `SELECT `+prefixed("s", slotCols)+` FROM reading_book_slots s JOIN reading_books b ON b.id = s.book_id
		WHERE s.assignment_status = 'change_requested' AND s.status <> 'completed'
		AND (s.change_next_at IS NULL OR s.change_next_at <= ?)
		AND b.plan_status IN ('awaiting_approval', 'approved')
		AND EXISTS (SELECT 1 FROM groups g WHERE g.id = b.group_id AND g.deleted_at IS NULL)
		ORDER BY b.created_at, s.sequence_number LIMIT ?`, ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSlots(rows)
}

// SlotsWithDepartedMember は、担当者または変更候補が脱退した未完了の枠を返す。
func (t *Tx) SlotsWithDepartedMember(ctx context.Context) ([]ReadingBookSlot, error) {
	rows, err := t.query(ctx, `SELECT `+prefixed("s", slotCols)+` FROM reading_book_slots s JOIN reading_books b ON b.id = s.book_id
		WHERE s.status <> 'completed' AND b.plan_status IN ('awaiting_approval', 'approved')
		AND EXISTS (SELECT 1 FROM groups g WHERE g.id = b.group_id AND g.deleted_at IS NULL)
		AND ((s.assignee_member_id IS NOT NULL AND EXISTS (SELECT 1 FROM members m WHERE m.id = s.assignee_member_id AND m.left_at IS NOT NULL))
		  OR (s.proposed_assignee_member_id IS NOT NULL AND EXISTS (SELECT 1 FROM members m WHERE m.id = s.proposed_assignee_member_id AND m.left_at IS NOT NULL)))
		ORDER BY b.created_at, s.sequence_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSlots(rows)
}

func prefixed(alias, cols string) string {
	out := ""
	start := 0
	for i := 0; i <= len(cols); i++ {
		if i == len(cols) || cols[i] == ',' {
			col := cols[start:i]
			for len(col) > 0 && col[0] == ' ' {
				col = col[1:]
			}
			if out != "" {
				out += ", "
			}
			out += alias + "." + col
			start = i + 1
		}
	}
	return out
}

// SlotConfirmation は開催3日前の担当者確認。(枠, 担当者) ごとに1行。
type SlotConfirmation struct {
	ID          string
	SlotID      string
	MemberID    string
	Status      string
	RequestedAt time.Time
	RemindedAt  *time.Time
	EscalatedAt *time.Time
	AnsweredAt  *time.Time
}

const confirmationCols = "id, slot_id, member_id, status, requested_at, reminded_at, escalated_at, answered_at"

func scanConfirmation(r interface{ Scan(...any) error }) (SlotConfirmation, error) {
	var c SlotConfirmation
	var requested string
	var reminded, escalated, answered sql.NullString
	if err := r.Scan(&c.ID, &c.SlotID, &c.MemberID, &c.Status, &requested, &reminded, &escalated, &answered); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	c.RequestedAt = parseTS(requested)
	c.RemindedAt, c.EscalatedAt, c.AnsweredAt = parseNullTS(reminded), parseNullTS(escalated), parseNullTS(answered)
	return c, nil
}

// CreateSlotConfirmation は確認を登録する。同じ (枠, 担当者) が既にあれば何もせず false を返す。
func (t *Tx) CreateSlotConfirmation(ctx context.Context, c SlotConfirmation) (bool, error) {
	n, err := t.execN(ctx, "INSERT INTO reading_slot_confirmations ("+confirmationCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (slot_id, member_id) DO NOTHING",
		c.ID, c.SlotID, c.MemberID, c.Status, ts(c.RequestedAt), nullTS(c.RemindedAt), nullTS(c.EscalatedAt), nullTS(c.AnsweredAt))
	return n == 1, err
}

func (t *Tx) SlotConfirmation(ctx context.Context, slotID, memberID string) (SlotConfirmation, error) {
	return scanConfirmation(t.row(ctx, "SELECT "+confirmationCols+" FROM reading_slot_confirmations WHERE slot_id = ? AND member_id = ?", slotID, memberID))
}

func (t *Tx) SlotConfirmations(ctx context.Context, slotID string) ([]SlotConfirmation, error) {
	rows, err := t.query(ctx, "SELECT "+confirmationCols+" FROM reading_slot_confirmations WHERE slot_id = ? ORDER BY requested_at, id", slotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlotConfirmation
	for rows.Next() {
		c, err := scanConfirmation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (t *Tx) UpdateSlotConfirmation(ctx context.Context, c SlotConfirmation) error {
	return t.exec(ctx, "UPDATE reading_slot_confirmations SET status = ?, reminded_at = ?, escalated_at = ?, answered_at = ? WHERE id = ?",
		c.Status, nullTS(c.RemindedAt), nullTS(c.EscalatedAt), nullTS(c.AnsweredAt), c.ID)
}

// BookLog はブックの計画・承認・確認の履歴の1行。
type BookLog struct {
	ID         string
	BookID     string
	SlotID     string
	MemberID   string
	Kind       string
	Summary    string
	OccurredAt time.Time
}

func (t *Tx) AddBookLog(ctx context.Context, l BookLog) error {
	if l.ID == "" {
		l.ID = NewID("blog")
	}
	return t.exec(ctx, "INSERT INTO reading_book_log (id, book_id, slot_id, member_id, kind, summary, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		l.ID, l.BookID, nullStr(l.SlotID), nullStr(l.MemberID), l.Kind, l.Summary, ts(l.OccurredAt))
}

func (t *Tx) BookLogs(ctx context.Context, bookID string) ([]BookLog, error) {
	rows, err := t.query(ctx, "SELECT id, book_id, slot_id, member_id, kind, summary, occurred_at FROM reading_book_log WHERE book_id = ? ORDER BY seq", bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookLog
	for rows.Next() {
		var l BookLog
		var slot, member sql.NullString
		var at string
		if err := rows.Scan(&l.ID, &l.BookID, &slot, &member, &l.Kind, &l.Summary, &at); err != nil {
			return nil, err
		}
		l.SlotID, l.MemberID, l.OccurredAt = slot.String, member.String, parseTS(at)
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateSessionData は開催回のデータ（教材・範囲・担当のスナップショット）を置き換える。
func (t *Tx) UpdateSessionData(ctx context.Context, id string, data json.RawMessage, now time.Time) error {
	return t.exec(ctx, "UPDATE sessions SET data = ?, updated_at = ? WHERE id = ?", string(data), ts(now), id)
}

// BusyInterval は開催候補と重なってはいけない、確定済みの予定。
type BusyInterval struct {
	StartsAt        time.Time
	DurationMinutes int
}

// ConfirmedBusyForSession は開催回の在籍メンバーが参加する、別の確定済みの開催回を返す。
// 別のグループの予定も含む（同じ人が同時に2つの会へ出られないため）。今日より前に終わったものは含めない。
func (t *Tx) ConfirmedBusyForSession(ctx context.Context, sessionID string, since time.Time) ([]BusyInterval, error) {
	rows, err := t.query(ctx, `SELECT DISTINCT s.id, s.starts_at, s.duration_minutes FROM sessions s
		JOIN groups g ON g.id = s.group_id AND g.deleted_at IS NULL
		JOIN session_members sm ON sm.session_id = s.id
		JOIN members m ON m.id = sm.member_id AND m.left_at IS NULL AND m.user_id IS NOT NULL
		WHERE s.schedule_status = 'confirmed' AND s.id <> ? AND s.starts_at >= ?
		AND m.user_id IN (SELECT m2.user_id FROM session_members sm2 JOIN members m2 ON m2.id = sm2.member_id
			WHERE sm2.session_id = ? AND m2.left_at IS NULL AND m2.user_id IS NOT NULL)
		ORDER BY s.starts_at, s.id`, sessionID, ts(since), sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BusyInterval
	for rows.Next() {
		var id, starts string
		var b BusyInterval
		if err := rows.Scan(&id, &starts, &b.DurationMinutes); err != nil {
			return nil, err
		}
		b.StartsAt = parseTS(starts)
		out = append(out, b)
	}
	return out, rows.Err()
}
