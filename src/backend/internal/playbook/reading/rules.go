package reading

import (
	"context"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

func (Playbook) ValidatePlan(context.Context, coord.Snapshot, coord.Proposal) error {
	// TODO: 登録された節、持ち時間、担当可能範囲、連続代役などを検証する。
	return coord.ErrNotImplemented
}

func (Playbook) ApprovalRequirements(context.Context, coord.Snapshot, coord.Proposal) (coord.ApprovalRequirements, error) {
	// TODO: 範囲変更への過半数の承認と、担当者全員の引き受け条件を返す。
	// 空の条件を成功として返すと承認を省略するため、未実装中は必ずエラーにする。
	return coord.ApprovalRequirements{}, coord.ErrNotImplemented
}
