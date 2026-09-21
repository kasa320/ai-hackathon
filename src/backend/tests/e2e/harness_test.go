package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/devapi"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/notify"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading/toc"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	bg = context.Background()
	// t0 はテストの開始時刻（固定）。
	t0    = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// デモの4人（devapi の replan_demo と同じ Discord ID）。
var persona = map[string]string{
	"A": "100000000000000001",
	"B": "100000000000000002",
	"C": "100000000000000003",
	"D": "100000000000000004",
}

// fakeProvider は Discord OAuth の代わり。code は "Discord ID:表示名"。
type fakeProvider struct{ base string }

func (p fakeProvider) AuthURL(state string, _ bool) string {
	return p.base + "/fake-discord/authorize?state=" + state
}
func (p fakeProvider) Exchange(_ context.Context, code string) (auth.Identity, error) {
	id, name, ok := strings.Cut(code, ":")
	if !ok {
		return auth.Identity{}, errors.New("invalid code")
	}
	return auth.Identity{DiscordUserID: id, DisplayName: name}, nil
}

// recordingSender は Discord へ送ったはずのメッセージを記録する。
type recordingSender struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (r *recordingSender) Send(_ context.Context, m notify.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recordingSender) all() []notify.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Message{}, r.msgs...)
}

// switchPlanner はテスト中に Planner を差し替えられるようにする。
type switchPlanner struct {
	mu   sync.Mutex
	next coord.Planner
}

func (s *switchPlanner) set(p coord.Planner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = p
}

func (s *switchPlanner) Plan(ctx context.Context, req coord.PlanRequest) (coord.Outcome, coord.Usage, error) {
	s.mu.Lock()
	p := s.next
	s.mu.Unlock()
	return p.Plan(ctx, req)
}

// 目次の取得の外部サービスの偽物。
type fakeBib struct{}

func (fakeBib) Lookup(_ context.Context, isbn string) (*toc.Book, error) {
	if isbn == tocISBN {
		pub := "サンプル出版"
		return &toc.Book{ISBN: isbn, Title: "サンプル技術書", Authors: []string{"山田太郎"}, Publisher: &pub}, nil
	}
	return nil, nil
}

type fakeSearch struct{}

func (fakeSearch) Search(context.Context, toc.Book) (toc.SearchResult, []coord.LLMCall, error) {
	return toc.SearchResult{Found: true, URLs: []string{"https://publisher.example/book"}, Entries: []toc.Entry{
		{Title: "第1章 はじめに", Level: 1}, {Title: "1.1 背景", Level: 2}, {Title: "第2章 設計", Level: 1},
	}}, []coord.LLMCall{{Model: "search-model", Currency: "unknown", Succeeded: true}}, nil
}

type fakeFetch struct{}

func (fakeFetch) Fetch(_ context.Context, u string) (string, error) {
	if u == "https://publisher.example/book" {
		return toc.HTMLText("<h1>目次</h1><ul><li>第１章　はじめに</li><li>1.1 背景</li><li>第2章 設計</li></ul>"), nil
	}
	return "", errors.New("not found")
}

type fakeReader struct{}

func (fakeReader) Read(context.Context, []toc.Image) (toc.ReadResult, []coord.LLMCall, error) {
	return toc.ReadResult{Entries: []toc.Entry{{Title: "第1章 画像から", Level: 1}}, UnreadableCount: 1},
		[]coord.LLMCall{{Model: "vision-model", Currency: "unknown", Succeeded: true}}, nil
}

const tocISBN = "9784297127831"

// server は本番と同じ部品を組み立てたテスト用サーバー。
type server struct {
	t        *testing.T
	dbPath   string
	st       *store.Store
	clk      *clock.Offset
	faults   *fault.Registry
	planner  *switchPlanner
	sender   *recordingSender
	coord    *coord.Coordinator
	dispatch *notify.Dispatcher
	toc      *toc.Service
	http     *httptest.Server
	handler  *swapHandler
}

// swapHandler は再起動時にハンドラーだけを差し替え、URL（Cookie の送り先）を保つ。
type swapHandler struct {
	mu sync.RWMutex
	h  http.Handler
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.h
	s.mu.RUnlock()
	h.ServeHTTP(w, r)
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{
		t:       t,
		dbPath:  filepath.Join(t.TempDir(), "app.db"),
		clk:     clock.NewOffset(clock.Fixed{T: t0}),
		planner: &switchPlanner{next: coord.DraftOnlyPlanner{}},
		sender:  &recordingSender{},
		handler: &swapHandler{h: http.NotFoundHandler()},
	}
	s.http = httptest.NewServer(s.handler)
	t.Cleanup(func() {
		s.http.Close()
		if s.st != nil {
			s.st.Close()
		}
	})
	s.boot()
	return s
}

