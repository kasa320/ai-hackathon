package discord

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// turnTimeout は1ターンの処理に与える時間。LLM の応答が返らなくても下書きを壊さない。
const turnTimeout = 60 * time.Second

// Deps は Bot の依存。DBと認可は Coordinator が持ち、ここからは触らない。
type Deps struct {
	Token         string
	Coord         *coord.Coordinator
	Clock         clock.Clock
	Log           *slog.Logger
	PublicBaseURL string
}

// Bot は DM での対話を受け持つ常駐Bot。1プロセスで動かし、利用者ごとに処理を直列化する。
type Bot struct {
	dg   *discordgo.Session
	co   *coord.Coordinator
	clk  clock.Clock
	log  *slog.Logger
	base string
	mem  *memory
}

func New(d Deps) (*Bot, error) {
	dg, err := discordgo.New("Bot " + d.Token)
	if err != nil {
		return nil, err
	}
	// DM本文の受信に必要なのは DIRECT_MESSAGES だけ。BotとのDMは MESSAGE_CONTENT の例外。
	dg.Identify.Intents = discordgo.IntentsDirectMessages
	// 本文やツールの生出力を抱えないよう、SDK の履歴キャッシュを無効にする。
	dg.StateEnabled = false
	dg.State.MaxMessageCount = 0
	dg.State.TrackMembers = false
	dg.State.TrackPresences = false

	b := &Bot{dg: dg, co: d.Coord, clk: d.Clock, log: d.Log, base: strings.TrimSuffix(d.PublicBaseURL, "/"), mem: newMemory()}
	dg.AddHandler(b.onMessage)
	dg.AddHandler(b.onInteraction)
	return b, nil
}

// Run は Gateway に接続し、ctx が終わるまで待つ。
func (b *Bot) Run(ctx context.Context) error {
	if err := b.dg.Open(); err != nil {
		return err
	}
	b.log.Info("Discord Bot を開始", "intents", "direct_messages")
	<-ctx.Done()
	return b.dg.Close()
}

func (b *Bot) now() time.Time { return b.clk.Now().UTC() }

// onMessage は DM の本文だけを受け取る。Bot・サーバー内の発言・本文のない更新は扱わない。
func (b *Bot) onMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot || m.GuildID != "" || strings.TrimSpace(m.Content) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	defer cancel()
	b.handleMessage(ctx, m)
}

func (b *Bot) handleMessage(ctx context.Context, m *discordgo.MessageCreate) {
	now := b.now()
	if !b.mem.firstSeen("msg:"+m.ID, now) {
		return
	}
	user, err := b.co.UserByDiscordID(ctx, m.Author.ID)
	if err != nil {
		b.send(ctx, m.ChannelID, msgNoAccount+webLink(b.base, ""), nil)
		return
	}
	us, ok := b.mem.begin(user.ID, now)
	if !ok {
		b.send(ctx, m.ChannelID, msgBusy, nil)
		return
	}
	defer func() { b.mem.end(user.ID, b.now()) }()

	text := strings.TrimSpace(m.Content)
	// 定型の読み取り・取消・対象切替は LLM を使わない。クールダウン中も使える。
	switch {
	case isCommand(text, "取消", "取り消し", "キャンセル", "やめる"):
		b.clearDraft(ctx, us)
		b.send(ctx, m.ChannelID, msgCanceled, nil)
		return
	case isCommand(text, "予定", "いまの予定", "今の予定", "確認"):
		b.replyCurrent(ctx, m.ChannelID, user.ID, us)
		return
	case isCommand(text, "変更", "切替", "切り替え", "別の回"):
		b.clearDraft(ctx, us)
		us.conv = nil
		b.askTarget(ctx, m.ChannelID, user.ID, us)
		return
	case isCommand(text, "ヘルプ", "help", "使い方"):
		b.send(ctx, m.ChannelID, msgHelp, nil)
		return
	case isCommand(text, "変更なし", "変更ありません", "このまま", "そのまま"):
		// 変更しない確認も本人のボタンで行う。LLM は使わない。
		b.confirmUnchanged(ctx, m.ChannelID, user.ID, us)
		return
	}

	if us.conv == nil {
		// 対象が決まっていなければ先に決める。選択前の発言は保持しない。
		if !b.pickTarget(ctx, m.ChannelID, user.ID, us) {
			return
		}
	}
	// 訂正を受け取った時点で、表示済みの確認は無効にする。
	b.clearDraft(ctx, us)
	if now.Before(us.cooldownUntil) {
		b.send(ctx, m.ChannelID, msgCooldown+webLink(b.base, us.conv.sessionID), nil)
		return
	}

	res, err := b.co.ContinueDialog(ctx, user.ID, us.conv.state, text)
	if err != nil {
		// モデル・通信の失敗は空振りに数えず、値も変えない。
		b.replyError(ctx, m.ChannelID, user.ID, us, err)
		return
	}
	if res.OutOfScope != "" {
		b.countMiss(ctx, m.ChannelID, us, outOfScopeHint(res.OutOfScope))
		return
	}
	us.conv.state = res.State
	if !res.Progressed {
		b.countMiss(ctx, m.ChannelID, us, "読み取れませんでした。"+res.Question)
		return
	}
	us.misses = 0
	b.reply(ctx, m.ChannelID, us, res)
}

