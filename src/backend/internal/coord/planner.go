package coord

import (
	"context"
	"encoding/json"
	"errors"
)

// Planner の失敗の種類（docs/api-endpoint.md のエラー表）。
var (
	// ErrTransient は通信エラー・タイムアウト・429・5xx などの一時的な障害。指数バックオフで再試行する。
	ErrTransient = errors.New("planner: transient failure")
	// ErrInvalidOutput は同じ起動内のやり直しでも出力が直らなかったこと。管理者判断待ち（model_error）。
	ErrInvalidOutput = errors.New("planner: invalid output")
	// ErrBudgetExceeded は呼び出し回数・費用の上限に達したこと。再試行しない。
	ErrBudgetExceeded = errors.New("planner: budget exceeded")
)

// Outcome は計画処理が選んだ次の一手。Kind は DraftKind と同じ値を使う。
type Outcome struct {
	Kind         DraftKind       `json:"kind"`
	Summary      string          `json:"summary"`
	Plan         json.RawMessage `json:"plan,omitempty"`
	AskMemberIDs []string        `json:"ask_member_ids,omitempty"`
}

// PlanRequest は1回の計画処理への入力。
type PlanRequest struct {
	Snapshot   Snapshot
	Playbook   Playbook
	ChangeKind string
	// Check は結果をサーバーの規則で検証する。検証エラーは AI に返してやり直させる。
	Check func(ctx context.Context, o Outcome) error
	// MaxLLMCalls はこの起動で使える LLM 呼び出し回数（案件累計の残りと1起動の上限の小さい方）。
	MaxLLMCalls int
}

// LLM 呼び出しの用途（llm_calls.purpose）。case_id・lookup_id だけでは
// 計画と解釈、目次検索と目次画像を区別できないため、呼び出し元がこれを付ける。
const (
	PurposePlanner     = "planner"
	PurposeInterpreter = "interpreter"
	PurposeTocSearch   = "toc_search"
	PurposeTocVision   = "toc_vision"
)

// LLMCall は LLM 呼び出し1回の記録。金額が分からなければ nil（0 として扱わない）。
type LLMCall struct {
	// Model は要求したモデル。Named Router を使う場合はルーター名が入る。
	Model string
	// Purpose は呼び出し元（Purpose* のいずれか）。
	Purpose string
	// ResolvedModel は実際に応答したモデル。取得できなければ空にし、Model で代用しない。
	ResolvedModel string
	// FallbackLevel は 0 が本命成功、1 以上で受け皿が発動したこと。取得できなければ nil。
	FallbackLevel *int
	RequestID     string
	// LatencyMS は OrcaRouter が計測した所要時間（ミリ秒）。取得できなければ nil。
	LatencyMS       *int
	InputTokens     *int
	OutputTokens    *int
	Currency        string
	EstimatedAmount *string
	BilledAmount    *string
	Succeeded       bool
}

// Usage は計画処理の実行量。失敗時も分かった分を返す。
type Usage struct {
	LLMCalls  []LLMCall
	ToolCalls int
}

// Planner は案件の状況から次の一手を選ぶ。実行してよいかの判定は Coordinator が行う。
type Planner interface {
	Plan(ctx context.Context, req PlanRequest) (Outcome, Usage, error)
}

// DraftOnlyPlanner は LLM を呼ばず、Playbook の規則（DraftPlanner）だけで次の一手を作る。
type DraftOnlyPlanner struct{}

func (DraftOnlyPlanner) Plan(ctx context.Context, req PlanRequest) (Outcome, Usage, error) {
	usage := Usage{ToolCalls: 1}
	dp, ok := req.Playbook.(DraftPlanner)
	if !ok {
		return Outcome{Kind: DraftNoFeasible, Summary: "この用途では自動の計画に対応していないため、管理者の判断が必要です。"}, usage, nil
	}
	d, err := dp.DraftPlan(ctx, req.Snapshot)
	if err != nil {
		return Outcome{}, usage, err
	}
	o := Outcome(d)
	if err := req.Check(ctx, o); err != nil {
		return Outcome{}, usage, errors.Join(ErrInvalidOutput, err)
	}
	return o, usage, nil
}
