package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
)

func TestClientRecordsInlineOrcaRouterCost(t *testing.T) {
	t.Run("cost_usd records exact USD amount and tokens", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-OrcaRouter-Include-Cost"); got != "true" {
				t.Fatalf("cost header = %q", got)
			}
			_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34,"cost_usd":0.000000123456789123456789}}`))
		}))
		defer srv.Close()

		_, call, err := agent.NewClient(srv.URL, "key", 0).Chat(context.Background(), agent.ChatRequest{Model: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if call.Currency != "USD" || call.EstimatedAmount == nil || *call.EstimatedAmount != "0.000000123456789123456789" || call.BilledAmount != nil {
			t.Fatalf("cost = %+v", call)
		}
		if call.InputTokens == nil || *call.InputTokens != 12 || call.OutputTokens == nil || *call.OutputTokens != 34 {
			t.Fatalf("tokens = %+v", call)
		}
	})

	t.Run("missing cost is unknown, not zero", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34}}`))
		}))
		defer srv.Close()

		_, call, err := agent.NewClient(srv.URL, "key", 0).Chat(context.Background(), agent.ChatRequest{Model: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if call.Currency != "unknown" || call.EstimatedAmount != nil || call.BilledAmount != nil {
			t.Fatalf("missing cost = %+v", call)
		}
		if call.InputTokens == nil || *call.InputTokens != 12 || call.OutputTokens == nil || *call.OutputTokens != 34 {
			t.Fatalf("tokens = %+v", call)
		}
	})

	t.Run("failed response has no zero cost", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		_, call, err := agent.NewClient(srv.URL, "key", 0).Chat(context.Background(), agent.ChatRequest{Model: "test"})
		if err == nil || call.Currency != "unknown" || call.EstimatedAmount != nil || call.BilledAmount != nil || call.Succeeded {
			t.Fatalf("failed call = %+v, %v", call, err)
		}
	})
}
