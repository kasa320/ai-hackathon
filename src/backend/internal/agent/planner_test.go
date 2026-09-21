package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

// fakeLLM は OpenAI 互換 API の代わりに、決まった応答を順に返す。
type fakeLLM struct {
	mu        sync.Mutex
	responses []func(w http.ResponseWriter)
	requests  []map[string]any
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.requests = append(f.requests, body)
	next := f.responses[0]
	f.responses = f.responses[1:]
	next(w)
}

func toolCall(name string, args any) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		a, _ := json.Marshal(args)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": nil, "tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}},
			}}}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
		})
	}
}

func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code) }
}

const sessionData = `{"book_title":"本","isbn":null,"toc_source":{"kind":"manual","urls":[]},
 "sections":[{"id":"sec_1","title":"前回"},{"id":"sec_2","title":"前半"},{"id":"sec_3","title":"後半"}],
 "completed_section_ids":["sec_1"],"target_section_ids":["sec_2","sec_3"]}`

func request(check func(context.Context, coord.Outcome) error) coord.PlanRequest {
	// prep は参加条件。declined なら今回の説明の担当を辞退している。
	prep := func(declined bool) *coord.Preparation {
		d, _ := json.Marshal(map[string]any{"declined_presentation": declined})
		return &coord.Preparation{Attendance: "attending", Data: d}
	}
	return coord.PlanRequest{
		Snapshot: coord.Snapshot{
			StartsAt: time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC), DurationMinutes: 60, OwnerMemberID: "mem_a",
			Members:     []coord.SnapshotMember{{ID: "mem_a", DisplayName: "A", Role: "owner"}, {ID: "mem_c", DisplayName: "C", Role: "member"}},
			SessionData: json.RawMessage(sessionData),
			Preparations: []coord.MemberPreparation{
				{MemberID: "mem_a", Value: prep(true)},
				{MemberID: "mem_c", Value: prep(false)},
			},
		},
		Playbook: reading.New(), ChangeKind: coord.ChangeInitial, MaxLLMCalls: 8, Check: check,
	}
}

var validPlan = map[string]any{
	"covered_section_ids": []string{"sec_2"}, "deferred_section_ids": []string{"sec_3"},
	"agenda": []any{
		map[string]any{"id": "i1", "activity": "presentation", "section_ids": []string{"sec_2"}, "presenter_member_id": "mem_c", "minutes": 15},
		map[string]any{"id": "i2", "activity": "discussion", "section_ids": []string{"sec_2"}, "presenter_member_id": nil, "minutes": 30},
	},
}

func newPlanner(t *testing.T, f *fakeLLM) *agent.LLMPlanner {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &agent.LLMPlanner{Client: agent.NewClient(srv.URL+"/v1", "key", 0), Model: "test-model"}
}

func TestLLMPlannerReturnsValidatedProposal(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("propose_plan", map[string]any{"summary": "C が第2節を説明", "plan": validPlan})}}
	p := newPlanner(t, f)
	checked := 0
	o, usage, err := p.Plan(context.Background(), request(func(context.Context, coord.Outcome) error { checked++; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != coord.DraftProposal || o.Summary != "C が第2節を説明" || checked != 1 {
		t.Fatalf("outcome = %+v", o)
	}
	if len(usage.LLMCalls) != 1 || *usage.LLMCalls[0].InputTokens != 100 || usage.LLMCalls[0].Currency != "unknown" || usage.LLMCalls[0].EstimatedAmount != nil {
		t.Fatalf("usage = %+v", usage.LLMCalls)
	}
	// ツールは提案・確認依頼・管理者へ戻すの3つだけ。同意を記録するツールは渡さない。
	tools := f.requests[0]["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "propose_plan,request_preparation,report_no_feasible_plan" {
		t.Fatalf("tools = %v", names)
	}
}

// 不正な出力は検証エラーをモデルに返し、同じ起動内で最大2回やり直させる。
func TestLLMPlannerRepairsInvalidOutput(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){
		func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ツールなし"}}]}`))
		},
		toolCall("propose_plan", map[string]any{"summary": "x", "plan": map[string]any{"bad": true}}),
		toolCall("propose_plan", map[string]any{"summary": "ok", "plan": validPlan}),
	}}
	p := newPlanner(t, f)
	pb := reading.New()
	req := request(nil)
	req.Check = func(ctx context.Context, o coord.Outcome) error {
		return pb.ValidatePlan(ctx, req.Snapshot, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: o.Plan})
	}
	o, usage, err := p.Plan(context.Background(), req)
	if err != nil || o.Summary != "ok" || len(usage.LLMCalls) != 3 {
		t.Fatalf("o=%+v calls=%d err=%v", o, len(usage.LLMCalls), err)
	}
	// 2回目の呼び出しには検証エラーが tool メッセージで返されている。
	msgs := f.requests[2]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || !strings.Contains(last["content"].(string), "検証で拒否") {
		t.Fatalf("検証エラーが返されていない: %v", last)
	}

	f = &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("unknown_tool", map[string]any{}), toolCall("unknown_tool", map[string]any{}), toolCall("unknown_tool", map[string]any{}),
	}}
	p = newPlanner(t, f)
	if _, usage, err := p.Plan(context.Background(), request(func(context.Context, coord.Outcome) error { return nil })); !errors.Is(err, coord.ErrInvalidOutput) || len(usage.LLMCalls) != 3 {
		t.Fatalf("3回失敗したら ErrInvalidOutput: %v (%d)", err, len(usage.LLMCalls))
	}
}

func TestLLMPlannerErrorKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		resp func(http.ResponseWriter)
		want error
	}{
		"429": {status(http.StatusTooManyRequests), coord.ErrTransient},
		"503": {status(http.StatusServiceUnavailable), coord.ErrTransient},
		"401": {status(http.StatusUnauthorized), nil},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPlanner(t, &fakeLLM{responses: []func(http.ResponseWriter){tc.resp}})
			_, usage, err := p.Plan(context.Background(), request(func(context.Context, coord.Outcome) error { return nil }))
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.want == nil && errors.Is(err, coord.ErrTransient)) {
				t.Fatalf("err = %v", err)
			}
			if len(usage.LLMCalls) != 1 || usage.LLMCalls[0].Succeeded {
				t.Fatalf("失敗した呼び出しも記録する: %+v", usage.LLMCalls)
			}
		})
	}
	// 通信できない場合も一時的な障害。
	p := &agent.LLMPlanner{Client: agent.NewClient("http://127.0.0.1:1", "key", 0), Model: "m"}
	if _, _, err := p.Plan(context.Background(), request(func(context.Context, coord.Outcome) error { return nil })); !errors.Is(err, coord.ErrTransient) {
		t.Fatalf("接続失敗は一時的な障害: %v", err)
	}
}

func TestLLMPlannerRespectsCallBudget(t *testing.T) {
	p := newPlanner(t, &fakeLLM{})
	req := request(func(context.Context, coord.Outcome) error { return nil })
	req.MaxLLMCalls = 0
	if _, usage, err := p.Plan(context.Background(), req); !errors.Is(err, coord.ErrBudgetExceeded) || len(usage.LLMCalls) != 0 {
		t.Fatalf("上限に達したら呼ばない: %v", err)
	}
}

func TestFaultInjection(t *testing.T) {
	faults := fault.New()
	faults.Define("llm", "error", "invalid_output")
	p := agent.WithFaults(coord.DraftOnlyPlanner{}, faults)
	req := request(func(context.Context, coord.Outcome) error { return nil })

	if _, _, err := p.Plan(context.Background(), req); err != nil {
		t.Fatalf("障害なしなら通常の処理: %v", err)
	}
	v := "error"
	if err := faults.Replace(map[string]*string{"llm": &v}); err != nil {
		t.Fatal(err)
	}
	if _, usage, err := p.Plan(context.Background(), req); !errors.Is(err, coord.ErrTransient) || len(usage.LLMCalls) != 1 {
		t.Fatalf("llm=error は一時的な障害: %v", err)
	}
	bad := "boom"
	if err := faults.Replace(map[string]*string{"llm": &bad}); err == nil {
		t.Fatal("未定義の値は拒否")
	}
	if err := faults.Replace(map[string]*string{"unknown": nil}); err == nil {
		t.Fatal("未定義のキーは拒否")
	}
}