// reply は進んだ会話の続きを返す。確認に進めるならボタン付きの確認を出す。
func (b *Bot) reply(ctx context.Context, channelID string, us *userSession, res coord.DialogResult) {
	conv := us.conv
	conv.draftID = newToken()
	if !res.Ready {
		b.send(ctx, channelID, sessionHeader(conv.title, conv.startsAt)+"\n"+res.Question, nil)
		return
	}
	msg, err := b.sendComplex(ctx, channelID, confirmText(conv, res.Confirm), confirmButtons(conv.draftID))
	if err != nil {
		b.log.Warn("確認の送信に失敗", "session_id", conv.sessionID)
		return
	}
	// 保存するのはこの不変スナップショットで、その後に変わる下書きではない。
	conv.confirm = &confirmation{draftID: conv.draftID, state: res.State, lines: res.Confirm, channelID: channelID, messageID: msg.ID}
}

// countMiss は進捗のない入力を数え、続くようなら休止する。
func (b *Bot) countMiss(ctx context.Context, channelID string, us *userSession, text string) {
	us.misses++
	if us.misses >= missLimit {
		us.misses = 0
		us.cooldownUntil = b.now().Add(cooldownFor)
		b.send(ctx, channelID, msgCooldown+webLink(b.base, convSessionID(us)), nil)
		return
	}
	b.send(ctx, channelID, text, nil)
}

// clearDraft は表示済みの確認を無効にし、未保存の下書きの世代を進める。
func (b *Bot) clearDraft(ctx context.Context, us *userSession) {
	if us.conv == nil || us.conv.confirm == nil {
		return
	}
	c := us.conv.confirm
	b.disableButtons(ctx, c.channelID, c.messageID, confirmText(us.conv, c.lines)+"\n（この確認は無効になりました）")
	us.conv.confirm = nil
	us.conv.draftID = newToken()
}

// pickTarget は対象の開催回を決める。1件ならその回、複数ならボタンで選ばせる。
func (b *Bot) pickTarget(ctx context.Context, channelID, userID string, us *userSession) bool {
	targets, err := b.co.PreparationTargets(ctx, userID)
	if err != nil {
		b.send(ctx, channelID, msgUnavailable+webLink(b.base, ""), nil)
		return false
	}
	switch {
	case len(targets) == 0:
		b.send(ctx, channelID, msgNoTargets+webLink(b.base, ""), nil)
		return false
	case len(targets) == 1:
		return b.startConversation(ctx, channelID, userID, us, targets[0], false)
	default:
		b.offerTargets(ctx, channelID, us, targets)
		return false
	}
}

func (b *Bot) askTarget(ctx context.Context, channelID, userID string, us *userSession) {
	targets, err := b.co.PreparationTargets(ctx, userID)
	switch {
	case err != nil:
		b.send(ctx, channelID, msgUnavailable+webLink(b.base, ""), nil)
	case len(targets) == 0:
		b.send(ctx, channelID, msgNoTargets+webLink(b.base, ""), nil)
	default:
		b.offerTargets(ctx, channelID, us, targets)
	}
}

func (b *Bot) offerTargets(ctx context.Context, channelID string, us *userSession, targets []coord.DialogTarget) {
	if len(targets) > maxTargetButtons {
		targets = targets[:maxTargetButtons]
	}
	selectID := newToken()
	if _, err := b.sendComplex(ctx, channelID, msgPickTarget, targetButtons(selectID, targets)); err != nil {
		b.log.Warn("対象の選択肢の送信に失敗")
		return
	}
	us.conv = &conversation{targets: targets, selectID: selectID}
}

