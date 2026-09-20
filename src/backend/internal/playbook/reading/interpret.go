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
- 担当できるか判断できないときは willing_to_present=false、explainable_section_ids=[]、max_presentation_minutes=0 にして、unclear に項目名を入れる。
- 「自信がない」「たぶん」など確信のない範囲は explainable_section_ids に入れず、unclear に explainable_section_ids を入れる。
- 欠席と読み取れるときだけ attendance="absent" にする。書かれていなければ "attending" とし、unclear に attendance を入れる。`
}

// 仮の抽出で使う手がかり。
var (
	minutesPattern = regexp.MustCompile(`([0-9０-９]{1,3})\s*分`)
	absentWords    = []string{"欠席", "参加できません", "参加できない", "行けません", "行けない", "休みます", "欠席します"}
	presentWords   = []string{"説明", "担当", "発表"}
	uncertainWords = []string{"自信がない", "自信ない", "たぶん", "かも", "わからない", "分からない"}
)

// DraftInterpret は LLM を使わずに規則だけで抽出する（AGENT_MODE=fake 用）。
// 節の題名がそのまま含まれている場合だけ対応付け、読み取れない項目は unclear に入れる。
func (Playbook) DraftInterpret(_ context.Context, s coord.Snapshot, text string) (coord.Interpretation, error) {
	sd, err := sessionData(s)
	if err != nil {
		return coord.Interpretation{}, err
	}
	var unclear []string

	attendance := coord.AttendanceAttending
	if containsAny(text, absentWords) {
		attendance = coord.AttendanceAbsent
	} else if !strings.Contains(text, "参加") && !strings.Contains(text, "出ます") {
		unclear = append(unclear, "attendance")
	}

	var prepared, explainable []string
	wantsPresent := containsAny(text, presentWords)
	uncertain := containsAny(text, uncertainWords)
	for _, sec := range sd.Sections {
		if sec.Title == "" || !strings.Contains(text, sec.Title) {
			continue
		}
		prepared = append(prepared, sec.ID)
		if wantsPresent && !uncertain {
			explainable = append(explainable, sec.ID)
		}
	}
	if len(prepared) == 0 {
		unclear = append(unclear, "prepared_section_ids")
	}
	if uncertain {
		unclear = append(unclear, "explainable_section_ids")
	}

	minutes := 0
	if m := minutesPattern.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(toASCIIDigits(m[1])); err == nil {
			minutes = n
		}
	}
	if minutes > s.DurationMinutes {
		minutes = s.DurationMinutes
		unclear = append(unclear, "max_presentation_minutes")
	}

	// 担当は「説明できる節」と「分数」の両方が読み取れたときだけ。迷う場合は担当しない側に倒す。
	willing := attendance == coord.AttendanceAttending && len(explainable) > 0 && minutes > 0
	if !willing {
		explainable = nil
		minutes = 0
		if attendance == coord.AttendanceAttending && wantsPresent {
			unclear = appendUnique(unclear, "max_presentation_minutes")
		}
	}

	data := PreparationData{
		WillingToPresent:       willing,
		PreparedSectionIDs:     nonNil(prepared),
		ExplainableSectionIDs:  nonNil(explainable),
		MaxPresentationMinutes: minutes,
	}
	return coord.Interpretation{
		Attendance:    attendance,
		Data:          mustJSON(data),
		Unclear:       unclear,
		NeedsFollowup: len(unclear) > 0,
	}, nil
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
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
