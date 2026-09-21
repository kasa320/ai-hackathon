package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
)

type fakeProvider struct{ id auth.Identity }

func (f fakeProvider) AuthURL(state string, _ bool) string {
	return "https://discord.test/authorize?state=" + state
}
func (f fakeProvider) Exchange(_ context.Context, code string) (auth.Identity, error) {
	if code != "good" {
		return auth.Identity{}, errors.New("bad code")
	}
	return f.id, nil
}

func setup(t *testing.T) (*auth.Manager, *store.Store, *clock.Offset) {
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.NewOffset(clock.Fixed{T: t0})
	return auth.NewManager(st, clk, fakeProvider{id: auth.Identity{DiscordUserID: "222222222222222222", DisplayName: "B本人"}}, "secret", false), st, clk
}

// login は OAuth の開始から callback までをブラウザーと同じ手順で行い、遷移先と Cookie を返す。
func login(t *testing.T, m *auth.Manager, returnTo, code string, tamper bool) (string, []*http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	u, err := m.StartLogin(ctx, w, httptest.NewRequest(http.MethodGet, "/api/auth/discord", nil), returnTo)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	state := parsed.Query().Get("state")
	if tamper {
		state = "forged"
	}
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code="+code+"&state="+state, nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	w2 := httptest.NewRecorder()
	loc := m.Callback(ctx, w2, r)
	return loc, w2.Result().Cookies()
}

