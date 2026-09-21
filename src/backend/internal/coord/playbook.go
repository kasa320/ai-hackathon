package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotImplemented は業務処理が未実装であることを示す。
// 呼び出し側は成功や「承認不要」に読み替えず、その処理を停止する。
var ErrNotImplemented = errors.New("playbook operation is not implemented")

var ErrUnknownPlaybook = errors.New("unknown playbook")

// FieldError は入力の1項目に対する検証エラー。Path は検証対象の JSON 内のパス。
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError は用途固有データの検証エラー。
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Path+": "+f.Message)
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Add は検証エラーを追加する。
func (e *ValidationError) Add(path, format string, args ...any) {
	e.Fields = append(e.Fields, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Err はエラーがあれば自身を、なければ nil を返す。
func (e *ValidationError) Err() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// Prefixed は各パスに接頭辞を付けたコピーを返す。API層で本文内のパスへ変換するのに使う。
func (e *ValidationError) Prefixed(prefix string) *ValidationError {
	out := &ValidationError{Fields: make([]FieldError, 0, len(e.Fields))}
	for _, f := range e.Fields {
		p := prefix
		if f.Path != "" {
			if strings.HasPrefix(f.Path, "[") {
				p += f.Path
			} else {
				p += "." + f.Path
			}
		}
		out.Fields = append(out.Fields, FieldError{Path: p, Message: f.Message})
	}
	return out
}

// SessionParams は開催回データの検証に使う共通項目。
type SessionParams struct {
	DurationMinutes int
}

// Playbook はアプリ内に静的に登録する用途別プラグイン。
// DB 更新・通知・同意の作成は行わず、判断材料と用途固有の制約を提供する。
// 用途固有の Data は用途側の型へデコードして検証する。
type Playbook interface {
	Descriptor() Descriptor

	// ValidateSessionData は開催回登録時の SessionData を検証し、正規化した値を返す。
	ValidateSessionData(ctx context.Context, params SessionParams, data json.RawMessage) (json.RawMessage, error)
	// ValidatePreparation は本人の PreparationData を検証し、正規化した値を返す。
	ValidatePreparation(ctx context.Context, s Snapshot, attendance string, data json.RawMessage) (json.RawMessage, error)
	// ApplyWithdrawal は辞退時に参加条件データを「担当できない」状態へ変換する純粋関数。current=nil は未回答。
	ApplyWithdrawal(ctx context.Context, s Snapshot, scope string, current json.RawMessage) (json.RawMessage, error)
	// Assignees は計画で本人の引き受けが必要な担当者を返す。
	Assignees(plan json.RawMessage) ([]string, error)

	// BuildContext は確認済みの共有可能な状態から AI の判断材料を構築する。
	BuildContext(ctx context.Context, s Snapshot) (json.RawMessage, error)
	// Instructions は用途ごとの判断指示。
	Instructions() string
	// PlanSchema は PlanData の JSON Schema。AI のツール定義に使う。
	PlanSchema() json.RawMessage
	// ValidatePlan は提案された計画の用途固有の制約を検証する。
	ValidatePlan(ctx context.Context, s Snapshot, p Proposal) error
	// ApprovalRequirements はその案に必要な投票・承認・本人の引き受け条件を返す。
	ApprovalRequirements(ctx context.Context, s Snapshot, p Proposal) (ApprovalRequirements, error)
}

// DraftPlanner は LLM を使わずに用途の規則だけで次の一手を作る。
// AGENT_MODE=fake の仮の判断処理で使う。実装しない用途では案を作らず管理者へ戻す。
type DraftPlanner interface {
	DraftPlan(ctx context.Context, s Snapshot) (Draft, error)
}

// PlanScheduler は「案が開催日時も決める」用途で実装する任意のインターフェース。
// 期間だけで登録された開催回（ScheduleStatus=proposed）で、確定時にどの日時になったかを共通側へ渡す。
type PlanScheduler interface {
	// PlannedStart は案が決めた開催日時を返す。案が日時を含まなければ ok=false。
	PlannedStart(plan json.RawMessage) (time.Time, bool, error)
}

// plannedStart は用途が日時を決めるならその値を返す。決めない用途では ok=false。
func plannedStart(pb Playbook, plan json.RawMessage) (time.Time, bool) {
	sc, ok := pb.(PlanScheduler)
	if !ok {
		return time.Time{}, false
	}
	at, ok, err := sc.PlannedStart(plan)
	if err != nil || !ok {
		return time.Time{}, false
	}
	return at, true
}

// PreparationDiffer は保存前後の参加条件の差分を、人が読める1行ずつの説明にする用途。
// 記録に残すのは差分だけで、変更の理由や発言の原文は残さない。
type PreparationDiffer interface {
	// DiffPreparation は before（未回答なら nil）から after への変更点を返す。変更がなければ空。
	DiffPreparation(s Snapshot, before *Preparation, after Preparation) []string
}

// preparationDiff は用途が差分を作れるならその行を返す。作れない用途では空。
func preparationDiff(pb Playbook, s Snapshot, before *Preparation, after Preparation) []string {
	d, ok := pb.(PreparationDiffer)
	if !ok {
		return nil
	}
	return d.DiffPreparation(s, before, after)
}
