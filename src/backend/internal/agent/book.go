package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
)

const bookInstructions = `あなたは輪読会のブックの全体計画を作る運営エージェントです。ツールを1回だけ呼んで提案します。
- あなたが決めるのは、全回分の章割り・開催目安・担当者の「提案」だけです。同意・承認・引き受けを記録することはできません。担当者本人が後から承認します。
- 章（sections）は入力の順序のまま、欠落も重複もなく全回へ割り当てる。各回に1章以上。章のIDや題名を新しく作らない。
- 開催目安（period_start〜period_end）は全体期間の中で、回の順に重ならないようにする。
- 担当者は members に含まれる member_id だけから選ぶ。存在しない人を作らない。同じ人を続けて担当にしない。
- 負担は同じグループで同時進行中の全ブック（member_loads・concurrent_books_slots）を合わせて考える。concurrent_assignments が少ない人を優先し、別ブックで開催目安が重なる人は避ける。他のブックの割り当ては変更できない。
- 入力にない個人の事情を推測しない。summary には共有してよい説明だけを書く。
- サーバーが検証し、条件を満たさなければエラー内容を返します。その内容に従って修正してください。`

const replacementInstructions = `あなたは輪読会の担当変更の候補を選ぶ運営エージェントです。propose_assignee を1回だけ呼びます。
- 提案するだけで、候補本人が承認するまで担当は変わりません。承認を記録することはできません。
- member_id は members から選ぶ。current_assignee_member_id と excluded_member_ids の人、存在しない人は選ばない。
- 同時進行中の全ブックを合わせた負担（member_loads の concurrent_assignments）が最も少ない人を選ぶ。隣の回の担当（book_assignees）は、他に人がいれば避ける。
- サーバーが検証し、条件を満たさなければエラー内容を返します。その内容に従って修正してください。`

// LLMBookAgent はブックの全体計画と担当変更の候補を、LLM（OrcaRouter）に提案させる coord.BookAgent。
// 提案は必ずサーバー側の検証を通し、同意や承認を記録するツールは持たない。
type LLMBookAgent struct {
	Client *Client
	Model  string
}

var _ coord.BookAgent = (*LLMBookAgent)(nil)

func bookTools() []Tool {
	return []Tool{{Type: "function", Function: ToolFunction{
		Name:        "propose_book_plan",
		Description: "全回分の章割り・開催目安・担当者の仮の割り当てを提出する。担当者本人の承認は別に取得される。",
		Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","slots"],"properties":{
"summary":{"type":"string","description":"共有してよい計画の説明"},
"slots":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["sequence","period_start","period_end","section_ids","assignee_member_id"],"properties":{
"sequence":{"type":"integer","minimum":1},
"period_start":{"type":"string","description":"開催目安の開始日 YYYY-MM-DD"},
"period_end":{"type":"string","description":"開催目安の終了日 YYYY-MM-DD"},
"section_ids":{"type":"array","items":{"type":"string"},"minItems":1},
"assignee_member_id":{"type":"string"}}}}}}`),
	}}}
}

func replacementTools() []Tool {
	return []Tool{{Type: "function", Function: ToolFunction{
		Name:        "propose_assignee",
		Description: "担当変更の候補を1人提案する。候補本人が承認するまで担当は変わらない。",
		Parameters:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["member_id"],"properties":{"member_id":{"type":"string"}}}`),
	}}}
}

// toolLoop はツール呼び出しを1回受け取り、検証エラーを返してやり直させる（最大 maxRepairs 回）。
func (a *LLMBookAgent) toolLoop(ctx context.Context, system, user string, tools []Tool, maxCalls int, handle func(ToolCall) error) (coord.Usage, error) {
	var usage coord.Usage
	messages := []Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	for attempt := 0; attempt <= maxRepairs; attempt++ {
		if len(usage.LLMCalls) >= maxCalls {
			return usage, coord.ErrBudgetExceeded
		}
		res, call, err := a.Client.Chat(ctx, ChatRequest{Model: a.Model, Messages: messages, Tools: tools, ToolChoice: "required"})
		usage.LLMCalls = append(usage.LLMCalls, call)
		if err != nil && !errors.Is(err, ErrMalformedResponse) {
			return usage, err
		}
		var feedback string
		var tc *ToolCall
		switch {
		case err != nil || len(res.Choices) == 0:
			feedback = "応答を読めませんでした。ツールを1つ呼んでください。"
		case len(res.Choices[0].Message.ToolCalls) == 0:
			feedback = "ツールを呼ばずに回答しました。指定のツールを1回呼んでください。"
		default:
			tc = &res.Choices[0].Message.ToolCalls[0]
			usage.ToolCalls++
			if herr := handle(*tc); herr == nil {
				return usage, nil
			} else {
				feedback = "サーバーの検証で拒否されました。次の点を直して、もう一度ツールを呼んでください：\n" + herr.Error()
			}
		}
		if tc != nil {
			messages = append(messages, Message{Role: "assistant", ToolCalls: []ToolCall{*tc}}, Message{Role: "tool", ToolCallID: tc.ID, Content: feedback})
		} else {
			messages = append(messages, Message{Role: "user", Content: feedback})
		}
	}
	return usage, coord.ErrInvalidOutput
}

