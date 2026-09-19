// Package config は環境変数から設定を読み込む。
package config

import (
	"fmt"
	"os"
)

// AgentMode はエージェントの動作モード。
type AgentMode string

const (
	// AgentModeFake は LLM を呼ばずに決まった応答を返す（画面開発・テスト用）。
	AgentModeFake AgentMode = "fake"
	// AgentModeLLM は OrcaRouter 経由で LLM を呼ぶ。
	AgentModeLLM AgentMode = "llm"
)

type Config struct {
	Addr        string
	DBPath      string
	FrontendDir string

	AgentMode        AgentMode
	OrcaRouterAPIKey string
	OrcaRouterURL    string
	OrcaRouterModel  string

	DiscordClientID     string
	DiscordClientSecret string
	DiscordRedirectURL  string
	DiscordBotToken     string

	SessionSecret string
}

func Load() (Config, error) {
	c := Config{
		Addr:        env("ADDR", ":8080"),
		DBPath:      env("DB_PATH", "data/app.db"),
		FrontendDir: env("FRONTEND_DIR", "src/frontend"),

		AgentMode:        AgentMode(env("AGENT_MODE", string(AgentModeFake))),
		OrcaRouterAPIKey: os.Getenv("ORCAROUTER_API_KEY"),
		OrcaRouterURL:    env("ORCAROUTER_BASE_URL", "https://api.orcarouter.ai/v1"),
		OrcaRouterModel:  env("ORCAROUTER_MODEL", "orcarouter/auto"),

		DiscordClientID:     os.Getenv("DISCORD_CLIENT_ID"),
		DiscordClientSecret: os.Getenv("DISCORD_CLIENT_SECRET"),
		DiscordRedirectURL:  os.Getenv("DISCORD_REDIRECT_URL"),
		DiscordBotToken:     os.Getenv("DISCORD_BOT_TOKEN"),

		SessionSecret: os.Getenv("SESSION_SECRET"),
	}

	switch c.AgentMode {
	case AgentModeFake:
	case AgentModeLLM:
		if c.OrcaRouterAPIKey == "" {
			return Config{}, fmt.Errorf("AGENT_MODE=llm には ORCAROUTER_API_KEY が必要です")
		}
	default:
		return Config{}, fmt.Errorf("AGENT_MODE が不正です: %q（fake または llm）", c.AgentMode)
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
