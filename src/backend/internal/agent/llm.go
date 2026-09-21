package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// Client は OpenAI 互換の Chat Completions API（OrcaRouter）を呼ぶ最小限のクライアント。
// SDK の自動再試行は使わず、呼び出し回数と費用はアプリ側で数える。
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// CostCurrency は応答の usage.cost の通貨。分からなければ空（unknown として記録）。
	CostCurrency string
}

// NewClient を作る。timeout は1回の呼び出しの上限で、0以下なら DefaultTimeout を使う。
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: timeout}}
}

// DefaultTimeout は呼び出し1回の既定の上限。推論の重いモデルは1回に90秒前後かかることがある。
const DefaultTimeout = 180 * time.Second

type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ChatRequest struct {
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice any       `json:"tool_choice,omitempty"`
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content   *string    `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		Cost             *float64 `json:"cost"`
	} `json:"usage"`
}

// Chat は1回の呼び出しを行い、応答と呼び出しの記録を返す。
// 通信エラー・タイムアウト・429・5xx は coord.ErrTransient を包んで返す。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, coord.LLMCall, error) {
	call := coord.LLMCall{Model: req.Model, Currency: "unknown"}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, call, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, call, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(hreq)
	if err != nil {
		return nil, call, fmt.Errorf("%w: %v", coord.ErrTransient, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, call, fmt.Errorf("%w: %v", coord.ErrTransient, err)
	}
	switch {
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return nil, call, fmt.Errorf("%w: status %d", coord.ErrTransient, res.StatusCode)
	case res.StatusCode >= 400:
		// 認証エラーなど、再試行しても直らない失敗。応答本文はログにも残さない。
		return nil, call, fmt.Errorf("agent: LLM API status %d", res.StatusCode)
	}
	readRoutingHeaders(res.Header, &call)
	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, call, fmt.Errorf("%w: 応答を読めません", ErrMalformedResponse)
	}
	call.Succeeded = true
	c.fetchGeneration(ctx, &call)
	if u := out.Usage; u != nil {
		call.InputTokens, call.OutputTokens = u.PromptTokens, u.CompletionTokens
		if u.Cost != nil {
			s := strconv.FormatFloat(*u.Cost, 'f', -1, 64)
			call.EstimatedAmount = &s
			if c.CostCurrency != "" {
				call.Currency = c.CostCurrency
			}
		}
	}
	return &out, call, nil
}

// ErrMalformedResponse は API の応答自体が読めないこと。
var ErrMalformedResponse = errors.New("agent: malformed LLM response")

// fetchGeneration は確定した費用と所要時間を GET /generation から取りに行く。
// chat/completions の usage には cost が含まれない（2026-09-21 実測）ため、
// 金額はこの呼び出しでしか分からない。
//
// 取得できなければ記録を変えずに戻る。費用不明を 0 や推定値で埋めないため、
// 失敗しても呼び出し元にはエラーを返さない（本処理を止める理由にならない）。
func (c *Client) fetchGeneration(ctx context.Context, call *coord.LLMCall) {
	if call.RequestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/generation?id="+url.QueryEscape(call.RequestID), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return
	}
	var out struct {
		Data struct {
			Model        string   `json:"model"`
			TotalCost    *float64 `json:"total_cost"`
			CostCurrency string   `json:"cost_currency"`
			LatencyMS    *int     `json:"latency_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return
	}
	d := out.Data
	if d.TotalCost != nil {
		s := strconv.FormatFloat(*d.TotalCost, 'f', -1, 64)
		// 確定した請求額。見積（estimated_amount）とは別の列に入れる。
		call.BilledAmount = &s
		if d.CostCurrency != "" {
			call.Currency = d.CostCurrency
		}
	}
	if d.LatencyMS != nil {
		call.LatencyMS = d.LatencyMS
	}
	// ヘッダーから解決モデルが取れなかった場合の補完。
	if call.ResolvedModel == "" {
		call.ResolvedModel = d.Model
	}
}

// readRoutingHeaders は OrcaRouter が返す振り分け結果を記録に移す。
// model にルーター名（orcarouter/...）を指定した場合、実際に答えたモデルはここでしか分からない。
// ヘッダーが無いプロバイダもあるため、取れなければ空・nil のままにして Model で代用しない。
func readRoutingHeaders(h http.Header, call *coord.LLMCall) {
	call.RequestID = h.Get("X-Orca-Request-Id")
	call.ResolvedModel = h.Get("X-Orca-Resolved-Model")
	// 受け皿が発動したときは、実際に答えたモデルがこちらに入る。
	if m := h.Get("X-Orca-Fallback-Model"); m != "" {
		call.ResolvedModel = m
	}
	if v := h.Get("X-Orca-Fallback-Level"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			call.FallbackLevel = &n
		}
	}
	// 実測では X-Orca-Fallback-Level は返らず、X-Orca-Route に
	// 「model=...; by=balanced; class=chat; fallback=0」の形で入っていた（2026-09-21 確認）。
	// 専用ヘッダーが無いときだけこちらから読む。
	for _, part := range strings.Split(h.Get("X-Orca-Route"), ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "fallback":
			if n, err := strconv.Atoi(v); err == nil && call.FallbackLevel == nil {
				call.FallbackLevel = &n
			}
		case "model":
			if call.ResolvedModel == "" {
				call.ResolvedModel = v
			}
		}
	}
}
