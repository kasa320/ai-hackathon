// server は API サーバーを起動し、フロントエンドを配信する。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/config"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("起動に失敗", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	clk := clock.NewOffset(clock.Real{})
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(st, clk, log).Handler(cfg.FrontendDir),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("起動", "addr", cfg.Addr, "agent_mode", cfg.AgentMode, "db", cfg.DBPath)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("停止中")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
