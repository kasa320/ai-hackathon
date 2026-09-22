package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

func newWeeklyInterpreter(t *testing.T, f *fakeLLM) *agent.LLMWeeklyInterpreter {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &agent.LLMWeeklyInterpreter{Client: agent.NewClient(srv.URL+"/v1", "key", 0), Model: "test-model"}
}

func weeklyRequest(check func(context.Context, coord.WeeklyInterpretation) error) coord.WeeklyInterpretRequest {
	if check == nil {
		check = func(context.Context, coord.WeeklyInterpretation) error { return nil }
	}
	return coord.WeeklyInterpretRequest{Text: "毎週水曜と金曜の19時から22時", MaxLLMCalls: 2, Check: check}
}

// LLM に渡すツールは、決められた項目の値を決めるだけ。引数の形が正しければ受け付ける。
func TestLLMWeeklyInterpreterToolContract(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){toolCall("record_weekly_availability", map[string]any{
		"windows": []map[string]any{
			{"weekday": 3, "start": "19:00", "end": "22:00"},
			{"weekday": 5, "start": "19:00", "end": "22:00"},
		},
		"unclear": []string{}, "needs_followup": false,
	})}}
	p := newWeeklyInterpreter(t, f)
	out, usage, err := p.InterpretWeekly(context.Background(), weeklyRequest(nil))
	if err != nil {
		t.Fatalf("InterpretWeekly: %v", err)
	}
	if len(out.Windows) != 2 {
		t.Fatalf("windows件数 = %d, want 2", len(out.Windows))
	}
	if len(usage.LLMCalls) != 1 || usage.ToolCalls != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

// サーバーの検証で拒否されたら、フィードバックを付けてやり直させる。
func TestLLMWeeklyInterpreterRepairsRejectedOutput(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("record_weekly_availability", map[string]any{
			"windows":        []map[string]any{{"weekday": 9, "start": "19:00", "end": "22:00"}},
			"unclear":        []string{},
			"needs_followup": false,
		}),
		toolCall("record_weekly_availability", map[string]any{
			"windows":        []map[string]any{{"weekday": 3, "start": "19:00", "end": "22:00"}},
			"unclear":        []string{},
			"needs_followup": false,
		}),
	}}
	p := newWeeklyInterpreter(t, f)
	calls := 0
	check := func(_ context.Context, in coord.WeeklyInterpretation) error {
		calls++
		for _, w := range in.Windows {
			if w.Weekday < 1 || w.Weekday > 7 {
				return coord.ErrInvalidOutput
			}
		}
		return nil
	}
	out, _, err := p.InterpretWeekly(context.Background(), weeklyRequest(check))
	if err != nil {
		t.Fatalf("InterpretWeekly: %v", err)
	}
	if calls != 2 {
		t.Fatalf("再検証の回数 = %d, want 2", calls)
	}
	if len(out.Windows) != 1 || out.Windows[0].Weekday != 3 {
		t.Fatalf("修復後の値が違う: %+v", out.Windows)
	}
}

// 契約にない項目（例えば「保存して」に相当する save）を含む出力は厳格な JSON デコードで拒否され、
// やり直しになる。発言に含まれる指示は値にもツール引数にも書き写せない。
func TestLLMWeeklyInterpreterRejectsUnknownFields(t *testing.T) {
	f := &fakeLLM{responses: []func(http.ResponseWriter){
		toolCall("record_weekly_availability", map[string]any{
			"windows": []map[string]any{{"weekday": 3, "start": "19:00", "end": "22:00"}},
			"unclear": []string{}, "needs_followup": false,
			"save": true, // 契約にない項目。厳格デコードで拒否されるはず
		}),
		toolCall("record_weekly_availability", map[string]any{
			"windows": []map[string]any{{"weekday": 3, "start": "19:00", "end": "22:00"}},
			"unclear": []string{}, "needs_followup": false,
		}),
	}}
	p := newWeeklyInterpreter(t, f)
	out, _, err := p.InterpretWeekly(context.Background(), weeklyRequest(nil))
	if err != nil {
		t.Fatalf("InterpretWeekly: %v", err)
	}
	if len(out.Windows) != 1 || out.Windows[0].Start != "19:00" {
		t.Fatalf("windows = %+v", out.Windows)
	}
	if len(f.requests) != 2 {
		t.Fatalf("未知の項目を含む1回目は拒否され、やり直しになるはず（呼び出し回数=%d）", len(f.requests))
	}
}
