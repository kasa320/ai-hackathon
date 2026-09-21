// Package apitypes は公開 API（docs/data-structure.md）の入出力の型。
// 内部の Go 型や DB テーブルをそのまま公開せず、ここで定義した形へ変換して返す。
// 用途固有の中身は必ず Data（json.RawMessage）に包む。
package apitypes

import (
	"encoding/json"
	"time"
)

type Member struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Joined      bool   `json:"joined"`
}

type Group struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	PlaybookID      string   `json:"playbook_id"`
	CurrentMemberID string   `json:"current_member_id"`
	Members         []Member `json:"members"`
}

type GroupListItem struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	PlaybookID      string `json:"playbook_id"`
	CurrentMemberID string `json:"current_member_id"`
	Role            string `json:"role"`
	MemberCount     int    `json:"member_count"`
}

type GroupList struct {
	Items []GroupListItem `json:"items"`
}

type Preparation struct {
	Attendance string          `json:"attendance"`
	Data       json.RawMessage `json:"data"`
}

type SessionSummary struct {
	ID         string    `json:"id"`
	GroupID    string    `json:"group_id"`
	PlaybookID string    `json:"playbook_id"`
	Title      string    `json:"title"`
	StartsAt   time.Time `json:"starts_at"`
	// ScheduleStatus が proposed の間、StartsAt は仮の候補。confirmed で決まった日時になる。
	ScheduleStatus  string    `json:"schedule_status"`
	PeriodStart     string    `json:"period_start"`
	PeriodEnd       string    `json:"period_end"`
	DurationMinutes int       `json:"duration_minutes"`
	Revision        int64     `json:"revision"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type SessionList struct {
	Items []SessionSummary `json:"items"`
}

type ReadingBook struct {
	ID                    string          `json:"id"`
	GroupID               string          `json:"group_id"`
	Title                 string          `json:"title"`
	ISBN                  *string         `json:"isbn"`
	TocSource             json.RawMessage `json:"toc_source,omitempty"`
	Sections              json.RawMessage `json:"sections,omitempty"`
	PlannedSessionCount   int             `json:"planned_session_count"`
	SessionCreationMode   string          `json:"session_creation_mode"`
	Status                string          `json:"status"`
	CompletedSectionIDs   []string        `json:"completed_section_ids"`
	CompletedSessionCount int             `json:"completed_session_count"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}
type ReadingBookList struct {
	Items []ReadingBook `json:"items"`
}
type ReadingBookSlot struct {
	SlotID            string          `json:"slot_id"`
	SequenceNumber    int             `json:"sequence_number"`
	Status            string          `json:"status"`
	CoveredSectionIDs []string        `json:"covered_section_ids"`
	Session           *SessionSummary `json:"session"`
}
type ReadingBookDetail struct {
	Book        ReadingBook       `json:"book"`
	Sessions    []ReadingBookSlot `json:"sessions"`
	Permissions struct {
		CanManage bool `json:"can_manage"`
	} `json:"permissions"`
}
type CreateReadingBookInput struct {
	Title               string                  `json:"title"`
	ISBN                *string                 `json:"isbn"`
	TocSource           json.RawMessage         `json:"toc_source"`
	Sections            json.RawMessage         `json:"sections"`
	PlannedSessionCount int                     `json:"planned_session_count"`
	SessionCreationMode string                  `json:"session_creation_mode"`
	InitialSession      ReadingBookSessionInput `json:"initial_session"`
}
type ReadingBookSessionInput struct {
	SlotID           string   `json:"slot_id,omitempty"`
	PeriodStart      string   `json:"period_start"`
	PeriodEnd        string   `json:"period_end"`
	DurationMinutes  int      `json:"duration_minutes"`
	TargetSectionIDs []string `json:"target_section_ids"`
}
type CreateReadingBookResult struct {
	Book           ReadingBook    `json:"book"`
	InitialSession SessionSummary `json:"initial_session"`
}
type StartReadingBookSessionResult struct {
	SlotID  string         `json:"slot_id"`
	Session SessionSummary `json:"session"`
}
type CompleteReadingBookSessionInput struct {
	ExpectedRevision *int64 `json:"expected_revision"`
}

