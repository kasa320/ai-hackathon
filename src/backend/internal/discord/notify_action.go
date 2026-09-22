package discord

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// onNotifyActionButton は通知に添えたボタンの押下を扱う。通知からコマンドなしで対象に束縛された
// 回答・対話を始める入口（.agent/kasa/decisions/discord-availability-home-refresh.md）。
// 操作者・所属・対象・期限・許可された decision は ResolveNotifyAction が再検証する。
func (b *Bot) onNotifyActionButton(ctx context.Context, i *discordgo.InteractionCreate, userID string, us *userSession, actionID, decision string) {
	body, _ := json.Marshal(struct {
		ActionID string `json:"action_id"`
		Decision string `json:"decision"`
	}{actionID, decision})
	// decision をキーに含めない。同じ通知で相反するボタンが競合しても、最初に成功した
	// decision と異なる本文ハッシュとして拒否される。
	idem := &store.IdemKey{UserID: userID, Key: actionID, Method: "POST", Path: "/discord/notify-actions/" + actionID + "/responses", BodyHash: httpx.BodyHash(body)}
	outcome, err := b.co.ResolveNotifyAction(ctx, userID, actionID, decision, idem)
	if err != nil {
		b.editInteraction(ctx, i, actionOutcomeText(i, notifyActionErrorText(err)))
		return
	}
	switch outcome.Kind {
	case coord.NotifyActionPreparationStart:
		b.editInteraction(ctx, i, actionOutcomeText(i, "対象を選びました。"))
		b.startConversationForSession(ctx, i.ChannelID, userID, us, outcome.SessionID)
	case coord.NotifyActionWeeklyPrompt:
		b.editInteraction(ctx, i, actionOutcomeText(i, "普段の空き時間の入力を始めます。"))
		b.beginWeeklyDialog(ctx, i.ChannelID, us)
	default:
		b.editInteraction(ctx, i, actionOutcomeText(i, "回答を保存しました。"))
	}
}

// notifyActionErrorText は業務エラーを定型文に直す。原文や詳細はログにも出さない。
func notifyActionErrorText(err error) string {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		return "いまは応答できません。Webから操作してください。"
	}
	switch ae.Code {
	case apperr.NotFound:
		return "この操作は見つかりません。Webから最新の内容を確認してください。"
	default:
		return ae.Message
	}
}

// actionOutcomeText は押されたメッセージの本文に結果を付け足す。元の文面はプログラムが送った
// 定型文（通知本文）なので、そのまま転記してよい。
func actionOutcomeText(i *discordgo.InteractionCreate, suffix string) string {
	content := ""
	if i.Message != nil {
		content = i.Message.Content
	}
	return content + "\n\n" + suffix
}

// startConversationForSession は通知のボタンから、対象を選ぶ手順を飛ばして対話を始める。
func (b *Bot) startConversationForSession(ctx context.Context, channelID, userID string, us *userSession, sessionID string) {
	targets, err := b.co.PreparationTargets(ctx, userID)
	if err != nil {
		b.send(ctx, channelID, msgUnavailable+webLink(b.base, ""), nil)
		return
	}
	for _, t := range targets {
		if t.SessionID == sessionID {
			b.startConversation(ctx, channelID, userID, us, t, true)
			return
		}
	}
	b.send(ctx, channelID, msgNoTargets+webLink(b.base, ""), nil)
}