func sessionCookie(cs []*http.Cookie) *http.Cookie {
	for _, c := range cs {
		if c.Name == auth.CookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginFlowActivatesInvitation(t *testing.T) {
	m, st, _ := setup(t)
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		owner, _ := tx.UpsertUser(ctx, "111111111111111111", "A", t0)
		_ = tx.CreateGroup(ctx, store.Group{ID: "g", Name: "g", OwnerUserID: owner.ID, CreatedAt: t0})
		return tx.AddMember(ctx, store.Member{ID: "mem_b", GroupID: "g", DiscordUserID: "222222222222222222", DisplayName: "仮B", Role: "member"}, 1)
	})
	loc, cookies := login(t, m, "/session.html?id=ses_1", "good", false)
	if loc != "/session.html?id=ses_1" {
		t.Fatalf("戻り先 = %q", loc)
	}
	c := sessionCookie(cookies)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != int(auth.SessionTTL.Seconds()) {
		t.Fatalf("Cookie の属性: %+v", c)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.AddCookie(c)
	s, err := m.Authenticate(ctx, r)
	if err != nil || s.User.DisplayName != "B本人" || s.CSRFToken == "" {
		t.Fatalf("session = %+v, %v", s, err)
	}
	_ = st.Tx(ctx, func(tx *store.Tx) error {
		mem, _ := tx.Member(ctx, "mem_b")
		if !mem.Joined() || mem.DisplayName != "B本人" {
			t.Fatalf("招待がログインで有効になっていない: %+v", mem)
		}
		return nil
	})
}

func TestCallbackFailures(t *testing.T) {
	m, _, _ := setup(t)
	if loc, cookies := login(t, m, "/", "good", true); loc != "/?auth_error=invalid_state" || sessionCookie(cookies) != nil {
		t.Fatalf("偽の state: %q", loc)
	}
	if loc, _ := login(t, m, "/", "bad", false); loc != "/?auth_error=provider_unavailable" {
		t.Fatalf("交換失敗: %q", loc)
	}
	// state の Cookie がない（別のブラウザーから開いた）場合も拒否する。
	w := httptest.NewRecorder()
	u, _ := m.StartLogin(ctx, w, httptest.NewRequest(http.MethodGet, "/api/auth/discord", nil), "/")
	parsed, _ := url.Parse(u)
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=good&state="+parsed.Query().Get("state"), nil)
	if loc := m.Callback(ctx, httptest.NewRecorder(), r); loc != "/?auth_error=invalid_state" {
		t.Fatalf("Cookie なし: %q", loc)
	}
	// 利用者が拒否した場合。
	w = httptest.NewRecorder()
	u, _ = m.StartLogin(ctx, w, httptest.NewRequest(http.MethodGet, "/api/auth/discord", nil), "/")
	parsed, _ = url.Parse(u)
	r = httptest.NewRequest(http.MethodGet, "/api/auth/callback?error=access_denied&state="+parsed.Query().Get("state"), nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	if loc := m.Callback(ctx, httptest.NewRecorder(), r); loc != "/?auth_error=access_denied" {
		t.Fatalf("拒否: %q", loc)
	}
}

func TestSanitizeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/session.html?id=x": "/session.html?id=x",
		"//evil.example":     "/",
		"https://evil":       "/",
		"/\\evil":            "/",
		"relative":           "/",
		"":                   "/",
	} {
		if got := auth.SanitizeReturnTo(in); got != want {
			t.Errorf("SanitizeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionExpiryAndLogout(t *testing.T) {
	m, _, clk := setup(t)
	_, cookies := login(t, m, "/", "good", false)
	c := sessionCookie(cookies)
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(c)
		return r
	}
	s, err := m.Authenticate(ctx, req())
	if err != nil {
		t.Fatal(err)
	}
	r := req()
	r.Header.Set("X-CSRF-Token", s.CSRFToken)
	if !s.CheckCSRF(r) || s.CheckCSRF(req()) {
		t.Fatal("CSRF トークンの照合")
	}
	if err := m.Logout(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, req()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("ログアウト後はサーバー側でも無効")
	}

	_, cookies = login(t, m, "/", "good", false)
	c = sessionCookie(cookies)
	clk.Advance(auth.SessionTTL + time.Second)
	if _, err := m.Authenticate(ctx, req()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("使わないまま期限を過ぎたら期限切れ")
	}
}

// 使っている間はログインが続く。期限は1日に1回延ばし、そのときだけ Cookie を出し直す。
func TestSessionSlidesWhileUsed(t *testing.T) {
	m, _, clk := setup(t)
	_, cookies := login(t, m, "/", "good", false)
	c := sessionCookie(cookies)
	if c.MaxAge != int(auth.SessionTTL.Seconds()) {
		t.Fatalf("Cookie の期限: %d", c.MaxAge)
	}
	use := func() http.Header {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(c)
		s, err := m.Authenticate(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		m.RefreshCookie(w, s)
		return w.Header()
	}

	if h := use(); h.Get("Set-Cookie") != "" {
		t.Fatal("ログイン直後は延ばさない")
	}
	// 20日ごとに使えば、最初の期限（30日）を過ぎてもログインが続く
	for i := 0; i < 5; i++ {
		clk.Advance(20 * 24 * time.Hour)
		h := use()
		if !strings.Contains(h.Get("Set-Cookie"), "Max-Age="+strconv.Itoa(int(auth.SessionTTL.Seconds()))) {
			t.Fatalf("%d回目：Cookie を出し直していない: %q", i, h.Get("Set-Cookie"))
		}
	}
	if h := use(); h.Get("Set-Cookie") != "" {
		t.Fatal("同じ日のうちは延ばさない")
	}
}

func TestProviderUnavailableWhenNotConfigured(t *testing.T) {
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := auth.NewManager(st, clock.Fixed{T: t0}, nil, "s", false)
	u, err := m.StartLogin(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/auth/discord", nil), "/")
	if err != nil || !strings.Contains(u, "provider_unavailable") {
		t.Fatalf("未設定なら provider_unavailable: %q %v", u, err)
	}
}

func TestDiscordAuthURL(t *testing.T) {
	p := auth.NewDiscordProvider("cid", "secret", "http://localhost:24680/api/auth/callback")
	u, _ := url.Parse(p.AuthURL("st", false))
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("scope") != "identify" || q.Get("state") != "st" || q.Get("prompt") != "none" || strings.Contains(u.String(), "secret") {
		t.Fatalf("auth url = %s", u)
	}
	// ログアウト後は確認画面を出し、別のアカウントを選べるようにする
	u, _ = url.Parse(p.AuthURL("st", true))
	if u.Query().Get("prompt") != "consent" {
		t.Fatalf("アカウントを選ぶ auth url = %s", u)
	}
}

// promptProvider は確認画面を出すよう求められたかを URL に残す。
type promptProvider struct{ fakeProvider }

func (p promptProvider) AuthURL(state string, chooseAccount bool) string {
	return fmt.Sprintf("https://discord.test/authorize?state=%s&choose=%t", state, chooseAccount)
}

// ログアウト後の最初のログインだけ Discord の確認画面を出し、別のアカウントを選べるようにする。
func TestChooseAccountAfterLogout(t *testing.T) {
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := auth.NewManager(st, clock.Fixed{T: t0}, promptProvider{fakeProvider{id: auth.Identity{DiscordUserID: "222222222222222222", DisplayName: "B"}}}, "s", false)
	start := func(cookies []*http.Cookie) (string, []*http.Cookie) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/auth/discord", nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		u, err := m.StartLogin(ctx, w, r, "/")
		if err != nil {
			t.Fatal(err)
		}
		return u, w.Result().Cookies()
	}
	if u, _ := start(nil); !strings.Contains(u, "choose=false") {
		t.Fatalf("初回は確認画面を省く: %s", u)
	}

	w := httptest.NewRecorder()
	m.MarkLoggedOut(w)
	loggedOut := w.Result().Cookies()
	u, stateCookies := start(loggedOut)
	if !strings.Contains(u, "choose=true") {
		t.Fatalf("ログアウト後は確認画面を出す: %s", u)
	}

	// ログインが済んだら印を消す
	parsed, _ := url.Parse(u)
	r := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=good&state="+parsed.Query().Get("state"), nil)
	for _, c := range append(stateCookies, loggedOut...) {
		r.AddCookie(c)
	}
	w = httptest.NewRecorder()
	if loc := m.Callback(ctx, w, r); strings.Contains(loc, "auth_error") {
		t.Fatalf("ログイン失敗: %s", loc)
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "auth_prompt" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("ログイン後に印を消していない")
	}
}
