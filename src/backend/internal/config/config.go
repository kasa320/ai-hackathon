// Package config は環境変数から設定を読み込む。
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
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
	// PublicBaseURL は通知に載せる画面URLと Origin 検証の基点。https なら Cookie に Secure を付ける。
	PublicBaseURL string
	// DevMode は開発・デモ用 API（/api/dev/*）と障害注入を有効にする。本番では無効にする。
	DevMode bool

	AgentMode        AgentMode
	OrcaRouterAPIKey string
	OrcaRouterURL    string
	OrcaRouterModel  string
	// OrcaRouterPlannerModel は案を考えるエージェントのモデル（空なら OrcaRouterModel）。
	OrcaRouterPlannerModel string
	// OrcaRouterInterpreterModel は Web・Discord の自由文から参加条件を取り出すモデル（空なら OrcaRouterModel）。
	OrcaRouterInterpreterModel string
	// OrcaRouterTimeout は LLM 呼び出し1回あたりの待ち時間の上限。
	OrcaRouterTimeout time.Duration
	// OrcaRouterSearchModel は目次の Web 検索に使うモデル（空なら Web 検索をせず画像の提出を依頼する）。
	OrcaRouterSearchModel string
	// OrcaRouterVisionModel は目次画像の書き写しに使うモデル（空なら OrcaRouterModel）。
	OrcaRouterVisionModel string

	DiscordClientID     string
	DiscordClientSecret string
	DiscordRedirectURL  string
	DiscordBotToken     string
	DiscordChannelID    string

	SessionSecret string
}

func Load() (Config, error) {
	c := Config{
		Addr:        env("ADDR", ":24680"),
		DBPath:      env("DB_PATH", "data/app.db"),
		FrontendDir: env("FRONTEND_DIR", "src/frontend"),

		PublicBaseURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:24680"), "/"),
		DevMode:       os.Getenv("DEV_MODE") == "1",

		AgentMode:        AgentMode(env("AGENT_MODE", string(AgentModeFake))),
		OrcaRouterAPIKey: os.Getenv("ORCAROUTER_API_KEY"),
		OrcaRouterURL:    env("ORCAROUTER_BASE_URL", "https://api.orcarouter.ai/v1"),
		OrcaRouterModel:  env("ORCAROUTER_MODEL", "orcarouter/auto"),

		OrcaRouterPlannerModel:     os.Getenv("ORCAROUTER_PLANNER_MODEL"),
		OrcaRouterInterpreterModel: os.Getenv("ORCAROUTER_INTERPRETER_MODEL"),

		OrcaRouterSearchModel: os.Getenv("ORCAROUTER_SEARCH_MODEL"),
		OrcaRouterVisionModel: os.Getenv("ORCAROUTER_VISION_MODEL"),

		DiscordClientID:     os.Getenv("DISCORD_CLIENT_ID"),
		DiscordClientSecret: os.Getenv("DISCORD_CLIENT_SECRET"),
		DiscordRedirectURL:  os.Getenv("DISCORD_REDIRECT_URL"),
		DiscordBotToken:     os.Getenv("DISCORD_BOT_TOKEN"),
		DiscordChannelID:    os.Getenv("DISCORD_CHANNEL_ID"),

		SessionSecret: os.Getenv("SESSION_SECRET"),
	}

	if c.OrcaRouterPlannerModel == "" {
		c.OrcaRouterPlannerModel = c.OrcaRouterModel
	}
	if c.OrcaRouterInterpreterModel == "" {
		c.OrcaRouterInterpreterModel = c.OrcaRouterModel
	}
	timeout, err := strconv.Atoi(env("ORCAROUTER_TIMEOUT_SECONDS", "180"))
	if err != nil || timeout < 1 || timeout > 600 {
		return Config{}, fmt.Errorf("ORCAROUTER_TIMEOUT_SECONDS が不正です: %q（1〜600の秒数）", os.Getenv("ORCAROUTER_TIMEOUT_SECONDS"))
	}
	c.OrcaRouterTimeout = time.Duration(timeout) * time.Second

	switch c.AgentMode {
	case AgentModeFake:
	case AgentModeLLM:
		if c.OrcaRouterAPIKey == "" {
			return Config{}, fmt.Errorf("AGENT_MODE=llm には ORCAROUTER_API_KEY が必要です")
		}
	default:
		return Config{}, fmt.Errorf("AGENT_MODE が不正です: %q（fake または llm）", c.AgentMode)
	}
	if u, err := url.Parse(c.PublicBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("PUBLIC_BASE_URL が不正です: %q", c.PublicBaseURL)
	}
	return c, nil
}

// DiscordLoginConfigured は Discord OAuth の設定が揃っているかを返す。
func (c Config) DiscordLoginConfigured() bool {
	return c.DiscordClientID != "" && c.DiscordClientSecret != "" && c.DiscordRedirectURL != ""
}

// DiscordNotifyConfigured は Discord へ通知を送れるかを返す。Bot トークンだけでも本人宛ての DM は送れる。
// チャンネルIDは、DM が使えないときの退避先と、全員が知るべき連絡の宛先として使う。
func (c Config) DiscordNotifyConfigured() bool {
	return c.DiscordBotToken != ""
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
