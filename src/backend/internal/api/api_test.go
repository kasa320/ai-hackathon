package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var ctx = context.Background()

type env struct {
	h      http.Handler
	cookie *http.Cookie
	csrf   string
}

func setup(t *testing.T) env {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.Fixed{T: time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)}
	reg, _ := coord.NewService(reading.New())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := coord.NewCoordinator(reg, st, clk, coord.DraftOnlyPlanner{}, coord.Options{PublicBaseURL: "http://app.test"})
	am := auth.NewManager(st, clk, nil, "secret", false)
	token, err := am.Login(ctx, auth.Identity{DiscordUserID: "111111111111111111", DisplayName: "A"})
	if err != nil {
		t.Fatal(err)
	}
	srv := api.New(api.Deps{DB: st, Clock: clk, Log: log, Coord: c, Auth: am, AllowedOrigins: []string{"http://app.test"}})
	e := env{h: srv.Handler(t.TempDir()), cookie: &http.Cookie{Name: auth.CookieName, Value: token}}
	res := e.do(t, "GET", "/api/me", "", nil)
	var me api.MeResponse
	_ = json.Unmarshal([]byte(res.Body.String()), &me)
	e.csrf = me.CSRFToken
	return e
}

func (e env) do(t *testing.T, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "app.test"
	if e.cookie != nil {
		r.AddCookie(e.cookie)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code      string         `json:"code"`
			Details   map[string]any `json:"details"`
			RequestID string         `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("エラーが JSON でない: %s", w.Body.String())
	}
	if body.Error.Details == nil || body.Error.RequestID == "" {
		t.Fatalf("details と request_id が必要: %s", w.Body.String())
	}
	return body.Error.Code
}

func TestPublicEndpoints(t *testing.T) {
	e := setup(t)
	w := e.do(t, "GET", "/api/health", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dev_mode":false`) || !strings.Contains(w.Body.String(), `"now":"2026-09-19T09:00:00Z"`) {
		t.Fatalf("health: %d %s", w.Code, w.Body)
	}
	w = e.do(t, "GET", "/api/playbooks", "", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"playbooks":[{"id":"reading","name":"輪読"}]}` {
		t.Fatalf("playbooks: %s", w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("API は Cache-Control: no-store")
	}
}

func TestErrorContract(t *testing.T) {
	e := setup(t)
	anon := env{h: e.h}
	if w := anon.do(t, "GET", "/api/me", "", nil); w.Code != 401 || errCode(t, w) != "unauthenticated" {
		t.Fatalf("未ログイン: %d", w.Code)
	}
	if w := e.do(t, "GET", "/api/nothing", "", nil); w.Code != 404 || errCode(t, w) != "not_found" {
		t.Fatalf("未定義の API: %d", w.Code)
	}
	if w := e.do(t, "DELETE", "/api/groups", "", nil); w.Code != 405 || errCode(t, w) != "method_not_allowed" || !strings.Contains(w.Header().Get("Allow"), "POST") {
		t.Fatalf("メソッド違い: %d %v", w.Code, w.Header())
	}
	body := `{"name":"g","invitees":[{"discord_user_id":"222222222222222222","display_name":"B"}]}`
	ok := map[string]string{"Content-Type": "application/json", "X-CSRF-Token": e.csrf, "Idempotency-Key": "k1", "Origin": "http://app.test"}
	with := func(k, v string) map[string]string {
		h := map[string]string{}
		for a, b := range ok {
			h[a] = b
		}
		if v == "" {
			delete(h, k)
		} else {
			h[k] = v
		}
		return h
	}
	cases := []struct {
		name   string
		hdr    map[string]string
		body   string
		status int
		code   string
	}{
		{"CSRFなし", with("X-CSRF-Token", ""), body, 403, "csrf_invalid"},
		{"CSRF違い", with("X-CSRF-Token", "x"), body, 403, "csrf_invalid"},
		{"別オリジン", with("Origin", "http://evil.test"), body, 403, "forbidden"},
		{"再送キーなし", with("Idempotency-Key", ""), body, 400, "idempotency_key_required"},
		{"Content-Type違い", with("Content-Type", "text/plain"), body, 415, "unsupported_media_type"},
		{"未知のフィールド", ok, `{"name":"g","invitees":[],"owner_id":"x"}`, 400, "invalid_json"},
		{"JSON不正", ok, `{`, 400, "invalid_json"},
		{"大きすぎる", ok, `{"name":"` + strings.Repeat("a", 70<<10) + `"}`, 413, "payload_too_large"},
		{"入力の検証", with("Idempotency-Key", "k2"), `{"name":"","invitees":[]}`, 422, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(t, "POST", "/api/groups", tc.body, tc.hdr)
			if w.Code != tc.status || errCode(t, w) != tc.code {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
		})
	}

	w := e.do(t, "POST", "/api/groups", body, ok)
	if w.Code != 201 || !strings.HasPrefix(w.Header().Get("Location"), "/api/groups/grp_") {
		t.Fatalf("作成: %d %s", w.Code, w.Body)
	}
	// 同じキー・同じ本文の再送は最初の成功を返す。別の本文なら 409。
	again := e.do(t, "POST", "/api/groups", body, ok)
	if again.Code != 201 || again.Body.String() != w.Body.String() || again.Header().Get("Location") != w.Header().Get("Location") {
		t.Fatalf("再送: %d %s", again.Code, again.Body)
	}
	if w := e.do(t, "POST", "/api/groups", strings.Replace(body, `"g"`, `"h"`, 1), ok); w.Code != 409 || errCode(t, w) != "idempotency_key_reused" {
		t.Fatalf("キーの再利用: %d", w.Code)
	}
	// ログアウトは CSRF 必須・再送キー不要。
	if w := e.do(t, "POST", "/api/auth/logout", "", map[string]string{"X-CSRF-Token": e.csrf}); w.Code != 204 {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if w := e.do(t, "GET", "/api/me", "", nil); w.Code != 401 {
		t.Fatal("ログアウト後は 401")
	}
}

func TestOAuthRedirects(t *testing.T) {
	e := setup(t)
	w := e.do(t, "GET", "/api/auth/discord?return_to=/session.html?id=x", "", nil)
	if w.Code != 302 || w.Header().Get("Location") != "/?auth_error=provider_unavailable" {
		t.Fatalf("Discord 未設定: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = e.do(t, "GET", "/api/auth/callback?state=x&code=y", "", nil)
	if w.Code != 303 || w.Header().Get("Location") != "/?auth_error=invalid_state" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
}
