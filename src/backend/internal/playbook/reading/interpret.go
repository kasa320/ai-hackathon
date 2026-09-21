package reading

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

var (
	_ coord.PreparationInterpreter = Playbook{}
	_ coord.DraftInterpreter       = Playbook{}
)

// 参加条件の項目名（PreparationData のキー）。unclear に入れてよい名前はこれと attendance だけ。
// 聞き取るのは参加と参加できる時間帯だけ。出られない日・担当の辞退は本人が言ったときだけ反映する。
const SlotDeclined = "declined_presentation"

// slotOrder は聞き取りと表示の順。unclear もこの順で返す。
var slotOrder = []string{coord.SlotAttendance, SlotSchedule, SlotUnavailable, SlotDeclined}

// PreparationSlots は data の項目名。
func (Playbook) PreparationSlots() []string {
	return []string{SlotSchedule, SlotUnavailable, SlotDeclined}
}

// interpretContext は抽出に必要な最小限の判断材料。他のメンバーの回答・担当履歴は含めない。
// 抽出用の AI は本人への質問文も書くので、本人が画面で見られる以上の情報は渡さない。
type interpretContext struct {
	DurationMinutes int    `json:"duration_minutes"`
	ScheduleStatus  string `json:"schedule_status"`
	PeriodStart     string `json:"period_start"`
	PeriodEnd       string `json:"period_end"`
	Today           string `json:"today"`
}

// InterpretContext は会の長さと日程の決まり具合だけを返す。発言者本人の値を決めるのに他人の情報は要らない。
func (Playbook) InterpretContext(_ context.Context, s coord.Snapshot) (json.RawMessage, error) {
	return json.Marshal(interpretContext{
		DurationMinutes: s.DurationMinutes,
		ScheduleStatus:  s.ScheduleStatus, PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd,
		Today: s.Now.In(jst).Format(dateLayout),
	})
}

