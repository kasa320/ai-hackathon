// Package auth は Discord OAuth によるログインとサーバーセッションを扱う。
// Cookie には推測できないトークンだけを入れ、DB にはそのハッシュを保存する。
// Discord のアクセストークンは本人確認にだけ使い、保存もフロントへの返却もしない。
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

const (
	CookieName      = "session"
	stateCookieName = "oauth_state"
	SessionTTL      = 7 * 24 * time.Hour
	stateTTL        = 10 * time.Minute
)

// OAuth の失敗理由（callback の auth_error）。
const (
	ErrAccessDenied        = "access_denied"
	ErrInvalidState        = "invalid_state"
	ErrProviderUnavailable = "provider_unavailable"
)

// Identity は Discord で確認した本人の情報。
type Identity struct {
	DiscordUserID string
	DisplayName   string
}

// Provider は OAuth の提供元。
type Provider interface {
	AuthURL(state string) string
	Exchange(ctx context.Context, code string) (Identity, error)
}

// Manager はログイン・セッション・CSRF トークンを管理する。
type Manager struct {
	st       *store.Store
	clock    clock.Clock
	provider Provider // nil なら Discord 未設定
	secret   string
	secure   bool
}

// NewManager を作る。secure=true なら Cookie に Secure を付ける（本番 HTTPS）。
func NewManager(st *store.Store, clk clock.Clock, provider Provider, secret string, secure bool) *Manager {
	return &Manager{st: st, clock: clk, provider: provider, secret: secret, secure: secure}
}

func (m *Manager) now() time.Time { return m.clock.Now().UTC() }

func (m *Manager) hash(token string) string {
	sum := sha256.Sum256([]byte(m.secret + ":" + token))
	return hex.EncodeToString(sum[:])
}

// SanitizeReturnTo は同一サイト内の相対パス（/ で始まり // で始まらない）だけを受け付け、それ以外は / にする。
func SanitizeReturnTo(s string) string {
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") || strings.ContainsAny(s, "\r\n") {
		return "/"
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return s
}

// StartLogin は OAuth の state を保存し、Discord の認可画面の URL を返す。state は Cookie にも結び付ける。
func (m *Manager) StartLogin(ctx context.Context, w http.ResponseWriter, returnTo string) (string, error) {
	if m.provider == nil {
		return "/?auth_error=" + ErrProviderUnavailable, nil
	}
	state := store.RandomToken()
	err := m.st.Tx(ctx, func(tx *store.Tx) error {
		return tx.CreateOAuthState(ctx, state, SanitizeReturnTo(returnTo), m.now().Add(stateTTL))
	})
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: state, Path: "/api/auth/", HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(stateTTL.Seconds())})
	return m.provider.AuthURL(state), nil
}

// Callback は state を検証し、コードを交換して本人を確認し、セッションを発行する。
// 戻り値は遷移先。失敗時は /?auth_error=... を返し、詳細や外部トークンを URL に含めない。
func (m *Manager) Callback(ctx context.Context, w http.ResponseWriter, r *http.Request) string {
	q := r.URL.Query()
	state := q.Get("state")
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/api/auth/", HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	cookie, err := r.Cookie(stateCookieName)
	if state == "" || err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		return "/?auth_error=" + ErrInvalidState
	}
	var returnTo string
	err = m.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		returnTo, err = tx.ConsumeOAuthState(ctx, state, m.now())
		return err
	})
	if err != nil {
		return "/?auth_error=" + ErrInvalidState
	}
	if q.Get("error") != "" {
		return "/?auth_error=" + ErrAccessDenied
	}
	if m.provider == nil || q.Get("code") == "" {
		return "/?auth_error=" + ErrProviderUnavailable
	}
	id, err := m.provider.Exchange(ctx, q.Get("code"))
	if err != nil {
		return "/?auth_error=" + ErrProviderUnavailable
	}
	token, err := m.Login(ctx, id)
	if err != nil {
		return "/?auth_error=" + ErrProviderUnavailable
	}
	m.SetCookie(w, token)
	return returnTo
}

// Login は本人確認済みの利用者を登録し、招待中の所属を有効にしてセッショントークンを返す。
func (m *Manager) Login(ctx context.Context, id Identity) (string, error) {
	var token string
	err := m.st.Tx(ctx, func(tx *store.Tx) error {
		u, err := tx.UpsertUser(ctx, id.DiscordUserID, id.DisplayName, m.now())
		if err != nil {
			return err
		}
		if err := tx.ActivateMemberships(ctx, u); err != nil {
			return err
		}
		token, err = m.issue(ctx, tx, u.ID)
		return err
	})
	return token, err
}

