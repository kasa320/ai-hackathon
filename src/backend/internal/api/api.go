// Package api は HTTP の入口。/api/ 以下の API と、フロントエンドの静的ファイル配信を扱う。
// 入出力は api.md 第1部の契約に従い、業務処理は coord.Coordinator に委ねる。
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps は API サーバーの依存関係。
type Deps struct {
	DB    Pinger
	Clock clock.Clock
	Log   *slog.Logger
	Coord *coord.Coordinator
	Auth  *auth.Manager
	// AllowedOrigins は変更系リクエストで受け付ける Origin（例：PUBLIC_BASE_URL）。
	AllowedOrigins []string
	DevMode        bool
	// Extensions は用途固有の API。
	Extensions []httpx.Extension
}

type Server struct {
	Deps
}

func New(d Deps) *Server {
	return &Server{Deps: d}
}

// Handler は API と静的ファイル配信をまとめたハンドラを返す。
// mount には開発モードでだけ登録する API（/api/dev/*）などを渡す。
func (s *Server) Handler(frontendDir string, mount ...func(mux *http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/playbooks", s.listPlaybooks)
	mux.HandleFunc("GET /api/auth/discord", s.startLogin)
	mux.HandleFunc("GET /api/auth/callback", s.callback)
	mux.HandleFunc("GET /api/me", s.authed(s.me))
	mux.HandleFunc("POST /api/auth/logout", s.authed(s.logout))

	mux.HandleFunc("GET /api/groups", s.authed(s.listGroups))
	mux.HandleFunc("POST /api/groups", s.authed(s.createGroup))
	mux.HandleFunc("GET /api/groups/{group_id}", s.authed(s.getGroup))
	mux.HandleFunc("GET /api/groups/{group_id}/sessions", s.authed(s.listSessions))
	mux.HandleFunc("POST /api/groups/{group_id}/sessions", s.authed(s.createSession))
	mux.HandleFunc("GET /api/sessions/{session_id}", s.authed(s.getSession))
	mux.HandleFunc("PUT /api/sessions/{session_id}/preparations/me", s.authed(s.putPreparation))
	mux.HandleFunc("POST /api/sessions/{session_id}/withdrawals", s.authed(s.withdraw))
	mux.HandleFunc("POST /api/tasks/{task_id}/responses", s.authed(s.respondTask))
	mux.HandleFunc("POST /api/sessions/{session_id}/proposals", s.authed(s.submitProposal))
	mux.HandleFunc("GET /api/sessions/{session_id}/activity", s.authed(s.activity))

	for _, ext := range s.Extensions {
		for _, rt := range ext.Routes() {
			pattern := fmt.Sprintf("%s /api/groups/{group_id}/%s/%s", rt.Method, ext.PlaybookID(), rt.Pattern)
			mux.HandleFunc(pattern, s.authed(s.extension(rt)))
		}
	}
	for _, m := range mount {
		m(mux)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.apiFallback(mux, w, r) })
	mux.Handle("/", http.FileServer(http.Dir(frontendDir)))
	return s.middleware(mux)
}

// apiFallback は未定義の API に JSON の 404、メソッド違いに 405 を返す。
func (s *Server) apiFallback(mux *http.ServeMux, w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/api/" && pattern != "/" {
			allow = append(allow, m)
		}
	}
	if len(allow) > 0 {
		httpx.WriteError(w, r, s.Log, apperr.New(apperr.MethodNotAllowed, "このメソッドは使えません。").With("allow", strings.Join(allow, ", ")))
		return
	}
	httpx.WriteError(w, r, s.Log, apperr.NotFoundErr())
}

// middleware はリクエストID・キャッシュ禁止・パニックの回復・ログをまとめる。
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := store.NewID("req")
		r = r.WithContext(httpx.WithRequestID(r.Context(), id))
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Request-Id", id)
		}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				httpx.WriteError(w, r, s.Log, fmt.Errorf("panic: %v", v))
			}
			s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "request_id", id, "elapsed", time.Since(start))
		}()
		next.ServeHTTP(w, r)
	})
}

