package discord

import (
	"context"
	"errors"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
)

// 普段の空き時間の対話（.agent/kasa/decisions/discord-availability-home-refresh.md）。
// 開催回を束縛せず、本人の自由文 → 解釈（ここだけ LLM）→ 確認 → 本人の保存ボタンの順で進む。
// 解釈は下書きだけを返し、保存は既存の PutWeeklyAvailability を呼ぶ。

const (
	msgWeeklyAsk      = "普段の空き時間を教えてください（例：「毎週水曜と金曜の19時から22時」）。この内容は今後の全ての回の候補づくりに使われます。"
	msgWeeklyUnclear  = "うまく読み取れませんでした。曜日と時間帯をもう少しはっきり書いてください（例：「毎週水曜と金曜の19時から22時」）。"
	msgWeeklySaved    = "普段の空き時間を保存しました。"
	msgWeeklyNoDraft  = "この確認は古くなっています。もう一度、空き時間を送ってください。"
	msgWeeklyCanceled = "下書きを取り消しました。"
)

// beginWeeklyDialog は普段の空き時間の入力を始める（対象は本人固定。開催回を束縛しない）。
func (b *Bot) beginWeeklyDialog(ctx context.Context, channelID string, us *userSession) {
	us.conv = nil
	us.weekly = &weeklyState{}
	b.send(ctx, channelID, msgWeeklyAsk, nil)
}

// continueWeekly は自由文を解釈し、確認と保存ボタンを出す。保存はまだ行わない。
func (b *Bot) continueWeekly(ctx context.Context, channelID, userID string, us *userSession, text string) {
	out, err := b.co.InterpretWeeklyAvailability(ctx, userID, apitypes.WeeklyAvailabilityInterpretationInput{Text: text})
	if err != nil {
		b.replyWeeklyError(ctx, channelID, us, err)
		return
	}
	if out.NeedsFollowup || len(out.Availability.Windows) == 0 {
		b.countMiss(ctx, channelID, us, msgWeeklyUnclear)
		return
	}
	us.misses = 0
	lines := weeklyLines(out.Availability.Windows)
	draftID := newToken()
	content := "普段の空き時間の下書きです。\n" + escapeLines(lines) + "\n\n合っていれば保存してください。違う内容にしたい場合は、もう一度送信し直してください。"
	msg, err := b.sendComplex(ctx, channelID, content, weeklyButtons(draftID))
	if err != nil {
		b.log.Warn("週間空き時間の確認の送信に失敗")
		return
	}
	us.weekly.confirm = &weeklyConfirmation{
		draftID: draftID, timezone: out.Availability.Timezone, windows: out.Availability.Windows,
		lines: lines, channelID: channelID, messageID: msg.ID,
	}
}

func weeklyLines(windows []apitypes.WeeklyWindow) []string {
	out := make([]string, 0, len(windows))
	for _, w := range windows {
		out = append(out, weeklyDayJA(w.Weekday)+"曜 "+w.Start+"〜"+w.End)
	}
	return out
}

func weeklyDayJA(weekday int) string {
	labels := [...]string{"", "月", "火", "水", "木", "金", "土", "日"}
	if weekday < 1 || weekday > 7 {
		return "?"
	}
	return labels[weekday]
}

func weeklyButtons(draftID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "この内容で保存する", Style: discordgo.PrimaryButton, CustomID: "wsave:" + draftID},
		discordgo.Button{Label: "取り消す", Style: discordgo.SecondaryButton, CustomID: "wcancel:" + draftID},
	}}}
}

// clearWeeklyDraft は表示済みの確認を無効にする。訂正を受けた時点で古い確認は押せなくする。
func (b *Bot) clearWeeklyDraft(ctx context.Context, us *userSession) {
	if us.weekly == nil || us.weekly.confirm == nil {
		return
	}
	c := us.weekly.confirm
	b.disableButtons(ctx, c.channelID, c.messageID, escapeLines(c.lines)+"\n（この確認は無効になりました）")
	us.weekly.confirm = nil
}

func (b *Bot) replyWeeklyError(ctx context.Context, channelID string, us *userSession, err error) {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		b.log.Error("週間空き時間の解釈に失敗")
		b.send(ctx, channelID, msgUnavailable+webLink(b.base, ""), nil)
		return
	}
	b.send(ctx, channelID, ae.Message+webLink(b.base, ""), nil)
}

// onWeeklySaveButton は表示した下書きを保存する。表示と違う値を承認させない。
func (b *Bot) onWeeklySaveButton(ctx context.Context, i *discordgo.InteractionCreate, userID string, us *userSession, token string) {
	if us.weekly == nil || us.weekly.confirm == nil || us.weekly.confirm.draftID != token ||
		us.weekly.confirm.messageID != i.Message.ID || us.weekly.confirm.channelID != i.ChannelID {
		b.editInteraction(ctx, i, msgWeeklyNoDraft)
		return
	}
	c := us.weekly.confirm
	windows := c.windows
	// PutWeeklyAvailability は全置換で、何度送っても同じ結果になる（更新日時だけが新しくなる）ため
	// Idempotency-Key は不要（Web の PUT と同じ契約）。
	if _, err := b.co.PutWeeklyAvailability(ctx, userID, apitypes.PutWeeklyAvailabilityInput{Timezone: c.timezone, Windows: &windows}); err != nil {
		us.weekly.confirm = nil
		b.editInteraction(ctx, i, escapeLines(c.lines)+"\n（保存できませんでした）")
		b.replyWeeklyError(ctx, i.ChannelID, us, err)
		return
	}
	b.editInteraction(ctx, i, escapeLines(c.lines)+"\n\n"+msgWeeklySaved)
	us.weekly = nil
}

func (b *Bot) onWeeklyCancelButton(ctx context.Context, i *discordgo.InteractionCreate, us *userSession, token string) {
	if us.weekly == nil || us.weekly.confirm == nil || us.weekly.confirm.draftID != token {
		b.editInteraction(ctx, i, msgWeeklyNoDraft)
		return
	}
	lines := us.weekly.confirm.lines
	us.weekly = nil
	b.editInteraction(ctx, i, escapeLines(lines)+"\n（"+msgWeeklyCanceled+"）")
}
