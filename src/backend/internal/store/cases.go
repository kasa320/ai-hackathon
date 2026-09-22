package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// 案件の状態。
const (
	CaseCollecting      = "collecting"
	CasePlanning        = "planning"
	CaseAwaitingConsent = "awaiting_consent"
	CaseConfirmed       = "confirmed"
	CaseNeedsOwner      = "needs_owner"
)

type Case struct {
	ID                 string
	SessionID          string
	Status             string
	ReasonCode         string
	Summary            string
	NextRetryAt        *time.Time
	RetryCount         int
	LLMCallCount       int
	ToolCallCount      int
	WithdrawnMemberIDs []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Open は未完了の案件か（確定以外）を返す。
func (c Case) Open() bool { return c.Status != CaseConfirmed }

const caseCols = "id, session_id, status, reason_code, summary, next_retry_at, retry_count, llm_call_count, tool_call_count, withdrawn_member_ids, created_at, updated_at"

func scanCase(row interface{ Scan(...any) error }) (Case, error) {
	var c Case
	var reason, next sql.NullString
	var withdrawn, created, updated string
	if err := row.Scan(&c.ID, &c.SessionID, &c.Status, &reason, &c.Summary, &next, &c.RetryCount, &c.LLMCallCount, &c.ToolCallCount, &withdrawn, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	c.ReasonCode = reason.String
	c.NextRetryAt = parseNullTS(next)
	_ = json.Unmarshal([]byte(withdrawn), &c.WithdrawnMemberIDs)
	c.CreatedAt, c.UpdatedAt = parseTS(created), parseTS(updated)
	return c, nil
}

func (t *Tx) CreateCase(ctx context.Context, c Case) error {
	seq, err := t.nextSeq(ctx, "cases")
	if err != nil {
		return err
	}
	withdrawn, _ := json.Marshal(nonNilStrings(c.WithdrawnMemberIDs))
	return t.exec(ctx, "INSERT INTO cases ("+caseCols+", seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, c.SessionID, c.Status, nullStr(c.ReasonCode), c.Summary, nullTS(c.NextRetryAt), c.RetryCount, c.LLMCallCount, c.ToolCallCount,
		string(withdrawn), ts(c.CreatedAt), ts(c.UpdatedAt), seq)
}

func (t *Tx) UpdateCase(ctx context.Context, c Case) error {
	withdrawn, _ := json.Marshal(nonNilStrings(c.WithdrawnMemberIDs))
	return t.exec(ctx, `UPDATE cases SET status = ?, reason_code = ?, summary = ?, next_retry_at = ?, retry_count = ?,
		llm_call_count = ?, tool_call_count = ?, withdrawn_member_ids = ?, updated_at = ? WHERE id = ?`,
		c.Status, nullStr(c.ReasonCode), c.Summary, nullTS(c.NextRetryAt), c.RetryCount, c.LLMCallCount, c.ToolCallCount,
		string(withdrawn), ts(c.UpdatedAt), c.ID)
}

func (t *Tx) Case(ctx context.Context, id string) (Case, error) {
	return scanCase(t.row(ctx, "SELECT "+caseCols+" FROM cases WHERE id = ?", id))
}

// LatestCase は開催回の直近の案件を返す。なければ ErrNotFound。
func (t *Tx) LatestCase(ctx context.Context, sessionID string) (Case, error) {
	return scanCase(t.row(ctx, "SELECT "+caseCols+" FROM cases WHERE session_id = ? ORDER BY seq DESC LIMIT 1", sessionID))
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// 案の状態。
const (
	ProposalPending    = "pending"
	ProposalConfirmed  = "confirmed"
	ProposalSuperseded = "superseded"
	ProposalRejected   = "rejected"
)

type Proposal struct {
	ID           string
	SessionID    string
	CaseID       string
	Version      int
	Status       string
	ChangeKind   string
	Author       string
	Summary      string
	Data         json.RawMessage
	Requirements json.RawMessage
	// Revision は案の作成直後の開催回の revision。確定時に状態が変わっていないかの確認に使う。
	Revision    int64
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

const proposalCols = "id, session_id, case_id, version, status, change_kind, author, summary, data, requirements, revision, created_at, confirmed_at"

func scanProposal(row interface{ Scan(...any) error }) (Proposal, error) {
	var p Proposal
	var data, req, created string
	var confirmed sql.NullString
	if err := row.Scan(&p.ID, &p.SessionID, &p.CaseID, &p.Version, &p.Status, &p.ChangeKind, &p.Author, &p.Summary, &data, &req, &p.Revision, &created, &confirmed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, err
	}
	p.Data, p.Requirements = json.RawMessage(data), json.RawMessage(req)
	p.CreatedAt, p.ConfirmedAt = parseTS(created), parseNullTS(confirmed)
	return p, nil
}

func (t *Tx) CreateProposal(ctx context.Context, p Proposal) error {
	return t.exec(ctx, "INSERT INTO proposals ("+proposalCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.ID, p.SessionID, p.CaseID, p.Version, p.Status, p.ChangeKind, p.Author, p.Summary, string(p.Data), string(p.Requirements),
		p.Revision, ts(p.CreatedAt), nullTS(p.ConfirmedAt))
}

func (t *Tx) Proposal(ctx context.Context, id string) (Proposal, error) {
	return scanProposal(t.row(ctx, "SELECT "+proposalCols+" FROM proposals WHERE id = ?", id))
}

// LatestProposal は開催回で最新の版を返す。なければ ErrNotFound。
func (t *Tx) LatestProposal(ctx context.Context, sessionID string) (Proposal, error) {
	return scanProposal(t.row(ctx, "SELECT "+proposalCols+" FROM proposals WHERE session_id = ? ORDER BY version DESC LIMIT 1", sessionID))
}

// NextProposalVersion は開催回内で単調増加する次の版番号を返す。
func (t *Tx) NextProposalVersion(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := t.row(ctx, "SELECT COALESCE(MAX(version), 0) + 1 FROM proposals WHERE session_id = ?", sessionID).Scan(&n)
	return n, err
}

func (t *Tx) SetProposalStatus(ctx context.Context, id, status string, confirmedAt *time.Time) error {
	return t.exec(ctx, "UPDATE proposals SET status = ?, confirmed_at = COALESCE(?, confirmed_at) WHERE id = ?", status, nullTS(confirmedAt), id)
}

// SetProposalRequirements は保留中の案の承認条件を書き換える。ブック側で先に本人承認済みの担当分を
// 取り除き、重複した引き受け待ちを解消するときに使う。
func (t *Tx) SetProposalRequirements(ctx context.Context, id string, requirements json.RawMessage) error {
	return t.exec(ctx, "UPDATE proposals SET requirements = ? WHERE id = ?", string(requirements), id)
}

// ConfirmedProposals は開催回で確定した案を確定順に返す。
func (t *Tx) ConfirmedProposals(ctx context.Context, sessionID string) ([]Proposal, error) {
	rows, err := t.query(ctx, "SELECT "+proposalCols+" FROM proposals WHERE session_id = ? AND confirmed_at IS NOT NULL ORDER BY confirmed_at, version", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingProposalsInCase は案件内の pending の案を返す。
func (t *Tx) PendingProposals(ctx context.Context, sessionID string) ([]Proposal, error) {
	rows, err := t.query(ctx, "SELECT "+proposalCols+" FROM proposals WHERE session_id = ? AND status = 'pending' ORDER BY version", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountRejectedProposals は案件内で棄却された案の数を返す。
func (t *Tx) CountRejectedProposals(ctx context.Context, caseID string) (int, error) {
	var n int
	err := t.row(ctx, "SELECT COUNT(*) FROM proposals WHERE case_id = ? AND status = 'rejected'", caseID).Scan(&n)
	return n, err
}

func (t *Tx) RejectedProposals(ctx context.Context, caseID string) ([]Proposal, error) {
	rows, err := t.query(ctx, "SELECT "+proposalCols+" FROM proposals WHERE case_id = ? AND status = 'rejected' ORDER BY version", caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// タスクの種類と状態。
const (
	TaskPreparation   = "preparation"
	TaskAssignment    = "assignment"
	TaskApproval      = "approval"
	TaskOwnerApproval = "owner_approval"

	TaskOpen     = "open"
	TaskAnswered = "answered"
	TaskExpired  = "expired"
	TaskObsolete = "obsolete"
)

type Task struct {
	ID              string
	SessionID       string
	CaseID          string
	MemberID        string
	Kind            string
	Status          string
	Title           string
	DueAt           time.Time
	ProposalID      string
	ProposalVersion int
	Decision        string
	// RequestedBy は依頼元。"system"（開催回登録・案の作成）または "agent"（AIの確認依頼）。
	RequestedBy string
	AnsweredAt  *time.Time
	RemindedAt  *time.Time
	CreatedAt   time.Time
}

const taskCols = "id, session_id, case_id, member_id, kind, status, title, due_at, proposal_id, proposal_version, decision, requested_by, answered_at, reminded_at, created_at"

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var tk Task
	var due, created string
	var proposalID, decision, answered, reminded sql.NullString
	var version sql.NullInt64
	if err := row.Scan(&tk.ID, &tk.SessionID, &tk.CaseID, &tk.MemberID, &tk.Kind, &tk.Status, &tk.Title, &due, &proposalID, &version, &decision, &tk.RequestedBy, &answered, &reminded, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tk, ErrNotFound
		}
		return tk, err
	}
	tk.DueAt, tk.CreatedAt = parseTS(due), parseTS(created)
	tk.ProposalID, tk.ProposalVersion, tk.Decision = proposalID.String, int(version.Int64), decision.String
	tk.AnsweredAt, tk.RemindedAt = parseNullTS(answered), parseNullTS(reminded)
	return tk, nil
}

func (t *Tx) CreateTask(ctx context.Context, tk Task) error {
	seq, err := t.nextSeq(ctx, "tasks")
	if err != nil {
		return err
	}
	var version any
	if tk.ProposalID != "" {
		version = tk.ProposalVersion
	}
	return t.exec(ctx, "INSERT INTO tasks ("+taskCols+", seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		tk.ID, tk.SessionID, tk.CaseID, tk.MemberID, tk.Kind, tk.Status, tk.Title, ts(tk.DueAt), nullStr(tk.ProposalID), version,
		nullStr(tk.Decision), tk.RequestedBy, nullTS(tk.AnsweredAt), nullTS(tk.RemindedAt), ts(tk.CreatedAt), seq)
}

func (t *Tx) Task(ctx context.Context, id string) (Task, error) {
	return scanTask(t.row(ctx, "SELECT "+taskCols+" FROM tasks WHERE id = ?", id))
}

func (t *Tx) tasks(ctx context.Context, where string, args ...any) ([]Task, error) {
	rows, err := t.query(ctx, "SELECT "+taskCols+" FROM tasks WHERE "+where+" ORDER BY seq", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		tk, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tk)
	}
	return out, rows.Err()
}

// TasksByCase は案件のタスクを作成順に返す。
func (t *Tx) TasksByCase(ctx context.Context, caseID string) ([]Task, error) {
	return t.tasks(ctx, "case_id = ?", caseID)
}

// OpenTasksByKind は指定した種類の開いているタスクを全件返す（案件・開催回をまたぐ整合性チェック用）。
func (t *Tx) OpenAcceptedBookAssignmentTasks(ctx context.Context) ([]Task, error) {
	return t.tasks(ctx, `kind = 'assignment' AND status = 'open' AND EXISTS (
		SELECT 1 FROM reading_book_slots s
		WHERE s.session_id = tasks.session_id
		  AND s.assignment_status = 'accepted'
		  AND s.assignee_member_id = tasks.member_id
	)`)
}

// TasksByProposal は案に対するタスクを返す。
func (t *Tx) TasksByProposal(ctx context.Context, proposalID string) ([]Task, error) {
	return t.tasks(ctx, "proposal_id = ?", proposalID)
}

// AnswerTask は open のタスクを回答済みにする。既に open でなければ false。
func (t *Tx) AnswerTask(ctx context.Context, id, decision string, now time.Time) (bool, error) {
	n, err := t.execN(ctx, "UPDATE tasks SET status = 'answered', decision = ?, answered_at = ? WHERE id = ? AND status = 'open'", decision, ts(now), id)
	return n == 1, err
}

// CloseOpenTasks は条件に合う open のタスクをまとめて obsolete・expired にする。
func (t *Tx) CloseOpenTasksByCase(ctx context.Context, caseID, status string) error {
	return t.exec(ctx, "UPDATE tasks SET status = ? WHERE case_id = ? AND status = 'open'", status, caseID)
}

func (t *Tx) CloseOpenTasksByProposal(ctx context.Context, proposalID, status string) error {
	return t.exec(ctx, "UPDATE tasks SET status = ? WHERE proposal_id = ? AND status = 'open'", status, proposalID)
}

func (t *Tx) SetTaskStatus(ctx context.Context, id, status string) error {
	return t.exec(ctx, "UPDATE tasks SET status = ? WHERE id = ?", status, id)
}

func (t *Tx) MarkTaskReminded(ctx context.Context, id string, now time.Time) (bool, error) {
	n, err := t.execN(ctx, "UPDATE tasks SET reminded_at = ? WHERE id = ? AND reminded_at IS NULL AND status = 'open'", ts(now), id)
	return n == 1, err
}
