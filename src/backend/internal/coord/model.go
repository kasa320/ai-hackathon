package coord

import (
	"encoding/json"
	"time"
)

// Descriptor は用途の識別子と表示名。ID は保存する案件や画面の登録にも使う。
type Descriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// 開催日時の決まり具合（Snapshot.ScheduleStatus）。
const (
	// ScheduleProposed は期間だけが決まっていて、日時は案で決める状態。
	ScheduleProposed = "proposed"
	// ScheduleConfirmed は日時が決まっている状態。
	ScheduleConfirmed = "confirmed"
)

// 参加予定の値。用途によらず共通。
const (
	AttendanceAttending = "attending"
	AttendanceAbsent    = "absent"
)

// 辞退の範囲。
const (
	WithdrawAssignment = "assignment"
	WithdrawAttendance = "attendance"
)

// 案の種類。
const (
	ChangeInitial = "initial"
	ChangeReplan  = "replan"
)

// SnapshotMember は開催回に固定されたメンバー。
type SnapshotMember struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// Preparation は本人が共有を確認した参加条件。Data の型は各 Playbook が定義する。
type Preparation struct {
	Attendance string          `json:"attendance"`
	Data       json.RawMessage `json:"data"`
}

// MemberPreparation はメンバーごとの参加条件。未回答は Value=nil。
type MemberPreparation struct {
	MemberID string       `json:"member_id"`
	Value    *Preparation `json:"value"`
}

// PlanRecord は確定済みの計画。ConfirmedPlans は確定した順に並ぶ。
type PlanRecord struct {
	ProposalID string          `json:"proposal_id"`
	Version    int             `json:"version"`
	ChangeKind string          `json:"change_kind"`
	Data       json.RawMessage `json:"data"`
}

// PastSession は同じグループの過去の開催回（確定計画があるものだけ）。
type PastSession struct {
	SessionID      string       `json:"session_id"`
	StartsAt       time.Time    `json:"starts_at"`
	ConfirmedPlans []PlanRecord `json:"confirmed_plans"`
}

// CaseContext は現在の案件内での経過。同じ依頼を繰り返さないために使う。
type CaseContext struct {
	// DeclinedMemberIDs はこの案件で担当の引き受けを断った人。
	DeclinedMemberIDs []string `json:"declined_member_ids"`
	// WithdrawnMemberIDs はこの案件のきっかけとなった辞退をした人。確認依頼の対象にしない。
	WithdrawnMemberIDs []string `json:"withdrawn_member_ids"`
	// AskedMemberIDs はこの案件でAIが参加条件の確認を依頼した人。
	AskedMemberIDs []string `json:"asked_member_ids"`
	// RejectedProposals はこの案件で棄却された案の数。
	RejectedProposals int `json:"rejected_proposals"`
}

// Snapshot はサーバーが組み立てた開催回の状態。用途固有の Data は各 Playbook が解釈する。
// 認証情報や私的な原文を含めない。
type Snapshot struct {
	SessionID string `json:"session_id"`
	CaseID    string `json:"case_id"`
	// Now は判断時刻（UTC）。用途側が「この日時はもう過ぎている」を判断するために使う。
	Now      time.Time `json:"now"`
	Revision int64     `json:"revision"`
	StartsAt time.Time `json:"starts_at"`
	// ScheduleStatus が proposed の間、StartsAt は仮の候補で、案が日時を決める。
	// PeriodStart / PeriodEnd（YYYY-MM-DD）はその日時を選べる範囲。
	ScheduleStatus  string              `json:"schedule_status"`
	PeriodStart     string              `json:"period_start"`
	PeriodEnd       string              `json:"period_end"`
	DurationMinutes int                 `json:"duration_minutes"`
	OwnerMemberID   string              `json:"owner_member_id"`
	Members         []SnapshotMember    `json:"members"`
	SessionData     json.RawMessage     `json:"session_data"`
	Preparations    []MemberPreparation `json:"preparations"`
	ConfirmedPlans  []PlanRecord        `json:"confirmed_plans"`
	History         []PastSession       `json:"history"`
	Case            CaseContext         `json:"case"`
}

// Preparation は指定メンバーの参加条件を返す。未回答なら nil。
func (s Snapshot) Preparation(memberID string) *Preparation {
	for _, p := range s.Preparations {
		if p.MemberID == memberID {
			return p.Value
		}
	}
	return nil
}

// Attending は参加予定（attendance=attending）のメンバーIDをメンバー順で返す。
func (s Snapshot) Attending() []string {
	var ids []string
	for _, m := range s.Members {
		if p := s.Preparation(m.ID); p != nil && p.Attendance == AttendanceAttending {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// CurrentPlan は現在の確定計画を返す。なければ nil。
func (s Snapshot) CurrentPlan() *PlanRecord {
	if len(s.ConfirmedPlans) == 0 {
		return nil
	}
	p := s.ConfirmedPlans[len(s.ConfirmedPlans)-1]
	return &p
}

// Proposal は版付きの変更案。Data の内容は用途側で検証する。
type Proposal struct {
	Version    int             `json:"version"`
	ChangeKind string          `json:"change_kind"`
	Data       json.RawMessage `json:"data"`
}

// ApprovalKind は承認の種類。
type ApprovalKind string

const (
	// ApprovalMajority は対象者の過半数（floor(n/2)+1）の賛成。
	ApprovalMajority ApprovalKind = "majority"
	// ApprovalOwner は管理者の承認。対象者は共通側が管理者に固定する。
	ApprovalOwner ApprovalKind = "owner"
)

// ApprovalRequirement は投票・承認の種類と対象者。
// 必要人数は共通側が種類から算出し、対象者の所属・参加予定も共通側で確認する。
type ApprovalRequirement struct {
	Kind              ApprovalKind `json:"kind"`
	EligibleMemberIDs []string     `json:"eligible_member_ids"`
}

// ApprovalRequirements は一つの案に必要な条件。すべてを満たす必要がある。
// 引き受けは集団の承認とは別に、指定された本人全員から取得する。
type ApprovalRequirements struct {
	Approvals           []ApprovalRequirement `json:"approvals"`
	RequiredAcceptorIDs []string              `json:"required_acceptor_ids"`
}

// DraftKind は仮の判断処理（LLMを使わない計画）の結果の種類。
type DraftKind string

const (
	DraftProposal   DraftKind = "proposal"
	DraftAsk        DraftKind = "ask"
	DraftNoFeasible DraftKind = "no_feasible_plan"
)

// Draft は用途側の規則だけで作った次の一手。
type Draft struct {
	Kind         DraftKind       `json:"kind"`
	Summary      string          `json:"summary"`
	Plan         json.RawMessage `json:"plan,omitempty"`
	AskMemberIDs []string        `json:"ask_member_ids,omitempty"`
}
