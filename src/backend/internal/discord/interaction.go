package discord

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// ボタンは Gateway の INTERACTION_CREATE で受ける。同じアプリに Interactions Endpoint URL は設定しない。
// 3秒以内に初期応答するため、まず defer してから保存等を行い、完了後にメッセージを更新する。
func (b *Bot) onInteraction(_ *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionMessageComponent {
		return
	}
	user := i.User
	if user == nil && i.Member != nil {
		user = i.Member.User
	}
	if user == nil || user.Bot {
		return
	}
	if i.GuildID != "" || i.Message == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	defer cancel()

	now := b.now()
	if !b.mem.firstSeen("int:"+i.ID, now) {
		return
	}
	// 先に初期応答を返す（3秒以内）。以降の更新は元のメッセージへの編集で行う。
	if err := b.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}, discordgo.WithContext(ctx)); err != nil {
		b.log.Warn("ボタンへの初期応答に失敗")
		return
	}

	appUser, err := b.co.UserByDiscordID(ctx, user.ID)
	if err != nil {
		b.editInteraction(ctx, i, msgNoAccount+webLink(b.base, ""))
		return
	}
	us, ok := b.mem.begin(appUser.ID, now)
	if !ok {
		// 入力処理中のボタンでは保存しない。
		b.followUp(ctx, i, msgBusy)
		return
	}
	defer func() { b.mem.end(appUser.ID, b.now()) }()

	kind, token, arg := splitCustomID(i.MessageComponentData().CustomID)
	switch kind {
	case "respond":
		b.onTaskButton(ctx, i, appUser.ID, us, token, arg)
	case "target":
		b.onTargetButton(ctx, i, appUser.ID, us, token, arg)
	case "save":
		b.onSaveButton(ctx, i, appUser.ID, us, token)
	case "cancel":
		b.onCancelButton(ctx, i, us, token)
	case "wsave":
		b.onWeeklySaveButton(ctx, i, appUser.ID, us, token)
	case "wcancel":
		b.onWeeklyCancelButton(ctx, i, us, token)
	case "act":
		// 通知のボタン。token はアクションID、arg は decision。対象・期限・操作者は
		// ResolveNotifyAction が再検証する。custom_id の値は認可根拠にしない。
		b.onNotifyActionButton(ctx, i, appUser.ID, us, token, arg)
	}
}

// onTargetButton は対象の開催回を確定する。候補集合と本人に束縛し、所属を確かめ直す。
func (b *Bot) onTargetButton(ctx context.Context, i *discordgo.InteractionCreate, userID string, us *userSession, token, sessionID string) {
	conv := us.conv
	if conv == nil || conv.selectID == "" || conv.selectID != token {
		b.editInteraction(ctx, i, msgNoDraft)
		return
	}
	var target *coord.DialogTarget
	for idx := range conv.targets {
		if conv.targets[idx].SessionID == sessionID {
			target = &conv.targets[idx]
		}
	}
	if target == nil {
		b.editInteraction(ctx, i, msgNoDraft)
		return
	}
	b.editInteraction(ctx, i, "【"+escape(target.Title)+"】\nこの回を選びました。")
	// 選択前の発言は保持していないので、あらためて入力してもらう。
	tasks := conv.selectTasks
	if b.startConversation(ctx, i.ChannelID, userID, us, *target, !tasks) && tasks {
		b.replyTasks(ctx, i.ChannelID, userID, us)
	}
}

// onSaveButton は表示した下書きを保存する。表示と違う値を承認させない。
func (b *Bot) onSaveButton(ctx context.Context, i *discordgo.InteractionCreate, userID string, us *userSession, token string) {
	conv := us.conv
	if conv == nil || conv.confirm == nil || conv.confirm.draftID != token ||
		conv.draftID != token || conv.confirm.messageID != i.Message.ID || conv.confirm.channelID != i.ChannelID {
		b.editInteraction(ctx, i, msgNoDraft)
		return
	}
	c := conv.confirm
	if _, err := b.co.SaveDialogPreparation(ctx, userID, c.state, idemKey(userID, c)); err != nil {
		conv.confirm = nil
		b.editInteraction(ctx, i, confirmText(conv, c.lines)+"\n（保存できませんでした）")
		b.replyError(ctx, i.ChannelID, userID, us, err)
		return
	}
	b.editInteraction(ctx, i, conversationHeader(conv)+"\n"+escapeLines(c.lines)+"\n\n"+msgSaved)
	// 保存が済んだら会話を終える。未保存の下書きも確認も残さない。
	us.conv = nil
}

func (b *Bot) onCancelButton(ctx context.Context, i *discordgo.InteractionCreate, us *userSession, token string) {
	conv := us.conv
	if conv == nil || conv.confirm == nil || conv.confirm.draftID != token {
		b.editInteraction(ctx, i, msgNoDraft)
		return
	}
	lines := conv.confirm.lines
	conv.confirm = nil
	conv.draftID = newToken()
	b.editInteraction(ctx, i, confirmText(conv, lines)+"\n（"+msgCanceled+"）")
}

// splitCustomID は "操作:世代[:引数]" を分ける。custom_id に値は埋めない。
func splitCustomID(id string) (kind, token, arg string) {
	parts := strings.SplitN(id, ":", 3)
	switch len(parts) {
	case 2:
		return parts[0], parts[1], ""
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		return "", "", ""
	}
}

// send は DM へ定型文を送る。意図しないメンションは allowed_mentions で抑止する。
func (b *Bot) send(ctx context.Context, channelID, content string, components []discordgo.MessageComponent) {
	if _, err := b.sendComplex(ctx, channelID, content, components); err != nil {
		b.log.Warn("DM の送信に失敗")
	}
}

func (b *Bot) sendComplex(ctx context.Context, channelID, content string, components []discordgo.MessageComponent) (*discordgo.Message, error) {
	return b.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         clipContent(content),
		Components:      components,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	}, discordgo.WithContext(ctx))
}

// editInteraction は押されたメッセージを書き換え、ボタンを外して再押下を防ぐ。
func (b *Bot) editInteraction(ctx context.Context, i *discordgo.InteractionCreate, content string) {
	text := clipContent(content)
	empty := []discordgo.MessageComponent{}
	if _, err := b.dg.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:         &text,
		Components:      &empty,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	}, discordgo.WithContext(ctx)); err != nil {
		b.log.Warn("ボタンの応答の更新に失敗")
	}
}

func (b *Bot) followUp(ctx context.Context, i *discordgo.InteractionCreate, content string) {
	if _, err := b.dg.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content:         clipContent(content),
		Flags:           discordgo.MessageFlagsEphemeral,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	}, discordgo.WithContext(ctx)); err != nil {
		b.log.Warn("ボタンへの追加応答に失敗")
	}
}

// disableButtons は表示済みの確認を無効にする。訂正を受けた時点で古い確認は押せなくする。
func (b *Bot) disableButtons(ctx context.Context, channelID, messageID, content string) {
	text := clipContent(content)
	empty := []discordgo.MessageComponent{}
	if _, err := b.dg.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: channelID, ID: messageID, Content: &text, Components: &empty,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}},
	}, discordgo.WithContext(ctx)); err != nil {
		b.log.Warn("古い確認の無効化に失敗")
	}
}

// clipContent は Discord のメッセージ長の上限に収める。
func clipContent(s string) string {
	const max = 2000
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