// boot は起動処理（cmd/server と同じ組み立て）を行う。再起動のテストでも使う。
func (s *server) boot() {
	t := s.t
	st, err := store.Open(bg, s.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s.st = st
	s.faults = fault.New()
	s.faults.Define("llm", "error", "invalid_output")
	s.faults.Define("notify", "fail", "unknown")
	toc.DefineFaults(s.faults)

	registry, err := coord.NewService(reading.New())
	if err != nil {
		t.Fatal(err)
	}
	runLock := &sync.Mutex{}
	base := s.http.URL
	s.coord = coord.NewCoordinator(registry, st, s.clk, agent.WithFaults(s.planner, s.faults), coord.Options{PublicBaseURL: base, Log: quiet, RunLock: runLock, Rand: func() float64 { return 0.5 },
		Interpreter: agent.WithInterpretFaults(coord.DraftOnlyInterpreter{}, s.faults)})
	s.dispatch = notify.NewDispatcher(st, s.clk, notify.WithFaults(s.sender, s.faults), quiet)
	if err := s.dispatch.Recover(bg); err != nil {
		t.Fatal(err)
	}
	s.toc = toc.NewService(toc.Deps{Store: st, Clock: s.clk, Faults: s.faults, Bib: fakeBib{}, Searcher: fakeSearch{}, Fetcher: fakeFetch{}, Reader: fakeReader{}, Log: quiet})
	s.toc.Async = func(f func()) { f() }
	if err := s.toc.Recover(bg); err != nil {
		t.Fatal(err)
	}
	am := auth.NewManager(st, s.clk, fakeProvider{base: base}, "test-secret", false)
	srv := api.New(api.Deps{DB: st, Clock: s.clk, Log: quiet, Coord: s.coord, Auth: am, AllowedOrigins: []string{base}, DevMode: true,
		Extensions: []httpx.Extension{toc.NewExtension(s.toc)}})
	h := srv.Handler(t.TempDir(), devapi.Mount(devapi.Deps{Store: st, Clock: s.clk, Auth: am, Faults: s.faults, AllowedOrigins: []string{base}, Log: quiet,
		Seeder: devapi.NewSeeder(registry, st, s.clk, base, runLock), Wake: s.coord.Wake}))
	s.handler.mu.Lock()
	s.handler.h = h
	s.handler.mu.Unlock()
}

// restart はプロセスの再起動を模す。DB ファイルと時計は引き継ぎ、メモリ上の状態は捨てる。
func (s *server) restart() {
	s.st.Close()
	s.boot()
}

// process は保存済みのイベント・目次の取得・通知待ちを、進むものがなくなるまで処理する。
func (s *server) process() {
	s.t.Helper()
	for i := 0; i < 20; i++ {
		n1, err := s.coord.ProcessDue(bg)
		if err != nil {
			s.t.Fatal(err)
		}
		n2, err := s.toc.ProcessDue(bg)
		if err != nil {
			s.t.Fatal(err)
		}
		n3, err := s.dispatch.DispatchPending(bg)
		if err != nil {
			s.t.Fatal(err)
		}
		if n1+n2+n3 == 0 {
			return
		}
	}
}

// advance は時計を進めてから処理する。
func (s *server) advance(d time.Duration) {
	s.clk.Advance(d)
	s.process()
}

// client は1人の利用者のブラウザー（Cookie・CSRF トークン）を表す。
type client struct {
	t    *testing.T
	srv  *server
	http *http.Client
	csrf string
}

func (s *server) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: s.t, srv: s, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func newKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type response struct {
	status int
	body   []byte
	header http.Header
}

func (r response) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("JSON を読めない（%d）: %s", r.status, r.body)
	}
}

// errorCode はエラー応答の code を返す。形式が docs/api-endpoint.md と違えば失敗させる。
func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var e struct {
		Error *struct {
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			Details   json.RawMessage `json:"details"`
			RequestID string          `json:"request_id"`
		} `json:"error"`
	}
	r.decode(t, &e)
	if e.Error == nil || e.Error.Code == "" || e.Error.Message == "" || e.Error.RequestID == "" || !bytes.HasPrefix(e.Error.Details, []byte("{")) {
		t.Fatalf("エラー形式が契約と違う: %s", r.body)
	}
	return e.Error.Code
}

func (c *client) do(method, path string, body []byte, hdr map[string]string) response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.srv.http.URL+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Origin", c.srv.http.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return response{status: res.StatusCode, body: b, header: res.Header}
}

func jsonBody(v any) []byte {
	if raw, ok := v.(string); ok {
		return []byte(raw)
	}
	b, _ := json.Marshal(v)
	return b
}

func (c *client) get(path string) response { return c.do(http.MethodGet, path, nil, nil) }

// send は CSRF トークンと新しい Idempotency-Key を付けて更新を送る。
func (c *client) send(method, path string, body any) response {
	return c.sendKey(method, path, newKey(), body)
}

func (c *client) sendKey(method, path, key string, body any) response {
	return c.do(method, path, jsonBody(body), map[string]string{"X-CSRF-Token": c.csrf, "Idempotency-Key": key})
}