type authedHandler func(w http.ResponseWriter, r *http.Request, sess auth.Session)

// authed はログインを確認し、変更系のリクエストでは Origin と CSRF トークンを検証する。
func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Auth.Authenticate(r.Context(), r)
		if errors.Is(err, auth.ErrUnauthenticated) {
			httpx.WriteError(w, r, s.Log, apperr.New(apperr.Unauthenticated, "ログインしてください。"))
			return
		}
		if err != nil {
			httpx.WriteError(w, r, s.Log, err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !httpx.CheckOrigin(r, s.AllowedOrigins) {
				httpx.WriteError(w, r, s.Log, apperr.New(apperr.Forbidden, "別のサイトからの操作は受け付けません。"))
				return
			}
			if !sess.CheckCSRF(r) {
				httpx.WriteError(w, r, s.Log, apperr.New(apperr.CSRFInvalid, "CSRF トークンが正しくありません。/api/me を再取得してください。"))
				return
			}
		}
		h(w, r, sess)
	}
}

// idemKey は Idempotency-Key ヘッダーから再送判定のキーを作る。
func idemKey(r *http.Request, sess auth.Session, raw []byte) (*store.IdemKey, error) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 200 {
		return nil, apperr.New(apperr.IdempotencyKeyRequired, "Idempotency-Key ヘッダーが必要です。")
	}
	return &store.IdemKey{UserID: sess.User.ID, Key: key, Method: r.Method, Path: r.URL.Path, BodyHash: httpx.BodyHash(raw)}, nil
}

// mutation は業務更新の共通手順：アクセス確認 → 再送キー → 本文のデコード → 業務処理（再送判定を含む）。
func mutation[T any](s *Server, w http.ResponseWriter, r *http.Request, sess auth.Session, access func() error, run func(in T, idem *store.IdemKey) (store.Response, error)) {
	if access != nil {
		if err := access(); err != nil {
			httpx.WriteError(w, r, s.Log, err)
			return
		}
	}
	if r.Header.Get("Idempotency-Key") == "" {
		httpx.WriteError(w, r, s.Log, apperr.New(apperr.IdempotencyKeyRequired, "Idempotency-Key ヘッダーが必要です。"))
		return
	}
	var in T
	raw, err := httpx.ReadJSON(w, r, &in)
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	key, err := idemKey(r, sess, raw)
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	res, err := run(in, key)
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	httpx.WriteResponse(w, res)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		s.Log.Error("DB に接続できない", "err", err)
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"now":      s.Clock.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
		"dev_mode": s.DevMode,
	})
}

func (s *Server) listPlaybooks(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"playbooks": s.Coord.Playbooks()})
}

func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	u, err := s.Auth.StartLogin(r.Context(), w, r.URL.Query().Get("return_to"))
	if err != nil {
		s.Log.Error("ログイン開始に失敗", "err", err)
		http.Redirect(w, r, "/?auth_error="+auth.ErrProviderUnavailable, http.StatusFound)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.Auth.Callback(r.Context(), w, r), http.StatusSeeOther)
}

// MeResponse は GET /api/me の応答。
type MeResponse struct {
	User struct {
		ID            string `json:"id"`
		DisplayName   string `json:"display_name"`
		DiscordUserID string `json:"discord_user_id"`
	} `json:"user"`
	CSRFToken string `json:"csrf_token"`
}