type SessionCreated struct {
	Session SessionSummary `json:"session"`
	CaseID  string         `json:"case_id"`
}

type ApprovalProgress struct {
	Kind              string   `json:"kind"`
	EligibleMemberIDs []string `json:"eligible_member_ids"`
	RequiredCount     int      `json:"required_count"`
	ApprovedMemberIDs []string `json:"approved_member_ids"`
	RejectedMemberIDs []string `json:"rejected_member_ids"`
}

type AssignmentStatus struct {
	MemberID string `json:"member_id"`
	Status   string `json:"status"`
}

type Proposal struct {
	ID          string             `json:"id"`
	Version     int                `json:"version"`
	Status      string             `json:"status"`
	ChangeKind  string             `json:"change_kind"`
	Author      string             `json:"author"`
	Summary     string             `json:"summary"`
	Data        json.RawMessage    `json:"data"`
	Approvals   []ApprovalProgress `json:"approvals"`
	Assignments []AssignmentStatus `json:"assignments"`
}

type CaseSummary struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	ReasonCode  *string    `json:"reason_code"`
	Summary     string     `json:"summary"`
	NextRetryAt *time.Time `json:"next_retry_at"`
}

type Task struct {
	ID               string    `json:"id"`
	SessionID        string    `json:"session_id"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	Title            string    `json:"title"`
	DueAt            time.Time `json:"due_at"`
	ProposalID       *string   `json:"proposal_id"`
	ProposalVersion  *int      `json:"proposal_version"`
	AllowedDecisions []string  `json:"allowed_decisions"`
}

type MutationAccepted struct {
	SessionID        string `json:"session_id"`
	Revision         int64  `json:"revision"`
	CaseID           string `json:"case_id"`
	ProcessingStatus string `json:"processing_status"`
}

type MemberPreparation struct {
	MemberID string       `json:"member_id"`
	Value    *Preparation `json:"value"`
}

type Permissions struct {
	CanUpdatePreparation  bool `json:"can_update_preparation"`
	CanWithdrawAssignment bool `json:"can_withdraw_assignment"`
	CanWithdrawAttendance bool `json:"can_withdraw_attendance"`
	CanSubmitProposal     bool `json:"can_submit_proposal"`
	CanDeleteSession      bool `json:"can_delete_session"`
	CanViewActivity       bool `json:"can_view_activity"`
}

type NotificationSummary struct {
	PendingCount int `json:"pending_count"`
	SentCount    int `json:"sent_count"`
	FailedCount  int `json:"failed_count"`
	UnknownCount int `json:"unknown_count"`
}

type SessionDetail struct {
	Session             SessionSummary      `json:"session"`
	Data                json.RawMessage     `json:"data"`
	Members             []Member            `json:"members"`
	Preparations        []MemberPreparation `json:"preparations"`
	ConfirmedPlan       *Proposal           `json:"confirmed_plan"`
	CurrentProposal     *Proposal           `json:"current_proposal"`
	ActiveCase          *CaseSummary        `json:"active_case"`
	MyTasks             []Task              `json:"my_tasks"`
	Permissions         Permissions         `json:"permissions"`
	CurrentMemberID     string              `json:"current_member_id"`
	NotificationSummary NotificationSummary `json:"notification_summary"`
	ServerNow           time.Time           `json:"server_now"`
}

type ActivityItem struct {
	ID         string    `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	CaseID     string    `json:"case_id"`
	Kind       string    `json:"kind"`
	Summary    string    `json:"summary"`
	ProposalID *string   `json:"proposal_id"`
}

type Cost struct {
	Currency        string  `json:"currency"`
	EstimatedAmount *string `json:"estimated_amount"`
	BilledAmount    *string `json:"billed_amount"`
}

type NotificationStatus struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	ErrorCode *string   `json:"error_code"`
}

type ActivitySummary struct {
	CaseID        *string              `json:"case_id"`
	LLMCallCount  int                  `json:"llm_call_count"`
	ToolCallCount int                  `json:"tool_call_count"`
	Costs         []Cost               `json:"costs"`
	Notifications []NotificationStatus `json:"notifications"`
}

