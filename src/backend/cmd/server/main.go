// server は API サーバーを起動し、フロントエンドを配信する。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/agent"
	"github.com/kasa320/ai-hackathon/src/backend/internal/api"
	"github.com/kasa320/ai-hackathon/src/backend/internal/auth"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/config"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/devapi"
	"github.com/kasa320/ai-hackathon/src/backend/internal/discord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/fault"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/notify"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading/toc"
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
	faults := fault.New()
	faults.Define("llm", "error", "invalid_output")
	faults.Define("notify", "fail", "unknown")
	toc.DefineFaults(faults)

	// 用途別実装を共通側へ渡すのは起動処理だけ。追加用途もここに登録する。
	registry, err := coord.NewService(reading.New())
	if err != nil {
		return err
	}

	var planner coord.Planner = coord.DraftOnlyPlanner{}
	var interpreter coord.Interpreter = coord.DraftOnlyInterpreter{}
	tocDeps := toc.Deps{Store: st, Clock: clk, Faults: faults, Bib: toc.Chain{toc.NewOpenBD(), toc.NewNDLSearch()}, Fetcher: toc.NewSafeFetcher(), Log: log}
	if cfg.AgentMode == config.AgentModeLLM {
		client := agent.NewClient(cfg.OrcaRouterURL, cfg.OrcaRouterAPIKey, cfg.OrcaRouterTimeout)
		// 案を考える処理と、自由文から条件を取り出す処理は別のモデルを使える
		planner = &agent.LLMPlanner{Client: client, Model: cfg.OrcaRouterPlannerModel}
		interpreter = &agent.LLMInterpreter{Client: client, Model: cfg.OrcaRouterInterpreterModel}
		log.Info("LLM の設定", "planner_model", cfg.OrcaRouterPlannerModel, "interpreter_model", cfg.OrcaRouterInterpreterModel, "timeout", cfg.OrcaRouterTimeout)
		if cfg.OrcaRouterSearchModel != "" {
			tocDeps.Searcher = &toc.LLMSearcher{Client: client, Model: cfg.OrcaRouterSearchModel}
		}
		vision := cfg.OrcaRouterVisionModel
		if vision == "" {
			vision = cfg.OrcaRouterModel
		}
		tocDeps.Reader = &toc.LLMImageReader{Client: client, Model: vision}
	}
	tocService := toc.NewService(tocDeps)
	if err := tocService.Recover(ctx); err != nil {
		return err
	}
	var sender notify.Sender = notify.LogSender{Log: log}
	if cfg.DiscordNotifyConfigured() {
		sender = notify.NewDiscordSender(cfg.DiscordBotToken, cfg.DiscordChannelID)
	}
	if cfg.DevMode {
		planner = agent.WithFaults(planner, faults)
		interpreter = agent.WithInterpretFaults(interpreter, faults)
		sender = notify.WithFaults(sender, faults)
	}

	runLock := &sync.Mutex{}
	coordinator := coord.NewCoordinator(registry, st, clk, planner, coord.Options{PublicBaseURL: cfg.PublicBaseURL, Log: log, RunLock: runLock, Interpreter: interpreter})
	dispatcher := notify.NewDispatcher(st, clk, sender, log)
	if err := dispatcher.Recover(ctx); err != nil {
		return err
	}

	// Bot のトークンがあるときだけ、DM での対話を受け付ける常駐Botを動かす。
	if cfg.DiscordBotToken != "" {
		bot, err := discord.New(discord.Deps{
			Token: cfg.DiscordBotToken, Coord: coordinator, Clock: clk, Log: log, PublicBaseURL: cfg.PublicBaseURL,
		})
		if err != nil {
			return err
		}
		go func() {
			if err := bot.Run(ctx); err != nil {
				log.Error("Discord Bot が停止", "err", err)
			}
		}()
	}

	var provider auth.Provider
	if cfg.DiscordLoginConfigured() {
		provider = auth.NewDiscordProvider(cfg.DiscordClientID, cfg.DiscordClientSecret, cfg.DiscordRedirectURL)
	}
	secret := cfg.SessionSecret
	if secret == "" {
		log.Warn("SESSION_SECRET が未設定のため、一時的な値を使います（再起動でログアウトされます）")
		secret = store.RandomToken()
	}
	authManager := auth.NewManager(st, clk, provider, secret, strings.HasPrefix(cfg.PublicBaseURL, "https://"))

	server := api.New(api.Deps{
		DB: st, Clock: clk, Log: log, Coord: coordinator, Auth: authManager,
		AllowedOrigins: []string{cfg.PublicBaseURL}, DevMode: cfg.DevMode,
		Extensions: []httpx.Extension{toc.NewExtension(tocService)},
	})
	var mounts []func(*http.ServeMux)
	if cfg.DevMode {
		// 開発モードでだけ /api/dev/* を登録する。無効時は存在しない扱い（404）。
		mounts = append(mounts, devapi.Mount(devapi.Deps{
			Store: st, Clock: clk, Auth: authManager, Faults: faults, AllowedOrigins: []string{cfg.PublicBaseURL}, Log: log,
			Seeder: devapi.NewSeeder(registry, st, clk, cfg.PublicBaseURL, runLock), Wake: coordinator.Wake,
		}))
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(cfg.FrontendDir, mounts...),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go coordinator.Run(ctx, time.Second)
	go dispatcher.Run(ctx, 2*time.Second)
	go tocService.Run(ctx, 2*time.Second)
	go purgeIdempotency(ctx, st, clk, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("起動", "addr", cfg.Addr, "agent_mode", cfg.AgentMode, "dev_mode", cfg.DevMode, "db", cfg.DBPath,
			"discord_login", provider != nil, "discord_notify", cfg.DiscordNotifyConfigured(), "discord_dm", cfg.DiscordBotToken != "")
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

// purgeIdempotency は保存期間（24時間以上）を過ぎた再送情報を定期的に削除する。
func purgeIdempotency(ctx context.Context, st *store.Store, clk clock.Clock, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := st.PurgeIdempotency(ctx, clk.Now().Add(-48*time.Hour)); err != nil {
				log.Warn("再送情報の削除に失敗", "err", err)
			}
		}
	}
}