func MeFor(u store.User, csrf string) MeResponse {
	var out MeResponse
	out.User.ID, out.User.DisplayName, out.User.DiscordUserID = u.ID, u.DisplayName, u.DiscordUserID
	out.CSRFToken = csrf
	return out
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	httpx.WriteJSON(w, http.StatusOK, MeFor(sess.User, sess.CSRFToken))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Auth.Logout(r.Context(), sess); err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	s.Auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out, err := s.Coord.ListGroups(r.Context(), sess.User.ID)
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	mutation(s, w, r, sess, nil, func(in apitypes.CreateGroupInput, key *store.IdemKey) (store.Response, error) {
		return s.Coord.CreateGroup(r.Context(), sess.User.ID, in, key)
	})
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out, err := s.Coord.GetGroup(r.Context(), sess.User.ID, r.PathValue("group_id"))
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out, err := s.Coord.ListSessions(r.Context(), sess.User.ID, r.PathValue("group_id"))
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	groupID := r.PathValue("group_id")
	access := func() error { _, err := s.Coord.CheckGroupAccess(r.Context(), sess.User.ID, groupID, true); return err }
	mutation(s, w, r, sess, access, func(in apitypes.CreateSessionInput, key *store.IdemKey) (store.Response, error) {
		return s.Coord.CreateSession(r.Context(), sess.User.ID, groupID, in, key)
	})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out, err := s.Coord.SessionDetail(r.Context(), sess.User.ID, r.PathValue("session_id"))
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) sessionAccess(r *http.Request, sess auth.Session) func() error {
	return func() error { return s.Coord.CheckSessionAccess(r.Context(), sess.User.ID, r.PathValue("session_id")) }
}

func (s *Server) putPreparation(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	mutation(s, w, r, sess, s.sessionAccess(r, sess), func(in apitypes.PutPreparationInput, key *store.IdemKey) (store.Response, error) {
		return s.Coord.PutPreparation(r.Context(), sess.User.ID, r.PathValue("session_id"), in, key)
	})
}

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	mutation(s, w, r, sess, s.sessionAccess(r, sess), func(in apitypes.WithdrawalInput, key *store.IdemKey) (store.Response, error) {
		return s.Coord.Withdraw(r.Context(), sess.User.ID, r.PathValue("session_id"), in, key)
	})
}

func (s *Server) respondTask(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	taskID := r.PathValue("task_id")
	access := func() error { return s.Coord.CheckTaskAccess(r.Context(), sess.User.ID, taskID) }
	mutation(s, w, r, sess, access, func(in apitypes.TaskResponseInput, key *store.IdemKey) (store.Response, error) {
		return s.Coord.RespondTask(r.Context(), sess.User.ID, taskID, in, key)
	})
}

func (s *Server) submitProposal(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	mutation(s, w, r, sess, s.sessionAccess(r, sess), func(in apitypes.SubmitProposalInput, key *store.IdemKey) (store.Response, error) {
		return s.Coord.SubmitProposal(r.Context(), sess.User.ID, r.PathValue("session_id"), in, key)
	})
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out, err := s.Coord.Activity(r.Context(), sess.User.ID, r.PathValue("session_id"))
	if err != nil {
		httpx.WriteError(w, r, s.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// extension は用途固有の API に、所属（必要なら管理者）の確認と再送キー・本文を添えて渡す。
func (s *Server) extension(rt httpx.Route) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		groupID := r.PathValue("group_id")
		m, err := s.Coord.CheckGroupAccess(r.Context(), sess.User.ID, groupID, rt.OwnerOnly)
		if err != nil {
			httpx.WriteError(w, r, s.Log, err)
			return
		}
		req := httpx.Request{UserID: sess.User.ID, GroupID: groupID, MemberID: m.ID, Role: m.Role}
		if r.Method != http.MethodGet {
			if r.Header.Get("Idempotency-Key") == "" {
				httpx.WriteError(w, r, s.Log, apperr.New(apperr.IdempotencyKeyRequired, "Idempotency-Key ヘッダーが必要です。"))
				return
			}
			limit := rt.MaxBody
			if limit == 0 {
				limit = httpx.MaxJSONBody
			}
			if req.Body, err = httpx.ReadBody(w, r, limit); err != nil {
				httpx.WriteError(w, r, s.Log, err)
				return
			}
			if req.Idem, err = idemKey(r, sess, req.Body); err != nil {
				httpx.WriteError(w, r, s.Log, err)
				return
			}
		}
		rt.Handler(w, r, req)
	}
}