// PlanBook は全体計画を提案させる。検証（req.Check）に通らない出力は直して再提出させる。
func (a *LLMBookAgent) PlanBook(ctx context.Context, req coord.BookPlanRequest) (coord.BookPlan, coord.Usage, error) {
	input, err := json.Marshal(req.Input)
	if err != nil {
		return coord.BookPlan{}, coord.Usage{}, err
	}
	var plan coord.BookPlan
	usage, err := a.toolLoop(ctx, bookInstructions, "ブックの状況（JSON）：\n"+string(input), bookTools(), req.MaxLLMCalls, func(tc ToolCall) error {
		if tc.Function.Name != "propose_book_plan" {
			return fmt.Errorf("未知のツールです: %s", tc.Function.Name)
		}
		var args struct {
			Summary string               `json:"summary"`
			Slots   []coord.BookPlanSlot `json:"slots"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Errorf("引数が JSON として読めません: %v", err)
		}
		p := coord.BookPlan{Summary: clipSummary(args.Summary), Slots: args.Slots}
		if err := req.Check(p); err != nil {
			return err
		}
		plan = p
		return nil
	})
	return plan, usage, err
}

// ProposeReplacement は担当変更の候補を1人提案させる。
func (a *LLMBookAgent) ProposeReplacement(ctx context.Context, req coord.ReplacementRequest) (string, coord.Usage, error) {
	input, err := json.Marshal(req.Input)
	if err != nil {
		return "", coord.Usage{}, err
	}
	var chosen string
	usage, err := a.toolLoop(ctx, replacementInstructions, "担当変更の状況（JSON）：\n"+string(input), replacementTools(), req.MaxLLMCalls, func(tc ToolCall) error {
		if tc.Function.Name != "propose_assignee" {
			return fmt.Errorf("未知のツールです: %s", tc.Function.Name)
		}
		var args struct {
			MemberID string `json:"member_id"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return fmt.Errorf("引数が JSON として読めません: %v", err)
		}
		if err := req.Check(args.MemberID); err != nil {
			return err
		}
		chosen = args.MemberID
		return nil
	})
	return chosen, usage, err
}

func clipSummary(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxSummaryLen {
		return string([]rune(s)[:maxSummaryLen])
	}
	return s
}

// WithBookFaults は開発モードの障害注入（llm: error / invalid_output）をブックの計画にも反映する。
func WithBookFaults(next coord.BookAgent, faults *fault.Registry) coord.BookAgent {
	return faultBookAgent{next: next, faults: faults}
}

type faultBookAgent struct {
	next   coord.BookAgent
	faults *fault.Registry
}

func (f faultBookAgent) injected() (coord.Usage, error) {
	usage := coord.Usage{LLMCalls: []coord.LLMCall{{Model: "fault-injection", Currency: "unknown"}}}
	switch f.faults.Get("llm") {
	case "error":
		return usage, fmt.Errorf("%w: 障害注入", coord.ErrTransient)
	case "invalid_output":
		return usage, fmt.Errorf("%w: 障害注入", coord.ErrInvalidOutput)
	}
	return usage, nil
}

func (f faultBookAgent) PlanBook(ctx context.Context, req coord.BookPlanRequest) (coord.BookPlan, coord.Usage, error) {
	if usage, err := f.injected(); err != nil {
		return coord.BookPlan{}, usage, err
	}
	return f.next.PlanBook(ctx, req)
}

func (f faultBookAgent) ProposeReplacement(ctx context.Context, req coord.ReplacementRequest) (string, coord.Usage, error) {
	if usage, err := f.injected(); err != nil {
		return "", usage, err
	}
	return f.next.ProposeReplacement(ctx, req)
}