// startConversation は対象を決めて会話を始める。押下時にも所属と変更可能性を確かめ直す。
func (b *Bot) startConversation(ctx context.Context, channelID, userID string, us *userSession, t coord.DialogTarget, announce bool) bool {
	res, err := b.co.StartDialog(ctx, userID, t.SessionID)
	if err != nil {
		us.conv = nil
		b.replyError(ctx, channelID, userID, us, err)
		return false
	}
	us.conv = &conversation{sessionID: t.SessionID, title: t.Title, startsAt: t.StartsAt, state: res.State, draftID: newToken()}
	if announce {
		b.send(ctx, channelID, sessionHeader(t.Title, t.StartsAt)+"\n"+msgReenter, nil)
	}
	return true
}

// confirmUnchanged は今の内容のまま確認だけを出す。未回答の確認タスクはこれで完了できる。
func (b *Bot) confirmUnchanged(ctx context.Context, channelID, userID string, us *userSession) {
	if us.conv == nil || us.conv.sessionID == "" {
		if !b.pickTarget(ctx, channelID, userID, us) {
			return
		}
	}
	res, err := b.co.StartDialog(ctx, userID, us.conv.sessionID)
	if err != nil {
		b.replyError(ctx, channelID, userID, us, err)
		return
	}
	b.clearDraft(ctx, us)
	us.conv.state = res.State
	b.reply(ctx, channelID, us, res)
}

// replyCurrent は保存済みの内容を返す。LLM は使わない。
func (b *Bot) replyCurrent(ctx context.Context, channelID, userID string, us *userSession) {
	if us.conv == nil || us.conv.sessionID == "" {
		targets, err := b.co.PreparationTargets(ctx, userID)
		if err != nil || len(targets) == 0 {
			b.send(ctx, channelID, msgNoTargets+webLink(b.base, ""), nil)
			return
		}
		if len(targets) > 1 {
			b.offerTargets(ctx, channelID, us, targets)
			return
		}
		if !b.startConversation(ctx, channelID, userID, us, targets[0], false) {
			return
		}
	}
	res, err := b.co.StartDialog(ctx, userID, us.conv.sessionID)
	if err != nil {
		b.replyError(ctx, channelID, userID, us, err)
		return
	}
	conv := us.conv
	if len(res.Confirm) == 0 {
		b.send(ctx, channelID, sessionHeader(conv.title, conv.startsAt)+"\nまだ回答がありません。\n"+res.Question, nil)
		return
	}
	b.send(ctx, channelID, sessionHeader(conv.title, conv.startsAt)+"\n"+escapeLines(res.Confirm), nil)
}

// replyError は業務エラーを定型文に直す。原文や詳細はログにも出さない。
func (b *Bot) replyError(ctx context.Context, channelID, userID string, us *userSession, err error) {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		b.log.Error("対話の処理に失敗")
		b.send(ctx, channelID, msgUnavailable+webLink(b.base, ""), nil)
		return
	}
	sessionID := convSessionID(us)
	switch ae.Code {
	case apperr.RevisionConflict:
		// 古い値に新しい版を付けて送り直さない。最新の状態から確認し直す。
		if us.conv != nil {
			if res, rerr := b.co.StartDialog(ctx, userID, us.conv.sessionID); rerr == nil {
				us.conv.state = res.State
				us.conv.draftID = newToken()
			} else {
				us.conv = nil
			}
		}
		b.send(ctx, channelID, msgConflict, nil)
	case apperr.NotFound:
		us.conv = nil
		b.send(ctx, channelID, msgNoTargets+webLink(b.base, ""), nil)
	default:
		b.send(ctx, channelID, ae.Message+webLink(b.base, sessionID), nil)
	}
}

func convSessionID(us *userSession) string {
	if us == nil || us.conv == nil {
		return ""
	}
	return us.conv.sessionID
}

func isCommand(text string, words ...string) bool {
	t := strings.TrimSpace(strings.Trim(text, "。．！!？?"))
	for _, w := range words {
		if strings.EqualFold(t, w) {
			return true
		}
	}
	return false
}

// idemKey は連打・イベント再送を1回の保存として扱うための冪等キー。
// 利用者と下書きの世代で同じキーになり、表示した値から同じ本文ハッシュになる。
func idemKey(userID string, c *confirmation) *store.IdemKey {
	body, err := json.Marshal(apitypes.Preparation{Attendance: c.state.Attendance, Data: c.state.Data})
	if err != nil {
		return nil
	}
	return &store.IdemKey{
		UserID: userID, Key: c.draftID, Method: "PUT",
		Path: "/discord/sessions/" + c.state.SessionID + "/preparations/me", BodyHash: httpx.BodyHash(body),
	}
}