// mustStatus は期待したステータスでなければ失敗させる。
func (r response) mustStatus(t *testing.T, want int) response {
	t.Helper()
	if r.status != want {
		t.Fatalf("status = %d, want %d: %s", r.status, want, r.body)
	}
	return r
}

func (c *client) refreshCSRF() {
	c.t.Helper()
	var me api.MeResponse
	c.get("/api/me").mustStatus(c.t, 200).decode(c.t, &me)
	c.csrf = me.CSRFToken
}

// devLogin は /api/dev/login で人物を切り替える。
func (c *client) devLogin(name string) *client {
	c.t.Helper()
	var me api.MeResponse
	c.do(http.MethodPost, "/api/dev/login", jsonBody(map[string]string{"discord_user_id": persona[name]}), nil).mustStatus(c.t, 200).decode(c.t, &me)
	c.csrf = me.CSRFToken
	return c
}

// oauthLogin はブラウザーと同じ手順で Discord ログインを行い、最終的な遷移先を返す。
func (c *client) oauthLogin(discordID, name, returnTo string) string {
	c.t.Helper()
	res := c.get("/api/auth/discord?return_to="+url.QueryEscape(returnTo)).mustStatus(c.t, http.StatusFound)
	loc, _ := url.Parse(res.header.Get("Location"))
	state := loc.Query().Get("state")
	res = c.get("/api/auth/callback?state="+state+"&code="+url.QueryEscape(discordID+":"+name)).mustStatus(c.t, http.StatusSeeOther)
	if !strings.HasPrefix(res.header.Get("Location"), "/?auth_error") {
		c.refreshCSRF()
	}
	return res.header.Get("Location")
}

// seed は開発用 API で初期データを投入する。
func (s *server) seed(scenario string) devapi.SeedResult {
	s.t.Helper()
	var out devapi.SeedResult
	s.client().do(http.MethodPost, "/api/dev/seed", jsonBody(map[string]string{"scenario": scenario}), nil).mustStatus(s.t, 200).decode(s.t, &out)
	return out
}

// personas は A〜D のログイン済みクライアントを返す。
func (s *server) personas() map[string]*client {
	out := map[string]*client{}
	for name := range persona {
		out[name] = s.client().devLogin(name)
	}
	return out
}

func (c *client) detail(sessionID string) apitypes.SessionDetail {
	c.t.Helper()
	var d apitypes.SessionDetail
	c.get("/api/sessions/"+sessionID).mustStatus(c.t, 200).decode(c.t, &d)
	return d
}

func (c *client) openTask(sessionID, kind string) *apitypes.Task {
	for _, tk := range c.detail(sessionID).MyTasks {
		if tk.Kind == kind && tk.Status == "open" {
			tk := tk
			return &tk
		}
	}
	return nil
}

// respond は本人宛ての open なタスクに回答する。
func (c *client) respond(sessionID, kind, decision string) response {
	c.t.Helper()
	tk := c.openTask(sessionID, kind)
	if tk == nil {
		c.t.Fatalf("open な %s タスクがない", kind)
	}
	return c.send(http.MethodPost, "/api/tasks/"+tk.ID+"/responses", map[string]any{"decision": decision, "proposal_id": tk.ProposalID, "proposal_version": tk.ProposalVersion})
}

// submitPreparation は本人宛ての preparation タスクへ参加条件を回答する。
func (c *client) submitPreparation(sessionID string, attendance string, data map[string]any) response {
	c.t.Helper()
	tk := c.openTask(sessionID, "preparation")
	if tk == nil {
		c.t.Fatal("open な preparation タスクがない")
	}
	rev := c.detail(sessionID).Session.Revision
	return c.send(http.MethodPost, "/api/tasks/"+tk.ID+"/responses", map[string]any{
		"decision": "submit", "expected_revision": rev, "preparation": map[string]any{"attendance": attendance, "data": data},
	})
}

func (c *client) withdraw(sessionID, scope string) response {
	rev := c.detail(sessionID).Session.Revision
	return c.send(http.MethodPost, "/api/sessions/"+sessionID+"/withdrawals", map[string]any{"expected_revision": rev, "scope": scope})
}

// prepData は参加条件。declined なら今回の説明の担当を辞退している。
func prepData(declined bool) map[string]any {
	return map[string]any{"declined_presentation": declined}
}

// memberID は表示名からメンバーIDを引く。
func memberID(t *testing.T, d apitypes.SessionDetail, name string) string {
	t.Helper()
	for _, m := range d.Members {
		if m.DisplayName == name {
			return m.ID
		}
	}
	t.Fatalf("メンバー %s がいない", name)
	return ""
}

func (s *server) setFaults(v map[string]any) {
	s.t.Helper()
	s.client().do(http.MethodPut, "/api/dev/faults", jsonBody(v), nil).mustStatus(s.t, 200)
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return fmt.Sprint(string(b))
}
