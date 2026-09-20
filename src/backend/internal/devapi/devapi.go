// Package devapi は開発・デモ用の API（/api/dev/*）。DEV_MODE=1 のときだけ起動処理がルーティングに登録する。
// 本番用のハンドラーには開発モードの分岐を入れず、無効時はこれらのパスが存在しない（404）。
package devapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// maxAdvance は1回で進められる時計の上限。
const maxAdvance = 30 * 24 * time.Hour

type Deps struct {
	Store          *store.Store
	Clock          *clock.Offset
	Auth           *auth.Manager
	Faults         *fault.Registry
	Seeder         *Seeder
	AllowedOrigins []string
	Log            *slog.Logger
	// Wake は時計を進めた後にイベント処理を起こす。
	Wake func()
}

// Mount は開発用 API を mux に登録する関数を返す。
func Mount(d Deps) func(mux *http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/dev/status", d.status)
		mux.HandleFunc("POST /api/dev/clock/advance", d.guard(d.advance))
		mux.HandleFunc("POST /api/dev/login", d.guard(d.login))
		mux.HandleFunc("PUT /api/dev/faults", d.guard(d.setFaults))
		mux.HandleFunc("POST /api/dev/seed", d.guard(d.seed))
	}
}

// guard は Origin を検証する（開発用 API は Idempotency-Key 不要）。
func (d Deps) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpx.CheckOrigin(r, d.AllowedOrigins) {
			httpx.WriteError(w, r, d.Log, apperr.New(apperr.Forbidden, "別のサイトからの操作は受け付けません。"))
			return
		}
		h(w, r)
	}
}

type statusResponse struct {
	Now                time.Time          `json:"now"`
	ClockOffsetSeconds int64              `json:"clock_offset_seconds"`
	Faults             map[string]*string `json:"faults"`
}

func (d Deps) statusBody() statusResponse {
	return statusResponse{Now: d.Clock.Now().UTC().Truncate(time.Second), ClockOffsetSeconds: int64(d.Clock.Offset().Seconds()), Faults: d.Faults.Snapshot()}
}

func (d Deps) status(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, d.statusBody())
}

func (d Deps) advance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Seconds int64 `json:"seconds"`
	}
	if _, err := httpx.ReadJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	dur := time.Duration(in.Seconds) * time.Second
	if in.Seconds <= 0 || dur > maxAdvance {
		httpx.WriteError(w, r, d.Log, apperr.Validation(apperr.Field{Path: "seconds", Message: "1秒以上30日以内で指定してください（時計は戻せません）"}))
		return
	}
	d.Clock.Advance(dur)
	if d.Wake != nil {
		d.Wake()
	}
	httpx.WriteJSON(w, http.StatusOK, d.statusBody())
}

func (d Deps) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DiscordUserID string `json:"discord_user_id"`
	}
	if _, err := httpx.ReadJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	token, u, err := d.Auth.IssueExisting(r.Context(), in.DiscordUserID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, d.Log, apperr.NotFoundErr())
		return
	}
	if err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	d.Auth.SetCookie(w, token)
	// 通常のログインと同じセッションを発行し、/api/me と同じ形で返す。
	sess, err := d.Auth.Authenticate(r.Context(), requestWithCookie(r, token))
	if err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.MeFor(u, sess.CSRFToken))
}

func requestWithCookie(r *http.Request, token string) *http.Request {
	c := r.Clone(r.Context())
	c.Header = http.Header{}
	c.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	return c
}

func (d Deps) setFaults(w http.ResponseWriter, r *http.Request) {
	var in map[string]*string
	if _, err := httpx.ReadJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	if err := d.Faults.Replace(in); err != nil {
		httpx.WriteError(w, r, d.Log, apperr.Validation(apperr.Field{Path: "", Message: err.Error()}))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d.statusBody())
}

func (d Deps) seed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scenario string `json:"scenario"`
	}
	if _, err := httpx.ReadJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	res, err := d.Seeder.Seed(context.WithoutCancel(r.Context()), in.Scenario)
	if err != nil {
		httpx.WriteError(w, r, d.Log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
