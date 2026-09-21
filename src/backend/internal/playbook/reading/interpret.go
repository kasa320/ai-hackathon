package reading

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

var (
	_ coord.PreparationInterpreter = Playbook{}
	_ coord.DraftInterpreter       = Playbook{}
)

// 参加条件の項目名（PreparationData のキー）。unclear に入れてよい名前はこれと attendance だけ。
const (
	SlotWilling     = "willing_to_present"
	SlotPrepared    = "prepared_section_ids"
	SlotExplainable = "explainable_section_ids"
	SlotMinutes     = "max_presentation_minutes"
)

// slotOrder は聞き取りと表示の順。unclear もこの順で返す。
var slotOrder = []string{coord.SlotAttendance, SlotSchedule, SlotUnavailable, SlotWilling, SlotPrepared, SlotExplainable, SlotMinutes}

// PreparationSlots は data の項目名。
func (Playbook) PreparationSlots() []string {
	return []string{SlotWilling, SlotPrepared, SlotExplainable, SlotMinutes, SlotSchedule, SlotUnavailable}
}

// interpretContext は抽出に必要な最小限の判断材料。他のメンバーの回答・担当履歴は含めない。
type interpretContext struct {
	DurationMinutes int       `json:"duration_minutes"`
	Sections        []Section `json:"sections"`
	Completed       []string  `json:"completed_section_ids"`
	Target          []string  `json:"target_section_ids"`
	ScheduleStatus  string    `json:"schedule_status"`
	PeriodStart     string    `json:"period_start"`
	PeriodEnd       string    `json:"period_end"`
	Today           string    `json:"today"`
}