// IssueExisting は登録済みの利用者にセッションを発行する（開発用の人物切り替えで使う）。
func (m *Manager) IssueExisting(ctx context.Context, discordUserID string) (string, store.User, error) {
	var token string
	var u store.User
	err := m.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		if u, err = tx.UserByDiscordID(ctx, discordUserID); err != nil {
			return err
		}
		token, err = m.issue(ctx, tx, u.ID)
		return err
	})
	return token, u, err
}

func (m *Manager) issue(ctx context.Context, tx *store.Tx, userID string) (string, error) {
	token := store.RandomToken()
	now := m.now()
	return token, tx.CreateAuthSession(ctx, store.AuthSession{TokenHash: m.hash(token), UserID: userID, CSRFToken: store.RandomToken(), CreatedAt: now, ExpiresAt: now.Add(SessionTTL)})
}

// SetCookie はセッション Cookie を発行する。有効期限はログインから7日で固定。
func (m *Manager) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(SessionTTL.Seconds())})
}

func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// Session は認証済みのリクエストの本人情報。
type Session struct {
	User      store.User
	CSRFToken string
	tokenHash string
}

// ErrUnauthenticated は未ログイン・期限切れ・ログアウト済みを示す。
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// Authenticate は Cookie からセッションを解決する。
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return Session{}, ErrUnauthenticated
	}
	var s Session
	err = m.st.Tx(ctx, func(tx *store.Tx) error {
		as, err := tx.AuthSession(ctx, m.hash(c.Value))
		if err != nil {
			return err
		}
		if !m.now().Before(as.ExpiresAt) {
			_ = tx.DeleteAuthSession(ctx, as.TokenHash)
			return store.ErrNotFound
		}
		u, err := tx.User(ctx, as.UserID)
		if err != nil {
			return err
		}
		s = Session{User: u, CSRFToken: as.CSRFToken, tokenHash: as.TokenHash}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return Session{}, ErrUnauthenticated
	}
	return s, err
}

// CheckCSRF は X-CSRF-Token がセッションのトークンと一致するかを返す。
func (s Session) CheckCSRF(r *http.Request) bool {
	got := r.Header.Get("X-CSRF-Token")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRFToken)) == 1
}

// Logout はサーバー側のセッションも無効にする。
func (m *Manager) Logout(ctx context.Context, s Session) error {
	return m.st.Tx(ctx, func(tx *store.Tx) error { return tx.DeleteAuthSession(ctx, s.tokenHash) })
}

// DiscordProvider は Discord の OAuth2（identify スコープ）。
type DiscordProvider struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthBase     string
	APIBase      string
	HTTP         *http.Client
}

func NewDiscordProvider(clientID, clientSecret, redirectURL string) *DiscordProvider {
	return &DiscordProvider{
		ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		AuthBase: "https://discord.com/oauth2/authorize", APIBase: "https://discord.com/api/v10",
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *DiscordProvider) AuthURL(state string) string {
	v := url.Values{"client_id": {d.ClientID}, "redirect_uri": {d.RedirectURL}, "response_type": {"code"}, "scope": {"identify"}, "state": {state}, "prompt": {"none"}}
	return d.AuthBase + "?" + v.Encode()
}

func (d *DiscordProvider) Exchange(ctx context.Context, code string) (Identity, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {d.RedirectURL}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.APIBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.SetBasicAuth(d.ClientID, d.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := d.do(req, &tok); err != nil {
		return Identity{}, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, d.APIBase+"/users/@me", nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	var user struct {
		ID         string  `json:"id"`
		Username   string  `json:"username"`
		GlobalName *string `json:"global_name"`
	}
	if err := d.do(req, &user); err != nil {
		return Identity{}, err
	}
	name := user.Username
	if user.GlobalName != nil && strings.TrimSpace(*user.GlobalName) != "" {
		name = strings.TrimSpace(*user.GlobalName)
	}
	if utf8.RuneCountInString(name) > 100 {
		name = string([]rune(name)[:100])
	}
	if user.ID == "" {
		return Identity{}, fmt.Errorf("auth: Discord のユーザーIDがありません")
	}
	return Identity{DiscordUserID: user.ID, DisplayName: name}, nil
}

func (d *DiscordProvider) do(req *http.Request, out any) error {
	res, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: Discord API status %d", res.StatusCode)
	}
	return json.Unmarshal(body, out)
}
