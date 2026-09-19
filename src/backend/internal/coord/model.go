package coord

import "encoding/json"

// Descriptor は用途の識別子と表示名。ID は保存する案件や画面の登録にも使う。
type Descriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Snapshot はサーバーが組み立てた案件の状態。Data の型は各 Playbook が定義する。
// 認証情報や未加工の私的な原文を Data に含めない。
type Snapshot struct {
	CaseID   string          `json:"case_id"`
	Revision int64           `json:"revision"`
	Data     json.RawMessage `json:"data"`
}

// Proposal は版付きの変更案。Data の内容は用途側で検証する。
type Proposal struct {
	Version int64           `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// ApprovalRequirement は投票・承認の対象者と必要人数。
// 過半数などの必要人数の算出は用途側、本人・版・票数の検証は共通側の責務。
type ApprovalRequirement struct {
	EligibleMemberIDs []string `json:"eligible_member_ids"`
	MinimumApprovals  int      `json:"minimum_approvals"`
}

// ApprovalRequirements は一つの案に必要な条件。すべてを満たす必要がある。
// 引き受けは集団の承認とは別に、指定された本人全員から取得する。
type ApprovalRequirements struct {
	Approvals           []ApprovalRequirement `json:"approvals"`
	RequiredAcceptorIDs []string              `json:"required_acceptor_ids"`
}
