package discord

import (
	"context"
	"encoding/json"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/httpx"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func (b *Bot) replyTasks(ctx context.Context, channelID, userID string, us *userSession) {
	if us.conv == nil || us.conv.sessionID == "" {
		if !b.pickTarget(ctx, channelID, userID, us) {
			if us.conv != nil {
				us.conv.selectTasks = true
			}
			return
		}
	}
	cards, err := b.co.DialogTasks(ctx, userID, us.conv.sessionID)
	if err != nil {
		b.replyError(ctx, channelID, userID, us, err)
		return
	}
	us.conv.taskCards = map[string]taskCard{}
	if len(cards) == 0 {
		b.send(ctx, channelID, "今は回答待ちの案がありません。"+webLink(b.base, us.conv.sessionID), nil)
		return
	}
	for _, card := range cards {
		content := escapeLines(card.Lines)
		if len([]rune(content)) > 1800 {
			b.send(ctx, channelID, "案の全体をWebで確認して回答してください。"+webLink(b.base, us.conv.sessionID), nil)
			continue
		}
		token := newToken()
		var buttons []discordgo.MessageComponent
		for _, d := range card.Task.AllowedDecisions {
			label, style := "", discordgo.SecondaryButton
			switch d {
			case "approve":
				label = "この内容に同意する"
				if card.Task.Kind == "owner_approval" {
					label = "管理者として承認する"
				}
				style = discordgo.PrimaryButton
			case "reject":
				label = "同意できない"
				if card.Task.Kind == "owner_approval" {
					label = "承認しない"
				}
			case "accept":
				label = "担当を引き受ける"
				style = discordgo.PrimaryButton
			case "decline":
				label = "担当を辞退する"
			default:
				continue
			}
			buttons = append(buttons, discordgo.Button{Label: label, Style: style, CustomID: "respond:" + token + ":" + d})
		}
		msg, err := b.sendComplex(ctx, channelID, content, []discordgo.MessageComponent{discordgo.ActionsRow{Components: buttons}})
		if err == nil {
			us.conv.taskCards[token] = taskCard{value: card, channelID: channelID, messageID: msg.ID}
		}
	}
}

func (b *Bot) onTaskButton(ctx context.Context, i *discordgo.InteractionCreate, userID string, us *userSession, token, decision string) {
	if us.conv == nil {
		b.editInteraction(ctx, i, msgNoDraft)
		return
	}
	card, ok := us.conv.taskCards[token]
	if !ok || card.channelID != i.ChannelID || card.messageID != i.Message.ID {
		b.editInteraction(ctx, i, msgNoDraft)
		return
	}
	allowed := false
	for _, d := range card.value.Task.AllowedDecisions {
		if d == decision {
			allowed = true
		}
	}
	if !allowed {
		b.followUp(ctx, i, msgNoDraft)
		return
	}
	t := card.value.Task
	in := apitypes.TaskResponseInput{Decision: decision, ProposalID: t.ProposalID, ProposalVersion: t.ProposalVersion}
	body, _ := json.Marshal(in)
	idem := &store.IdemKey{UserID: userID, Key: token, Method: "POST", Path: "/discord/tasks/" + t.ID + "/responses", BodyHash: httpx.BodyHash(body)}
	if _, err := b.co.RespondTask(ctx, userID, t.ID, in, idem); err != nil {
		delete(us.conv.taskCards, token)
		b.editInteraction(ctx, i, "この案には回答できません。最新の案を「回答」で確認してください。")
		return
	}
	delete(us.conv.taskCards, token)
	b.editInteraction(ctx, i, escapeLines(card.value.Lines)+"\n\n回答を保存しました。")
}