type ActivityResponse struct {
	Items   []ActivityItem  `json:"items"`
	Summary ActivitySummary `json:"summary"`
}

// 入力の型。未知のフィールドは API 層で拒否する。

type Invitee struct {
	DiscordUserID string `json:"discord_user_id"`
	DisplayName   string `json:"display_name"`
}

type CreateGroupInput struct {
	Name string `json:"name"`
	// PlaybookID はグループの種別。省略すると reading。作成後は変更できない。
	PlaybookID string    `json:"playbook_id"`
	Invitees   []Invitee `json:"invitees"`
}

// UpdateGroupInput はグループ名の変更。種別（playbook_id）は受け付けない。
type UpdateGroupInput struct {
	Name string `json:"name"`
}

type LeaveGroupInput struct{}

// DeleteGroupInput は削除確認ダイアログから送る空の本文。
// 誤操作防止は、管理者限定と警告付き確認ダイアログで行う。
type DeleteGroupInput struct{}

type GroupLifecycleResult struct {
	GroupID              string `json:"group_id"`
	Status               string `json:"status"`
	AffectedSessionCount int    `json:"affected_session_count"`
	NotificationCount    int    `json:"notification_count"`
}

type CreateSessionInput struct {
	PlaybookID string `json:"playbook_id"`
	// Title は空なら「第N回」を自動で付ける。
	Title string `json:"title"`
	// StartsAt は日時を人が決める場合だけ入れる（RFC 3339）。
	// 空のときは PeriodStart / PeriodEnd が必須で、日時はエージェントが提案して合意で決める。
	StartsAt string `json:"starts_at"`
	// PeriodStart / PeriodEnd は「この期間のどこかで開きたい」範囲（YYYY-MM-DD）。
	PeriodStart     string          `json:"period_start"`
	PeriodEnd       string          `json:"period_end"`
	DurationMinutes int             `json:"duration_minutes"`
	Data            json.RawMessage `json:"data"`
}

type PutPreparationInput struct {
	ExpectedRevision *int64       `json:"expected_revision"`
	Preparation      *Preparation `json:"preparation"`
}

// InterpretPreparationInput は自由文からの参加条件の解釈依頼。対象者は常に呼び出した本人で、
// メンバーIDを入力で指定しない。原文は保存されない。
type InterpretPreparationInput struct {
	Text string `json:"text"`
}

// PreparationInterpretation は自由文の解釈結果の下書き。保存はされない（saved は常に false）。
// 本人が確認・修正したうえで PUT /api/sessions/{id}/preparations/me を送ると保存される。
type PreparationInterpretation struct {
	Preparation Preparation `json:"preparation"`
	// Unclear は発言から読み取れなかった項目名。値は推測せず、保守的な既定値が入る。
	Unclear []string `json:"unclear"`
	// NeedsFollowup は本人に確認すべき項目が残っていること。
	NeedsFollowup bool `json:"needs_followup"`
	// Saved は常に false。解釈だけでは何も保存されないことを示す。
	Saved bool `json:"saved"`
}

// DeleteSessionInput は開催回の削除。本文は空のオブジェクト。
type DeleteSessionInput struct{}

// SessionDeleted は削除した開催回と、戻り先のグループ。
type SessionDeleted struct {
	SessionID string `json:"session_id"`
	GroupID   string `json:"group_id"`
}

type WithdrawalInput struct {
	ExpectedRevision *int64 `json:"expected_revision"`
	Scope            string `json:"scope"`
}

// TaskResponseInput はタスクの種類ごとの3つの形をまとめて受ける。どの項目があるかで形を判定する。
type TaskResponseInput struct {
	Decision         string       `json:"decision"`
	ExpectedRevision *int64       `json:"expected_revision,omitempty"`
	Preparation      *Preparation `json:"preparation,omitempty"`
	ProposalID       *string      `json:"proposal_id,omitempty"`
	ProposalVersion  *int         `json:"proposal_version,omitempty"`
}

type SubmitProposalInput struct {
	ExpectedRevision *int64          `json:"expected_revision"`
	Data             json.RawMessage `json:"data"`
}
