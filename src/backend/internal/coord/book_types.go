package coord

import (
	"context"
	"encoding/json"
	"errors"
)

// ブックの全体計画（章割り・開催目安・担当）の入出力。用途固有の規則は BookPlanner が持ち、
// 共通側はDBの更新・承認・通知だけを行う。AIが作るのは BookPlan だけで、
// 章・メンバー・別ブックの割り当てをAIの出力で新しく作ったり書き換えたりすることはできない。

// BookSection はブックの最上位の章。
type BookSection struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// BookPlanMember は担当に割り当てられるメンバー（在籍していてログイン済みの人）。
type BookPlanMember struct {
	ID          string `json:"member_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// BookMemberLoad は同じグループ内の担当負担。同時進行中の全ブックを合算する。
type BookMemberLoad struct {
	// Concurrent は同時進行中の全ブックで担当する未完了の枠数（計画中のブック自身は含まない）。
	Concurrent int `json:"concurrent_assignments"`
	// Completed は担当を務め終えた枠数（負担の履歴）。
	Completed int `json:"completed_assignments"`
}

// BookOtherSlot は同時進行中の別ブックの担当済みの枠。日程が重なる担当を避けるために示す。
type BookOtherSlot struct {
	BookTitle        string `json:"book_title"`
	PeriodStart      string `json:"period_start"`
	PeriodEnd        string `json:"period_end"`
	AssigneeMemberID string `json:"assignee_member_id"`
}

// BookPlanInput は全体計画の判断材料。サーバーが組み立てた値だけを含む。
type BookPlanInput struct {
	Title           string                    `json:"book_title"`
	Sections        []BookSection             `json:"sections"`
	PeriodStart     string                    `json:"period_start"`
	PeriodEnd       string                    `json:"period_end"`
	SlotCount       int                       `json:"session_count"`
	DurationMinutes int                       `json:"duration_minutes"`
	Members         []BookPlanMember          `json:"members"`
	Loads           map[string]BookMemberLoad `json:"member_loads"`
	Others          []BookOtherSlot           `json:"concurrent_books_slots"`
}

// BookPlanSlot は1回分の枠の計画。
type BookPlanSlot struct {
	Sequence         int      `json:"sequence"`
	PeriodStart      string   `json:"period_start"`
	PeriodEnd        string   `json:"period_end"`
	SectionIDs       []string `json:"section_ids"`
	AssigneeMemberID string   `json:"assignee_member_id"`
}

// BookPlan は全枠分の計画案。承認されるまで仮の割り当てにすぎない。
type BookPlan struct {
	Summary string         `json:"summary"`
	Slots   []BookPlanSlot `json:"slots"`
}

// ReplacementInput は担当変更の候補を選ぶ判断材料。Loads にはこの枠の現在の担当を含めない。
type ReplacementInput struct {
	BookTitle   string                    `json:"book_title"`
	Sequence    int                       `json:"sequence"`
	PeriodStart string                    `json:"period_start"`
	PeriodEnd   string                    `json:"period_end"`
	SectionIDs  []string                  `json:"section_ids"`
	Current     string                    `json:"current_assignee_member_id"`
	Excluded    []string                  `json:"excluded_member_ids"`
	Members     []BookPlanMember          `json:"members"`
	Loads       map[string]BookMemberLoad `json:"member_loads"`
	// BookAssignees はこのブックの各回の担当（回の番号 → メンバーID）。
	BookAssignees map[int]string `json:"book_assignees"`
}

// BookMaterial は検証済みのブックの教材情報。
type BookMaterial struct {
	Title     string
	ISBN      *string
	TocSource json.RawMessage
	Sections  json.RawMessage
	List      []BookSection
}

// ErrNoReplacement は担当変更の候補になれる人がいないこと。
var ErrNoReplacement = errors.New("担当変更の候補になれるメンバーがいません")

// BookPlanner はブックの全体計画に関する用途固有の規則。AIの出力は必ず検証層を通す。
type BookPlanner interface {
	// ValidateBookMaterial は書名・ISBN・目次情報・章を検証し、正規化した値を返す。
	ValidateBookMaterial(title string, isbn *string, tocSource, sections json.RawMessage) (BookMaterial, error)
	// SplitBook は期間を開催回数で分け、全章を登録順に全回へ割り当てた枠を返す（担当は空）。
	SplitBook(sections []BookSection, periodStart, periodEnd string, n int) ([]BookPlanSlot, error)
	// DraftBookPlan は規則だけで全体計画を作る（AGENT_MODE=fake と検証の基準に使う）。
	DraftBookPlan(in BookPlanInput) (BookPlan, error)
	// ValidateBookPlan は計画案を検証する。章の欠落・重複、開催目安の順序、担当の実在と負担を確認する。
	ValidateBookPlan(in BookPlanInput, p BookPlan) error
	// DraftReplacement は担当変更の候補を規則で選ぶ。なれる人がいなければ ErrNoReplacement。
	DraftReplacement(in ReplacementInput) (string, error)
	// ValidateReplacement は候補を検証する。
	ValidateReplacement(in ReplacementInput, memberID string) error
}

// BookPlanRequest は全体計画の作成依頼。
type BookPlanRequest struct {
	Input   BookPlanInput
	Planner BookPlanner
	// Check は結果をサーバーの規則で検証する。検証エラーはAIに返してやり直させる。
	Check       func(BookPlan) error
	MaxLLMCalls int
}

// ReplacementRequest は担当変更の候補の作成依頼。
type ReplacementRequest struct {
	Input       ReplacementInput
	Planner     BookPlanner
	Check       func(memberID string) error
	MaxLLMCalls int
}

// BookAgent はブックの全体計画と担当変更の候補を作る。LLM（OrcaRouter）の境界。
// 同意や承認を記録する手段は持たない。失敗時は未承認の計画を確定せず、呼び出し側が管理者へ戻す。
type BookAgent interface {
	PlanBook(ctx context.Context, req BookPlanRequest) (BookPlan, Usage, error)
	ProposeReplacement(ctx context.Context, req ReplacementRequest) (string, Usage, error)
}

// DraftBookAgent はLLMを使わず、用途の規則だけで計画と候補を作る（AGENT_MODE=fake）。
type DraftBookAgent struct{}

func (DraftBookAgent) PlanBook(_ context.Context, req BookPlanRequest) (BookPlan, Usage, error) {
	usage := Usage{ToolCalls: 1}
	p, err := req.Planner.DraftBookPlan(req.Input)
	if err != nil {
		return BookPlan{}, usage, errors.Join(ErrInvalidOutput, err)
	}
	if err := req.Check(p); err != nil {
		return BookPlan{}, usage, errors.Join(ErrInvalidOutput, err)
	}
	return p, usage, nil
}

func (DraftBookAgent) ProposeReplacement(_ context.Context, req ReplacementRequest) (string, Usage, error) {
	usage := Usage{ToolCalls: 1}
	id, err := req.Planner.DraftReplacement(req.Input)
	if errors.Is(err, ErrNoReplacement) {
		return "", usage, err
	}
	if err != nil {
		return "", usage, errors.Join(ErrInvalidOutput, err)
	}
	if err := req.Check(id); err != nil {
		return "", usage, errors.Join(ErrInvalidOutput, err)
	}
	return id, usage, nil
}
