package reading

import (
	"context"
	"encoding/json"
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
var slotOrder = []string{coord.SlotAttendance, SlotWilling, SlotPrepared, SlotExplainable, SlotMinutes}

// PreparationSlots は data の項目名。
func (Playbook) PreparationSlots() []string {
	return []string{SlotWilling, SlotPrepared, SlotExplainable, SlotMinutes}
}

// interpretContext は抽出に必要な最小限の判断材料。他のメンバーの回答・担当履歴は含めない。
type interpretContext struct {
	DurationMinutes int       `json:"duration_minutes"`
	Sections        []Section `json:"sections"`
	Completed       []string  `json:"completed_section_ids"`
	Target          []string  `json:"target_section_ids"`
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
	})
}

// PreparationSchema は PreparationData の JSON Schema。
func (Playbook) PreparationSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["willing_to_present", "prepared_section_ids", "explainable_section_ids", "max_presentation_minutes"],
  "properties": {
    "willing_to_present": {"type": "boolean", "description": "今回の説明を担当できるか"},
    "prepared_section_ids": {"type": "array", "items": {"type": "string"}, "description": "読んできた節ID"},
    "explainable_section_ids": {"type": "array", "items": {"type": "string"}, "description": "説明できる節ID（読んできた節の範囲内）"},
    "max_presentation_minutes": {"type": "integer", "minimum": 0, "description": "説明に使える最大の分数。担当しないなら0"}
  }
}`)
}

func (Playbook) InterpretInstructions() string {
	return `取り出すのは、発言者本人の輪読の準備状況だけです。

- 節IDは sections に載っているIDだけを使う。載っていない範囲の話は無視する。
- prepared_section_ids は「読んできた」と読み取れる節だけ。explainable_section_ids はそのうち「説明できる」と読み取れる節だけ。
- max_presentation_minutes は本人が示した分数。duration_minutes を超えない。
- 担当できるか判断できないときは willing_to_present=false、explainable_section_ids=[]、max_presentation_minutes=0 にして、unclear に willing_to_present を入れる（読み取れていない項目名もすべて入れる）。
- 「自信がない」「たぶん」など確信のない範囲は explainable_section_ids に入れず、unclear に explainable_section_ids を入れる。
- 欠席と読み取れるときだけ attendance="absent" にする。書かれていなければ "attending" とし、unclear に attendance を入れる。
- 日程の変更、途中参加・途中退出、他の人の代理での回答は、この項目では扱えません。値を作らず out_of_scope に分類を入れてください。`
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

	// 担当は「説明できる節」と「分数」の両方が確定したときだけ。迷う場合は担当しない側に倒す。
	willing := attendance == coord.AttendanceAttending && len(d.ExplainableSectionIDs) > 0 && d.MaxPresentationMinutes > 0 &&
		!unclear.has(SlotExplainable) && !unclear.has(SlotMinutes)
	d.WillingToPresent = willing
	if willing {
		delete(unclear, SlotWilling)
	}
	if !willing && !req.Partial {
		// 一発の解釈はそのまま保存できる形で返す。担当しないなら従属する値は落とす。
		d.ExplainableSectionIDs = []string{}
		d.MaxPresentationMinutes = 0
		if attendance == coord.AttendanceAttending && wantsPresent {
			unclear[SlotMinutes] = struct{}{}
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
