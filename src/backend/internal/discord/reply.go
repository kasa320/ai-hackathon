package discord

import (
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// Bot の発話はすべてここで組み立てる定型文。LLM の出力文字列は転記しない。
const (
	msgBusy        = "前の入力を処理中です。応答が届いてから、もう一度送ってください。"
	msgNoAccount   = "この機能を使うには、先にWebでログインしてください。"
	msgNoTargets   = "いま変更できる開催回がありません。"
	msgPickTarget  = "どの回の予定を変更しますか？"
	msgReenter     = "対象を選びました。変更したい内容をもう一度送ってください。"
	msgCanceled    = "下書きを取り消しました。"
	msgNoDraft     = "この確認は古くなっています。もう一度、変更したい内容を送ってください。"
	msgSaved       = "保存しました。"
	msgConflict    = "開催回の情報が更新されたため、最新の内容で確認し直します。変更したい内容をもう一度送ってください。"
	msgCooldown    = "うまく読み取れませんでした。しばらくしてからやり直すか、Webから入力してください。"
	msgUnavailable = "いまは応答できません。Webから入力してください。"
	msgStarted     = "この開催回は開催時刻を過ぎているため、変更できません。"
	msgConfirmAsk  = "この内容で保存しますか？"
	msgOutOfScope  = "その内容は参加条件の項目では扱えません。"
	msgHelp        = "「予定」で今の内容、「取消」で下書きの取り消し、「変更」で対象の選び直しができます。"
)

// outOfScopeHint は扱えない依頼の種類ごとの案内。分類は案内にだけ使い、認可の代わりにはしない。
func outOfScopeHint(kind string) string {
	switch kind {
	case coord.OutOfScopeScheduleChange:
		return msgOutOfScope + "日程の変更は管理者に相談してください。"
	case coord.OutOfScopePartialAttendance:
		return msgOutOfScope + "途中参加・途中退出は、参加／欠席のどちらかで答えてください。"
	case coord.OutOfScopeOtherMember:
		return msgOutOfScope + "登録できるのはご本人の予定だけです。"
	default:
		return msgOutOfScope + "参加可否・読んできた範囲・説明できる範囲と時間だけを送ってください。"
	}
}

func webLink(baseURL, sessionID string) string {
	if baseURL == "" {
		return ""
	}
	if sessionID == "" {
		return "\n" + baseURL
	}
	return "\n" + baseURL + "/session.html?id=" + sessionID
}

// sessionHeader は対象の開催回を毎回示す。取り違えたまま進めないようにする。
func sessionHeader(title string, startsAt time.Time) string {
	return "【" + escape(title) + "】" + formatClock(startsAt)
}

func formatClock(t time.Time) string {
	return t.In(time.FixedZone("JST", 9*60*60)).Format("1/2 15:04")
}

// escape は既存の可変文字列（節名・開催回名）の装飾記号を打ち消す。
func escape(s string) string {
	r := strings.NewReplacer("*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`", "|", "\\|", ">", "\\>")
	return r.Replace(s)
}

func escapeLines(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, escape(l))
	}
	return strings.Join(out, "\n")
}

// confirmText は確認表示。開催回・日時・全項目を含み、正規化済みの値をそのまま載せる。
func confirmText(conv *conversation, lines []string) string {
	return sessionHeader(conv.title, conv.startsAt) + "\n" + escapeLines(lines) + "\n\n" + msgConfirmAsk
}

// confirmButtons は確認のボタン。custom_id には操作と下書きの世代だけを入れ、値は埋めない。
func confirmButtons(draftID string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "保存する", Style: discordgo.PrimaryButton, CustomID: "save:" + draftID},
		discordgo.Button{Label: "取り消す", Style: discordgo.SecondaryButton, CustomID: "cancel:" + draftID},
	}}}
}

// targetButtons は対象の開催回を選ぶボタン。押下時に所属と変更可能性を確かめ直す。
func targetButtons(selectID string, targets []coord.DialogTarget) []discordgo.MessageComponent {
	var rows []discordgo.MessageComponent
	var row []discordgo.MessageComponent
	for _, t := range targets {
		label := t.Title + "（" + formatClock(t.StartsAt) + "）"
		if t.HasOpenTask {
			label = "未回答：" + label
		}
		row = append(row, discordgo.Button{Label: trimLabel(label), Style: discordgo.SecondaryButton, CustomID: "target:" + selectID + ":" + t.SessionID})
		if len(row) == 5 {
			rows = append(rows, discordgo.ActionsRow{Components: row})
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, discordgo.ActionsRow{Components: row})
	}
	return rows
}

// trimLabel はボタンの表示名を Discord の上限（80文字）に収める。
func trimLabel(s string) string {
	const max = 80
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// maxTargetButtons は1つのメッセージに出す選択肢の数（5個×5行）。
const maxTargetButtons = 25
