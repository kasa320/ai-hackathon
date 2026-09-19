package coord

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrNotImplemented は雛形の業務処理が未実装であることを示す。
// 呼び出し側は成功や「承認不要」に読み替えず、その処理を停止する。
var ErrNotImplemented = errors.New("playbook operation is not implemented")

var ErrUnknownPlaybook = errors.New("unknown playbook")

// Playbook はアプリ内に静的に登録する用途別プラグイン。
// DB 更新・通知・同意の作成は行わず、判断材料と用途固有の制約を提供する。
// Snapshot と Proposal の Data は用途側の型へデコードして検証する。
type Playbook interface {
	Descriptor() Descriptor
	BuildContext(context.Context, Snapshot) (json.RawMessage, error)
	Instructions() string
	ValidatePlan(context.Context, Snapshot, Proposal) error
	ApprovalRequirements(context.Context, Snapshot, Proposal) (ApprovalRequirements, error)
}