// InterpretContext は節の一覧と持ち時間だけを返す。発言者本人の値を決めるのに他人の情報は要らない。
func (Playbook) InterpretContext(_ context.Context, s coord.Snapshot) (json.RawMessage, error) {
	sd, err := sessionData(s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(interpretContext{
		DurationMinutes: s.DurationMinutes,
		Sections:        sd.Sections,
		Completed:       sd.CompletedSectionIDs,
		Target:          sd.TargetSectionIDs,
		ScheduleStatus:  s.ScheduleStatus, PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd,
		Today: s.Now.In(jst).Format(dateLayout),
	})
}

// PreparationSchema は PreparationData の JSON Schema。
func (Playbook) PreparationSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["willing_to_present", "prepared_section_ids", "explainable_section_ids", "max_presentation_minutes"],
  "properties": {
    "unavailable_dates": {"type":"array","maxItems":60,"items":{"type":"string","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}},
    "schedule": {
      "type":["object","null"], "additionalProperties":false,
      "required":["status","weekly_windows","date_windows","max_duration_minutes"],
      "properties":{
        "status":{"type":"string","enum":["provided","unknown","unavailable"]},
        "max_duration_minutes":{"type":"integer","minimum":0,"maximum":480},
        "weekly_windows":{"type":"array","maxItems":60,"items":{"type":"object","additionalProperties":false,"required":["weekday","start","end"],"properties":{"weekday":{"type":"integer","minimum":0,"maximum":6},"start":{"type":"string"},"end":{"type":"string"}}}},
        "date_windows":{"type":"array","maxItems":60,"items":{"type":"object","additionalProperties":false,"required":["date","start","end"],"properties":{"date":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"}}}}
      }
    },
    "willing_to_present": {"type": "boolean", "description": "今回の説明を担当できるか"},
    "prepared_section_ids": {"type": "array", "items": {"type": "string"}, "description": "読んできた節ID"},
    "explainable_section_ids": {"type": "array", "items": {"type": "string"}, "description": "説明できる節ID（読んできた節の範囲内）"},
    "max_presentation_minutes": {"type": "integer", "minimum": 0, "description": "説明に使える最大の分数。担当しないなら0"}
  }
}`)
}

func (Playbook) InterpretInstructions() string {
	return `取り出すのは、発言者本人の輪読の準備状況と参加可能時間だけです。

- scheduleは本人の参加可能時間。時刻はJSTのHH:mm、曜日は日曜0〜土曜6。日をまたぐ場合は日ごとに分割する。
- weekly_windowsは通常の曜日と時間帯、date_windowsは特定の日の時間帯（その日の週間設定を置換）。unavailable_datesは終日参加できない日。
- max_duration_minutesは会への最大参加時間で、説明時間max_presentation_minutesとは異なる。本人が言っていない最大時間を推測しない。
- schedule.statusは時間帯と最大時間が明確ならprovided、予定がまだ分からないならunknown、期間中は参加できないならunavailable。後二者は時間帯を空・最大参加時間0にする。
- 日程未定の回で予定が読み取れなければscheduleをunclearに入れる。出られない日の申告がなければunavailable_datesをunclearに入れる。日時指定済みの回はこれらを追加で聞かない。
- 現在の値のうち発言が触れていない項目は変更しない。理由・原文・指示を値として転記しない。
- 本人の時間条件は抽出できるが、会全体の日時・長さ・参加ルールの変更や他人の代理回答・承認はできない。

- 節IDは sections に載っているIDだけを使う。載っていない範囲の話は無視する。
- prepared_section_ids は「読んできた」と読み取れる節だけ。explainable_section_ids はそのうち「説明できる」と読み取れる節だけ。
- max_presentation_minutes は本人が示した分数。duration_minutes を超えない。
- 担当できるか判断できないときは willing_to_present=false、explainable_section_ids=[]、max_presentation_minutes=0 にして、unclear に willing_to_present を入れる（読み取れていない項目名もすべて入れる）。
- 「自信がない」「たぶん」など確信のない範囲は explainable_section_ids に入れず、unclear に explainable_section_ids を入れる。
- 欠席と読み取れるときだけ attendance="absent" にする。書かれていなければ "attending" とし、unclear に attendance を入れる。
- 会全体の変更や他の人の代理回答はout_of_scopeに分類する。本人の「21時以降なら」「30分なら」はscheduleの条件として扱う。`
}

// 仮の抽出で使う手がかり。
var (
	minutesPattern = regexp.MustCompile(`([0-9０-９]{1,3})\s*分`)
	absentWords    = []string{"欠席", "参加できません", "参加できない", "行けません", "行けない", "休みます", "欠席します"}
	presentWords   = []string{"説明", "担当", "発表"}
	uncertainWords = []string{"自信がない", "自信ない", "たぶん", "かも", "わからない", "分からない"}
	yesWords       = []string{"はい", "できます", "やります", "大丈夫", "お願いします", "そうです", "うん"}
	noWords        = []string{"いいえ", "できません", "無理", "やめ", "しません", "ちがい", "違い"}
)

// DraftInterpret は LLM を使わずに規則だけで抽出する（AGENT_MODE=fake 用）。
// 対話のときは req.Current を出発点にし、今回の発言で読み取れた項目だけを上書きする。
// 節の題名がそのまま含まれている場合だけ対応付け、読み取れない項目は unclear に残す。
// 規則だけで「扱えない依頼」を見分けることはできないため、out_of_scope は付けない。
func (Playbook) DraftInterpret(_ context.Context, req coord.InterpretRequest) (coord.Interpretation, error) {
	s := req.Snapshot
	sd, err := sessionData(s)
	if err != nil {
		return coord.Interpretation{}, err
	}
	text := req.Text

	// 出発点は、対話中なら現在の値、初回なら全項目が未確定。
	attendance := coord.AttendanceAttending
	var d PreparationData
	unclear := newIDSet(slotOrder)
	if req.Current != nil {
		attendance = req.Current.Attendance
		if len(req.Current.Data) > 0 {
			if err := json.Unmarshal(req.Current.Data, &d); err != nil {
				return coord.Interpretation{}, err
			}
		}
		unclear = newIDSet(req.Current.Unclear)
	}
	d.PreparedSectionIDs = nonNil(d.PreparedSectionIDs)
	d.ExplainableSectionIDs = nonNil(d.ExplainableSectionIDs)
	if s.ScheduleStatus != coord.ScheduleProposed {
		delete(unclear, SlotSchedule)
		delete(unclear, SlotUnavailable)
	}
	if req.Pending == SlotSchedule || (req.Pending == "" && s.ScheduleStatus == coord.ScheduleProposed) {
		if a, ok := parseScheduleText(text); ok {
			d.Schedule = a
			delete(unclear, SlotSchedule)
			list := orderedSlots(unclear)
			return coord.Interpretation{Attendance: attendance, Data: mustJSON(d), Unclear: list, NeedsFollowup: len(list) > 0}, nil
		}
	}
	if req.Pending == SlotUnavailable {
		if strings.TrimSpace(text) == "なし" {
			d.UnavailableDates = []string{}
			delete(unclear, SlotUnavailable)
		} else {
			dates := strings.Fields(strings.ReplaceAll(text, "、", " "))
			v := &coord.ValidationError{}
			checked := validateDates(v, SlotUnavailable, dates)
			if v.Err() == nil {
				d.UnavailableDates = checked
				delete(unclear, SlotUnavailable)
			}
		}
	}
	if req.Pending == SlotSchedule || req.Pending == SlotUnavailable {
		list := orderedSlots(unclear)
		return coord.Interpretation{Attendance: attendance, Data: mustJSON(d), Unclear: list, NeedsFollowup: len(list) > 0}, nil
	}

	yes, no := containsAny(text, yesWords), containsAny(text, noWords)
	wantsPresent := containsAny(text, presentWords)
	uncertain := containsAny(text, uncertainWords)

	// 参加可否。
	switch {
	case containsAny(text, absentWords):
		attendance = coord.AttendanceAbsent
		delete(unclear, coord.SlotAttendance)
	case strings.Contains(text, "参加") || strings.Contains(text, "出ます"):
		attendance = coord.AttendanceAttending
		delete(unclear, coord.SlotAttendance)
	case req.Pending == coord.SlotAttendance && (yes || no):
		attendance = coord.AttendanceAttending
		if no {
			attendance = coord.AttendanceAbsent
		}
		delete(unclear, coord.SlotAttendance)
	}

	// 読んできた節と説明できる節。題名がそのまま出てきた節だけを対応付ける。
	var matched []string
	for _, sec := range sd.Sections {
		if sec.Title != "" && strings.Contains(text, sec.Title) {
			matched = append(matched, sec.ID)
		}
	}
	if len(matched) > 0 {
		d.PreparedSectionIDs = matched
		delete(unclear, SlotPrepared)
		if wantsPresent && !uncertain {
			d.ExplainableSectionIDs = matched
			delete(unclear, SlotExplainable)
		}
	} else if (req.Pending == SlotExplainable || req.Pending == SlotWilling) && yes && !uncertain && len(d.PreparedSectionIDs) > 0 {
		// 「（読んできた範囲を）説明できますか」への返答。範囲は読んできた節に限る。
		d.ExplainableSectionIDs = d.PreparedSectionIDs
		delete(unclear, SlotExplainable)
	}
	if uncertain {
		d.ExplainableSectionIDs = []string{}
		unclear[SlotExplainable] = struct{}{}
	}
	if (req.Pending == SlotExplainable || req.Pending == SlotWilling) && no {
		// 担当しないことが確定したら、説明できる節と時間は聞かない。
		d.ExplainableSectionIDs = []string{}
		d.MaxPresentationMinutes = 0
		delete(unclear, SlotWilling)
		delete(unclear, SlotExplainable)
		delete(unclear, SlotMinutes)
	}

	// 説明に使える時間。持ち時間を超える申告はそのまま採らず、聞き直す。
	if m := minutesPattern.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(toASCIIDigits(m[1])); err == nil {
			if n > s.DurationMinutes {
				d.MaxPresentationMinutes = s.DurationMinutes
				unclear[SlotMinutes] = struct{}{}
			} else {
				d.MaxPresentationMinutes = n
				delete(unclear, SlotMinutes)
			}
		}
	}

	if req.Partial {
		// 対話では「担当する意思」と「説明に使える時間」を別々に確定できる。
		// 説明できる節が決まっていれば担当する意思はあると見なし、時間は次の質問で聞く。
		d.WillingToPresent = attendance == coord.AttendanceAttending && len(d.ExplainableSectionIDs) > 0 && !unclear.has(SlotExplainable)
		if d.WillingToPresent {
			delete(unclear, SlotWilling)
		}
	} else {
		// 一発の解釈はそのまま保存できる形で返す。「説明できる節」と「分数」の両方が
		// 読み取れたときだけ担当とし、迷う場合は担当しない側に倒す。
		willing := attendance == coord.AttendanceAttending && len(d.ExplainableSectionIDs) > 0 && d.MaxPresentationMinutes > 0 &&
			!unclear.has(SlotExplainable) && !unclear.has(SlotMinutes)
		d.WillingToPresent = willing
		if willing {
			delete(unclear, SlotWilling)
		} else {
			d.ExplainableSectionIDs = []string{}
			d.MaxPresentationMinutes = 0
			if attendance == coord.AttendanceAttending && wantsPresent {
				unclear[SlotMinutes] = struct{}{}
			}
		}
	}
	if attendance == coord.AttendanceAbsent {
		// 欠席なら担当できない。従属する値は正規化し、聞き直さない。
		d.WillingToPresent = false
		d.ExplainableSectionIDs = []string{}
		d.MaxPresentationMinutes = 0
		delete(unclear, SlotWilling)
		delete(unclear, SlotExplainable)
		delete(unclear, SlotMinutes)
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

// SlotQuestion は未確定の項目を聞く定型文。Bot の発話はすべてこの形で用意し、LLM には書かせない。
func (Playbook) SlotQuestion(s coord.Snapshot, slot string) string {
	switch slot {
	case coord.SlotAttendance:
		if s.ScheduleStatus == coord.ScheduleProposed {
			return "この輪読に参加しますか？（参加／欠席）。開催日時は全員の条件を集めて決めます。"
		}
		return "今回は参加できますか？（参加／欠席）"
	case SlotSchedule:
		return "参加できる曜日・時間帯と最大参加時間を教えてください（JST）。例：水 20:00-22:00 60分 ／ 2026-10-07 21:00-22:00 60分。分からなければ「未定」、期間中すべて難しければ「期間内は参加不可」。"
	case SlotUnavailable:
		return "終日参加できない日をYYYY-MM-DDで教えてください。複数日は空白で区切り、なければ「なし」と答えてください。"
	case SlotWilling:
		return "今回の説明を担当できますか？（はい／いいえ）"
	case SlotPrepared:
		return "どこまで読んできましたか？" + targetSectionsText(s)
	case SlotExplainable:
		return "説明できるのはどの範囲ですか？" + targetSectionsText(s)
	case SlotMinutes:
		return fmt.Sprintf("説明に使える時間は何分ですか？（0〜%d分）", s.DurationMinutes)
	}
	return ""
}

// targetSectionsText は今回の範囲を列挙する。節の題名は既存の可変文字列なのでそのまま載せる。
func targetSectionsText(s coord.Snapshot) string {
	sd, err := sessionData(s)
	if err != nil || len(sd.TargetSectionIDs) == 0 {
		return ""
	}
	titles := map[string]string{}
	for _, sec := range sd.Sections {
		titles[sec.ID] = sec.Title
	}
	return "\n今回の範囲：" + sectionsLabel(titles, sd.TargetSectionIDs)
}
