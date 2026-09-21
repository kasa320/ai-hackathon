package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

func newInterpreter(t *testing.T, f *fakeLLM) *agent.LLMInterpreter {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &agent.LLMInterpreter{Client: agent.NewClient(srv.URL+"/v1", "key"), Model: "test-model"}
}

func validPreparation(minutes int) map[string]any {
	return map[string]any{
		"willing_to_present": true, "prepared_section_ids": []string{"sec_2"},
		"explainable_section_ids": []string{"sec_2"}, "max_presentation_minutes": minutes,
	}
}

// interpretRequest は1回の解釈への入力。budget が nil でなければ予算フックを付ける。
func interpretRequest(t *testing.T, partial bool) coord.InterpretRequest {
	t.Helper()
	pb := reading.New()
	snap := request(nil).Snapshot
	return coord.InterpretRequest{
		Snapshot: snap, Playbook: pb, Text: "今回の前半を15分で説明できます", MaxLLMCalls: 2, Partial: partial,
		Check: func(ctx context.Context, in coord.Interpretation) error {
			if in.OutOfScope != "" {
				return nil
			}
			if partial {
				_, _, err := pb.ValidatePartialPreparation(ctx, snap, in.Attendance, in.Data, in.Unclear)
				return err
			}
			_, err := pb.ValidatePreparation(ctx, snap, in.Attendance, in.Data)
			return err
		},
	}
}

// LLM に渡すツールは項目の値を決めるものだけで、引数は決められた項目名・列挙値に限る。
func TestLLMInterpreterToolContract(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("record_preparation", map[string]any{
		// 一部モデルが列挙値の大文字小文字を変えても、範囲内として安全に扱う。
		"attendance": "attending", "data": validPreparation(15), "unclear": []string{}, "needs_followup": false, "out_of_scope": "None",
	})}}
	p := newInterpreter(t, f)
	if _, _, err := p.Interpret(context.Background(), interpretRequest(t, false)); err != nil {
		t.Fatal(err)
	}
	tools := f.requests[0]["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("ツールが1つではない: %v", tools)
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "record_preparation" {
		t.Fatalf("tool = %v", fn["name"])
	}
	params := fn["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	for _, key := range []string{"attendance", "data", "unclear", "needs_followup", "out_of_scope"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("引数 %q がない: %v", key, props)
		}
	}
	// unclear に入れてよい名前と out_of_scope の値は列挙で縛る。
	slots := props["unclear"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	if len(slots) != 5 {
		t.Fatalf("unclear の列挙 = %v", slots)
	}
	kinds := props["out_of_scope"].(map[string]any)["enum"].([]any)
	if len(kinds) != len(coord.OutOfScopeKinds)+1 {
		t.Fatalf("out_of_scope の列挙 = %v", kinds)
	}
	if kinds[0] != "none" {
		t.Fatalf("out_of_scope の範囲内指定 = %v", kinds[0])
	}
}

// 対話では検証済みの値と質問中の項目だけを渡す。発言の履歴は渡さない。
func TestLLMInterpreterSendsCurrentValuesOnly(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("record_preparation", map[string]any{
		"attendance": "attending", "data": validPreparation(15), "unclear": []string{}, "needs_followup": false, "out_of_scope": "none",
	})}}
	p := newInterpreter(t, f)
	req := interpretRequest(t, true)
	cur, _ := json.Marshal(map[string]any{
		"willing_to_present": false, "prepared_section_ids": []string{"sec_2"},
		"explainable_section_ids": []string{}, "max_presentation_minutes": 0,
	})
	req.Current = &coord.Interpretation{Attendance: "attending", Data: cur, Unclear: []string{"max_presentation_minutes"}, NeedsFollowup: true}
	req.Pending = "max_presentation_minutes"
	req.Text = "15分です"
	if _, _, err := p.Interpret(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := f.requests[0]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("履歴を渡している: %d 件", len(msgs))
	}
	content := msgs[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "現在の値") || !strings.Contains(content, "質問中の項目：max_presentation_minutes") {
		t.Fatalf("現在の値と質問中の項目が渡っていない: %q", content)
	}
	if !strings.Contains(content, "15分です") {
		t.Fatal("今回の発言が渡っていない")
	}
	// 過去の発言は残さない。
	if strings.Contains(content, "今回の前半を15分で説明できます") {
		t.Fatal("前のターンの発言が渡っている")
	}
}