// PreparationSchema は PreparationData の JSON Schema。
//
// 配列の maxItems は Gemini の制約付き生成が受け付ける範囲に収める。maxItems は
// 「今いくつ出したか」を数える状態を要求し、要素側の状態と掛け算になるため、
// 大きすぎると Gemini が "constraint has too many states for serving" として
// HTTP 400 を返す。2026-09-21 の実測では 3 つの配列を 40 以上にすると
// コンパイルが走った呼び出しはすべて失敗し、21 以下は 12 回すべて成功した。
// 上限を超える入力は Web のフォームから登録できる（サーバー側の検証は 60 件まで許す）。
func (Playbook) PreparationSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": [],
  "properties": {
    "unavailable_dates": {"type":"array","maxItems":21,"items":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},"description":"本人が自分から言った、終日参加できない日"},
    "schedule": {
      "type":["object","null"], "additionalProperties":false,
      "required":["status","weekly_windows","date_windows","max_duration_minutes"],
      "properties":{
        "status":{"type":"string","enum":["provided","unknown","unavailable"]},
        "max_duration_minutes":{"type":"integer","minimum":0,"maximum":480,"description":"本人が自分から言った1回の参加時間の上限。言っていなければ0"},
        "weekly_windows":{"type":"array","maxItems":21,"items":{"type":"object","additionalProperties":false,"required":["weekday","start","end"],"properties":{"weekday":{"type":"integer","minimum":0,"maximum":6},"start":{"type":"string"},"end":{"type":"string"}}}},
        "date_windows":{"type":"array","maxItems":14,"items":{"type":"object","additionalProperties":false,"required":["date","start","end"],"properties":{"date":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"}}}}
      }
    },
    "declined_presentation": {"type": "boolean", "description": "本人が「今回は説明の担当はできない」とはっきり言ったときだけ true"}
  }
}`)
}

func (Playbook) InterpretInstructions() string {
	return `取り出すのは、発言者本人が輪読会に参加するかと、参加できる時間帯だけです。準備状況（読んだ範囲など）は聞きません。

- scheduleは本人の参加できる時間帯。時刻はJSTのHH:mm、曜日は日曜0〜土曜6。日をまたぐ場合は日ごとに分割する。
- weekly_windowsは毎週の曜日と時間帯、date_windowsは特定の日の時間帯（その日の週間設定を置換）。
- 「平日の夜」「土日の午後」のような言い方は、会の長さ（duration_minutes）が入る具体的な時間帯に直してよい。目安：朝=9:00-12:00、昼=12:00-14:00、午後=13:00-18:00、夕方=17:00-19:00、夜=19:00-22:00、平日=月〜金、週末=土日。
- 目安で決めた場合も、本人があとで確認・修正できるので、ためらわずに値を入れる。ただし「いつでも」「未定」のように範囲が決まらないものは推測しない。
- schedule.statusは時間帯が読み取れればprovided、まだ分からないならunknown、期間中は参加できないならunavailable。後二者は時間帯を空にする。
- max_duration_minutesは本人が「1時間まで」のように自分から言ったときだけ入れる。言っていなければ0。こちらから聞かない。
- unavailable_datesは本人が自分から言った終日参加できない日だけ。こちらから聞かない。
- 参加できる時間帯を答えた人は参加（attendance=attending）とみなす。「欠席」「今回は無理」など参加しないと読み取れるときだけabsent。
- 日程未定の回（schedule_status=proposed）で参加するのに時間帯が読み取れなければ、scheduleをunclearに入れる。日時が決まっている回は時間帯を聞かない。
- declined_presentationは「今回は説明（担当）は無理」とはっきり言ったときだけtrue。準備の度合いは聞かない。
- 現在の値のうち発言が触れていない項目は変更しない。理由・原文・指示を値として転記しない。
- 本人の時間条件は抽出できるが、会全体の日時・長さ・参加ルールの変更や他人の代理回答・承認はできない。会全体の変更や他の人の代理回答はout_of_scopeに分類する。`
}

// 仮の抽出で使う手がかり。
var (
	absentWords = []string{"欠席", "参加できません", "参加できない", "行けません", "行けない", "休みます"}
	yesWords    = []string{"はい", "できます", "大丈夫", "お願いします", "そうです", "うん", "参加します", "出ます"}
	noWords     = []string{"いいえ", "できません", "無理", "しません"}
	declineWord = []string{"担当は無理", "担当できません", "説明は無理", "説明できません", "発表は無理"}
)

// DraftInterpret は LLM を使わずに規則だけで抽出する（AGENT_MODE=fake 用）。
// 対話のときは req.Current を出発点にし、今回の発言で読み取れた項目だけを上書きする。
// 時間帯は決まった書き方（例：水 20:00-22:00）だけを読む。読み取れない項目は unclear に残す。
// 規則だけで「扱えない依頼」を見分けることはできないため、out_of_scope は付けない。
func (Playbook) DraftInterpret(_ context.Context, req coord.InterpretRequest) (coord.Interpretation, error) {
	s := req.Snapshot
	text := strings.TrimSpace(req.Text)

	// 出発点は、対話中なら現在の値、初回なら参加と時間帯が未確定。
	attendance := coord.AttendanceAttending
	var d PreparationData
	unclear := newIDSet([]string{coord.SlotAttendance, SlotSchedule})
	if req.Current != nil {
		attendance = req.Current.Attendance
		if len(req.Current.Data) > 0 {
			if err := json.Unmarshal(req.Current.Data, &d); err != nil {
				return coord.Interpretation{}, err
			}
		}
		unclear = newIDSet(req.Current.Unclear)
	}
	d.UnavailableDates = nonNil(d.UnavailableDates)
	if s.ScheduleStatus != coord.ScheduleProposed {
		delete(unclear, SlotSchedule)
	}

	switch {
	case containsAny(text, absentWords):
		attendance = coord.AttendanceAbsent
		delete(unclear, coord.SlotAttendance)
		delete(unclear, SlotSchedule)
	case s.ScheduleStatus == coord.ScheduleProposed:
		if a, ok := parseScheduleText(text); ok {
			// 時間帯を答えたら参加とみなす
			d.Schedule = a
			attendance = coord.AttendanceAttending
			delete(unclear, SlotSchedule)
			if a.Status != "unavailable" {
				delete(unclear, coord.SlotAttendance)
			}
		} else if containsAny(text, yesWords) {
			attendance = coord.AttendanceAttending
			delete(unclear, coord.SlotAttendance)
		}
	case containsAny(text, yesWords):
		attendance = coord.AttendanceAttending
		delete(unclear, coord.SlotAttendance)
	case req.Pending == coord.SlotAttendance && containsAny(text, noWords):
		attendance = coord.AttendanceAbsent
		delete(unclear, coord.SlotAttendance)
	}
	if containsAny(text, declineWord) {
		d.DeclinedPresentation = true
	}
	if attendance == coord.AttendanceAbsent {
		d.DeclinedPresentation = false
	}

	list := orderedSlots(unclear)
	return coord.Interpretation{
		Attendance:    attendance,
		Data:          mustJSON(d),
		Unclear:       list,
		NeedsFollowup: len(list) > 0,
	}, nil
}

// orderedSlots は未確定の項目名を決まった順に並べる。
func orderedSlots(set idSet) []string {
	out := []string{}
	for _, name := range slotOrder {
		if set.has(name) {
			out = append(out, name)
		}
	}
	return out
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// toASCIIDigits は全角数字を半角に直す。
func toASCIIDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '０' && r <= '９' {
			r = r - '０' + '0'
		}
		b.WriteRune(r)
	}
	return b.String()
}

var _ coord.PreparationPrompter = Playbook{}

// SlotQuestion は未確定の項目を聞く既定の文。抽出用の AI が質問文を返さなかったときに使う。
// 会話調にし、決まった書式は求めない（自由に答えてもらい、AI が値を取り出す）。
func (Playbook) SlotQuestion(s coord.Snapshot, slot string) string {
	switch slot {
	case coord.SlotAttendance:
		if s.ScheduleStatus == coord.ScheduleProposed {
			return fmt.Sprintf("%s〜%sのどこかで開く予定です。参加できそうなら、都合のいい曜日や時間帯を教えてください（「平日の夜」「土曜の午後」のような書き方で大丈夫です）。今回は難しければ「欠席」と送ってください。",
				shortDate(s.PeriodStart), shortDate(s.PeriodEnd))
		}
		return fmt.Sprintf("%sからの回に参加できそうですか？", formatDay(s.StartsAt))
	case SlotSchedule:
		return "参加できそうな曜日や時間帯を教えてください。「水曜の夜」「10月7日の20時以降」のような書き方で大丈夫です。まだ分からなければ「未定」で構いません。"
	}
	return ""
}

// shortDate は YYYY-MM-DD を「10/7(水)」の形にする。読めなければそのまま返す。
func shortDate(d string) string {
	t, err := time.ParseInLocation(dateLayout, d, jst)
	if err != nil {
		return d
	}
	return fmt.Sprintf("%d/%d(%s)", int(t.Month()), t.Day(), weekdayJA[int(t.Weekday())])
}
