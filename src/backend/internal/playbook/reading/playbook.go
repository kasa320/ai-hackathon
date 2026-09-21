package reading

import (
	"context"
	"encoding/json"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

const ID = "reading"

// Playbook は輪読用の実装。
type Playbook struct{}

var (
	_ coord.Playbook     = Playbook{}
	_ coord.DraftPlanner = Playbook{}
)

func New() Playbook { return Playbook{} }

func (Playbook) Descriptor() coord.Descriptor {
	return coord.Descriptor{ID: ID, Name: "輪読"}
}

// aiContext は AI に渡す判断材料。共有可能な構造化情報だけを含む。
type aiContext struct {
	StartsAt        string     `json:"starts_at"`
	Schedule        aiSchedule `json:"schedule"`
	DurationMinutes int        `json:"duration_minutes"`
	Book            string     `json:"book_title"`
	Sections        []Section  `json:"sections"`
	Completed       []string   `json:"completed_section_ids"`
	Target          []string   `json:"target_section_ids"`
	// AssignedPresenter は全体計画で本人が承認した、この回の担当者。ある場合、発表の担当は必ずこの人にする。
	AssignedPresenter string            `json:"assigned_presenter_member_id,omitempty"`
	Members           []aiMember        `json:"members"`
	CurrentPlan       *PlanData         `json:"current_confirmed_plan"`
	Case              coord.CaseContext `json:"case"`
}

// aiSchedule は開催日時の決まり具合。proposed の間は案で日時を決める。
type aiSchedule struct {
	Status      string `json:"status"`
	PeriodStart string `json:"period_start,omitempty"`
	PeriodEnd   string `json:"period_end,omitempty"`
	// Candidates は全員の共通時間から計算した候補（近い順、JST）。
	Candidates []string `json:"candidate_datetimes,omitempty"`
	// Today は判断時点の日付（JST）。過去の日を選ばないために渡す。
	Today string `json:"today,omitempty"`
}

type aiMember struct {
	ID                   string           `json:"member_id"`
	DisplayName          string           `json:"display_name"`
	Role                 string           `json:"role"`
	Answered             bool             `json:"answered"`
	Attendance           string           `json:"attendance,omitempty"`
	Preparation          *PreparationData `json:"preparation,omitempty"`
	ConsecutiveSubstitue int              `json:"consecutive_substitute_count"`
	PastPresentations    int              `json:"past_presentation_count"`
	CurrentPresenter     bool             `json:"presenter_in_current_plan"`
}

// BuildContext は確認済みの共有可能な状態から AI の判断材料を組み立てる。
func (Playbook) BuildContext(_ context.Context, s coord.Snapshot) (json.RawMessage, error) {
	sd, err := sessionData(s)
	if err != nil {
		return nil, err
	}
	c := aiContext{
		StartsAt:          s.StartsAt.UTC().Format("2006-01-02T15:04:05Z"),
		Schedule:          buildSchedule(s),
		DurationMinutes:   s.DurationMinutes,
		Book:              sd.BookTitle,
		Sections:          sd.Sections,
		Completed:         sd.CompletedSectionIDs,
		Target:            sd.TargetSectionIDs,
		AssignedPresenter: sd.AssigneeMemberID,
		Case:              s.Case,
	}
	var current idSet
	if cur := s.CurrentPlan(); cur != nil {
		var p PlanData
		if err := json.Unmarshal(cur.Data, &p); err == nil {
			c.CurrentPlan = &p
			current = newIDSet(p.presenters())
		}
	}
	for _, m := range s.Members {
		am := aiMember{ID: m.ID, DisplayName: m.DisplayName, Role: m.Role, ConsecutiveSubstitue: consecutiveSubstituteCount(s, m.ID), PastPresentations: pastPresentations(s, m.ID), CurrentPresenter: current.has(m.ID)}
		if p, pd := preparationData(s, m.ID); p != nil {
			am.Answered = true
			am.Attendance = p.Attendance
			am.Preparation = &pd
		}
		c.Members = append(c.Members, am)
	}
	return json.Marshal(c)
}

// PlanSchema は PlanData の JSON Schema。
func (Playbook) PlanSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["covered_section_ids", "deferred_section_ids", "agenda"],
  "properties": {
    "starts_at": {"type": "string", "description": "開催日時（RFC 3339、例 2026-10-02T19:00:00+09:00）。schedule.status が proposed のときだけ入れる。決まっている回では省く"},
    "frequency": {"type": "string", "description": "期間全体の進め方を1行で（例：週1回60分・全8回）。説明用"},
    "covered_section_ids": {"type": "array", "items": {"type": "string"}, "description": "今回扱う節ID"},
    "deferred_section_ids": {"type": "array", "items": {"type": "string"}, "description": "次回へ持ち越す節ID"},
    "agenda": {
      "type": "array",
      "minItems": 1,
      "maxItems": 20,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "activity", "section_ids", "presenter_member_id", "minutes"],
        "properties": {
          "id": {"type": "string"},
          "activity": {"type": "string", "enum": ["presentation", "review", "discussion", "joint_reading"]},
          "section_ids": {"type": "array", "items": {"type": "string"}, "minItems": 1},
          "presenter_member_id": {"type": ["string", "null"]},
          "minutes": {"type": "integer", "minimum": 1}
        }
      }
    }
  }
}`)
}
