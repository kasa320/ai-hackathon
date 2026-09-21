package agent_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

func newBookAgent(t *testing.T, f *fakeLLM) *agent.LLMBookAgent {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &agent.LLMBookAgent{Client: agent.NewClient(srv.URL+"/v1", "key", 0), Model: "test-model"}
}

func bookRequest() coord.BookPlanRequest {
	in := coord.BookPlanInput{
		Title: "本", PeriodStart: "2026-10-01", PeriodEnd: "2026-10-21", SlotCount: 2, DurationMinutes: 60,
		Sections: []coord.BookSection{{ID: "s1", Title: "1"}, {ID: "s2", Title: "2"}},
		Members:  []coord.BookPlanMember{{ID: "a", DisplayName: "A"}, {ID: "b", DisplayName: "B"}},
		Loads:    map[string]coord.BookMemberLoad{"a": {}, "b": {}},
	}
	pb := reading.New()
	return coord.BookPlanRequest{Input: in, Planner: pb, MaxLLMCalls: 8, Check: func(p coord.BookPlan) error { return pb.ValidateBookPlan(in, p) }}
}

func goodBookPlan() map[string]any {
	return map[string]any{"summary": "AとBが交代で担当", "slots": []any{
		map[string]any{"sequence": 1, "period_start": "2026-10-01", "period_end": "2026-10-10", "section_ids": []string{"s1"}, "assignee_member_id": "a"},
		map[string]any{"sequence": 2, "period_start": "2026-10-11", "period_end": "2026-10-21", "section_ids": []string{"s2"}, "assignee_member_id": "b"},
	}}
}

func TestLLMBookAgentReturnsValidatedPlanWithoutApprovalTools(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("propose_book_plan", goodBookPlan())}}
	a := newBookAgent(t, f)
	plan, usage, err := a.PlanBook(context.Background(), bookRequest())
	if err != nil || len(plan.Slots) != 2 || plan.Slots[1].AssigneeMemberID != "b" || plan.Summary == "" {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if len(usage.LLMCalls) != 1 || usage.ToolCalls != 1 || *usage.LLMCalls[0].InputTokens != 100 {
		t.Fatalf("usage = %+v", usage)
	}
	// 渡すツールは提案の1つだけ。同意・承認・引き受けを記録するツールは渡さない。
	tools := f.requests[0]["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "propose_book_plan" {
		t.Fatalf("tools = %v", tools)
	}
	// 判断材料は構造化した共有可能な値だけ（個人の理由は入力に存在しない）。
	msgs := f.requests[0]["messages"].([]any)
	body := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{`"member_loads"`, `"sections"`, `"session_count":2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("判断材料に %s がない: %s", want, body)
		}
	}
}

func TestLLMBookAgentRepairsPlanRejectedByServerValidation(t *testing.T) {
	bad := goodBookPlan()
	bad["slots"].([]any)[1].(map[string]any)["assignee_member_id"] = "ghost" // 存在しないメンバーの捏造
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("propose_book_plan", bad), toolCall("propose_book_plan", goodBookPlan())}}
	a := newBookAgent(t, f)
	plan, usage, err := a.PlanBook(context.Background(), bookRequest())
	if err != nil || plan.Slots[1].AssigneeMemberID != "b" || len(usage.LLMCalls) != 2 {
		t.Fatalf("plan = %+v, usage = %d, err = %v", plan, len(usage.LLMCalls), err)
	}
	// 検証エラーの内容がモデルに返されている。
	second := f.requests[1]["messages"].([]any)
	last := second[len(second)-1].(map[string]any)["content"].(string)
	if !strings.Contains(last, "サーバーの検証で拒否") || !strings.Contains(last, "assignee_member_id") {
		t.Fatalf("フィードバック = %s", last)
	}
}

func TestLLMBookAgentFailuresAreNeverAcceptedAsPlans(t *testing.T) {
	alwaysBad := map[string]any{"summary": "x", "slots": []any{}}
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("propose_book_plan", alwaysBad), toolCall("propose_book_plan", alwaysBad), toolCall("propose_book_plan", alwaysBad)}}
	if plan, _, err := newBookAgent(t, f).PlanBook(context.Background(), bookRequest()); !errors.Is(err, coord.ErrInvalidOutput) || len(plan.Slots) != 0 {
		t.Fatalf("直らない出力 = %+v, %v", plan, err)
	}
	f = &fakeLLM{responses: []func(http.ResponseWriter){status(http.StatusServiceUnavailable)}}
	if _, usage, err := newBookAgent(t, f).PlanBook(context.Background(), bookRequest()); !errors.Is(err, coord.ErrTransient) || len(usage.LLMCalls) != 1 {
		t.Fatalf("5xx = %v, calls %d", err, len(usage.LLMCalls))
	}
	// 呼び出し回数の上限を超えない。
	f = &fakeLLM{responses: []func(http.ResponseWriter){toolCall("propose_book_plan", alwaysBad), toolCall("propose_book_plan", alwaysBad)}}
	req := bookRequest()
	req.MaxLLMCalls = 1
	if _, usage, err := newBookAgent(t, f).PlanBook(context.Background(), req); !errors.Is(err, coord.ErrBudgetExceeded) || len(usage.LLMCalls) != 1 {
		t.Fatalf("上限 = %v, calls %d", err, len(usage.LLMCalls))
	}
}

func TestLLMBookAgentProposesReplacementOnlyAfterValidation(t *testing.T) {
	pb := reading.New()
	in := coord.ReplacementInput{
		Sequence: 2, Current: "a", Excluded: []string{"b"},
		Members: []coord.BookPlanMember{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}},
		Loads:   map[string]coord.BookMemberLoad{"a": {}, "b": {}, "c": {Concurrent: 2}, "d": {}}, BookAssignees: map[int]string{},
	}
	req := coord.ReplacementRequest{Input: in, Planner: pb, MaxLLMCalls: 8, Check: func(id string) error { return pb.ValidateReplacement(in, id) }}
	// 現在の担当者（a）・断った人（b）・負担の重い人（c）は拒否され、修正後の d が採用される。
	f := &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("propose_assignee", map[string]any{"member_id": "a"}),
		toolCall("propose_assignee", map[string]any{"member_id": "c"}),
		toolCall("propose_assignee", map[string]any{"member_id": "d"}),
	}}
	id, usage, err := newBookAgent(t, f).ProposeReplacement(context.Background(), req)
	if err != nil || id != "d" || len(usage.LLMCalls) != 3 {
		t.Fatalf("候補 = %q, calls %d, err %v", id, len(usage.LLMCalls), err)
	}
	tools := f.requests[0]["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "propose_assignee" {
		t.Fatalf("tools = %v", tools)
	}
}