// 扱えない依頼は分類だけを受け取り、値は使わない。
func TestLLMInterpreterParsesOutOfScope(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("record_preparation", map[string]any{
		"attendance": "attending", "data": validPreparation(15), "unclear": []string{}, "needs_followup": false,
		"out_of_scope": coord.OutOfScopeScheduleChange,
	})}}
	p := newInterpreter(t, f)
	got, _, err := p.Interpret(context.Background(), interpretRequest(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if got.OutOfScope != coord.OutOfScopeScheduleChange {
		t.Fatalf("out_of_scope = %q", got.OutOfScope)
	}
}

// 不正な出力は検証エラーを返してやり直させ、上限に達したら ErrInvalidOutput。
func TestLLMInterpreterRepairsInvalidOutput(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("record_preparation", map[string]any{
			"attendance": "attending", "data": map[string]any{"willing_to_present": true, "prepared_section_ids": []string{"sec_9"},
				"explainable_section_ids": []string{"sec_9"}, "max_presentation_minutes": 10}, "unclear": []string{}, "needs_followup": false}),
		toolCall("record_preparation", map[string]any{
			"attendance": "attending", "data": validPreparation(15), "unclear": []string{}, "needs_followup": false}),
	}}
	p := newInterpreter(t, f)
	got, usage, err := p.Interpret(context.Background(), interpretRequest(t, false))
	if err != nil || len(usage.LLMCalls) != 2 {
		t.Fatalf("やり直しで成功するはず: %v (%d回)", err, len(usage.LLMCalls))
	}
	if got.Attendance != "attending" {
		t.Fatalf("attendance = %q", got.Attendance)
	}
	msgs := f.requests[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || !strings.Contains(last["content"].(string), "検証で拒否") {
		t.Fatalf("検証エラーが返されていない: %v", last)
	}

	// 2回とも直らなければ諦める（1回の解釈で使えるのは2回まで）。
	f = &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("unknown_tool", map[string]any{}), toolCall("unknown_tool", map[string]any{}),
	}}
	p = newInterpreter(t, f)
	if _, usage, err := p.Interpret(context.Background(), interpretRequest(t, false)); !errors.Is(err, coord.ErrBudgetExceeded) && !errors.Is(err, coord.ErrInvalidOutput) {
		t.Fatalf("諦めるはず: %v (%d回)", err, len(usage.LLMCalls))
	}
}

// 呼び出しの直前に予算の枠を確保し、結果を記録する。確保できなければ呼ばない。
func TestLLMInterpreterReservesBudgetBeforeEachCall(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("record_preparation", map[string]any{
			"attendance": "attending", "data": map[string]any{"willing_to_present": true, "prepared_section_ids": []string{"sec_9"},
				"explainable_section_ids": []string{"sec_9"}, "max_presentation_minutes": 10}, "unclear": []string{}, "needs_followup": false}),
		toolCall("record_preparation", map[string]any{
			"attendance": "attending", "data": validPreparation(15), "unclear": []string{}, "needs_followup": false}),
	}}
	p := newInterpreter(t, f)
	req := interpretRequest(t, false)
	var reserved, recorded []string
	req.Reserve = func(context.Context, string) (string, error) {
		id := "llm_" + string(rune('a'+len(reserved)))
		reserved = append(reserved, id)
		return id, nil
	}
	req.Record = func(_ context.Context, id string, call coord.LLMCall) error {
		if call.Model != "test-model" {
			return errors.New("モデル名が違う")
		}
		recorded = append(recorded, id)
		return nil
	}
	if _, _, err := p.Interpret(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// やり直しの分も1回ずつ確保・記録する。
	if len(reserved) != 2 || len(recorded) != 2 || reserved[0] != recorded[0] {
		t.Fatalf("予約 %v 記録 %v", reserved, recorded)
	}

	// 枠を確保できなければ LLM を呼ばない。
	f2 := &fakeLLM{}
	p2 := newInterpreter(t, f2)
	req2 := interpretRequest(t, false)
	req2.Reserve = func(context.Context, string) (string, error) { return "", coord.ErrBudgetExceeded }
	if _, usage, err := p2.Interpret(context.Background(), req2); !errors.Is(err, coord.ErrBudgetExceeded) || len(usage.LLMCalls) != 0 {
		t.Fatalf("上限なら呼ばない: %v", err)
	}
	if len(f2.requests) != 0 {
		t.Fatalf("上限なのに呼び出した: %d 件", len(f2.requests))
	}
}

// 障害注入でも枠は消費する（失敗しても予算は戻らない）。
func TestInterpretFaultInjectionConsumesBudget(t *testing.T) {
	faults := fault.New()
	faults.Define("llm", "error", "invalid_output")
	p := agent.WithInterpretFaults(coord.DraftOnlyInterpreter{}, faults)
	req := interpretRequest(t, false)
	reserved := 0
	req.Reserve = func(context.Context, string) (string, error) { reserved++; return "llm_1", nil }
	req.Record = func(context.Context, string, coord.LLMCall) error { return nil }

	if _, _, err := p.Interpret(context.Background(), req); err != nil {
		t.Fatalf("障害なしなら通常の処理: %v", err)
	}
	if reserved != 0 {
		t.Fatal("LLM を呼ばない経路で枠を使った")
	}
	v := "error"
	if err := faults.Replace(map[string]*string{"llm": &v}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Interpret(context.Background(), req); !errors.Is(err, coord.ErrTransient) {
		t.Fatalf("llm=error は一時的な障害: %v", err)
	}
	if reserved != 1 {
		t.Fatalf("障害注入で枠を使っていない: %d", reserved)
	}
}
