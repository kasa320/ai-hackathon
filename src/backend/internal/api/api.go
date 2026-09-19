// Package api は HTTP の入口。/api/ 以下の API と、フロントエンドの静的ファイル配信を扱う。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type Server struct {
	db    Pinger
	clock clock.Clock
	log   *slog.Logger
}

func New(db Pinger, clk clock.Clock, log *slog.Logger) *Server {
	return &Server{db: db, clock: clk, log: log}
}

// Handler は API と静的ファイル配信をまとめたハンドラを返す。
func (s *Server) Handler(frontendDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.Handle("/", http.FileServer(http.Dir(frontendDir)))
	return s.logRequests(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.db.Ping(ctx); err != nil {
		s.log.Error("DB に接続できない", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"now":    s.clock.Now().Format(time.RFC3339),
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "elapsed", time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
