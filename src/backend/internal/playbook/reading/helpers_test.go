package reading_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// 輪読テスト共通の初期状態：A（管理者）・B・C・D、60分、sec_1 は前回まで、sec_2・sec_3 が今回。
const sessionJSON = `{
  "book_title": "サンプル技術書",
  "isbn": null,
  "toc_source": {"kind": "manual", "urls": []},
  "sections": [
    {"id": "sec_1", "title": "前回の範囲"},
    {"id": "sec_2", "title": "今回の前半"},
    {"id": "sec_3", "title": "今回の後半"}
  ],
  "completed_section_ids": ["sec_1"],
  "target_section_ids": ["sec_2", "sec_3"]
}`

func prep(attendance string, willing bool, prepared, explainable []string, minutes int) *coord.Preparation {
	data, _ := json.Marshal(map[string]any{
		"willing_to_present":       willing,
		"prepared_section_ids":     prepared,
		"explainable_section_ids":  explainable,
		"max_presentation_minutes": minutes,
	})
	return &coord.Preparation{Attendance: attendance, Data: data}
}

func baseSnapshot() coord.Snapshot {
	return coord.Snapshot{
		SessionID:       "ses_1",
		CaseID:          "case_1",
		Revision:        1,
		StartsAt:        time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC),
		DurationMinutes: 60,
		OwnerMemberID:   "mem_a",
		Members: []coord.SnapshotMember{
			{ID: "mem_a", DisplayName: "A", Role: "owner"},
			{ID: "mem_b", DisplayName: "B", Role: "member"},
			{ID: "mem_c", DisplayName: "C", Role: "member"},
			{ID: "mem_d", DisplayName: "D", Role: "member"},
		},
		SessionData: json.RawMessage(sessionJSON),
		Preparations: []coord.MemberPreparation{
			{MemberID: "mem_a", Value: prep("attending", false, []string{"sec_1"}, []string{}, 0)},
			{MemberID: "mem_b", Value: prep("attending", true, []string{"sec_1", "sec_2", "sec_3"}, []string{"sec_2", "sec_3"}, 40)},
			{MemberID: "mem_c", Value: prep("attending", true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)},
			{MemberID: "mem_d", Value: prep("attending", false, []string{"sec_1"}, []string{}, 0)},
		},
	}
}

func setPrep(s *coord.Snapshot, memberID string, p *coord.Preparation) {
	for i := range s.Preparations {
		if s.Preparations[i].MemberID == memberID {
			s.Preparations[i].Value = p
		}
	}
}

// 初回の確定計画：B が sec_2・sec_3 を担当。
const initialPlanJSON = `{
  "covered_section_ids": ["sec_2", "sec_3"],
  "deferred_section_ids": [],
  "agenda": [
    {"id": "item_1", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 15},
    {"id": "item_2", "activity": "presentation", "section_ids": ["sec_2", "sec_3"], "presenter_member_id": "mem_b", "minutes": 30},
    {"id": "item_3", "activity": "discussion", "section_ids": ["sec_2", "sec_3"], "presenter_member_id": null, "minutes": 15}
  ]
}`

// api.md R6 の再計画例：C が sec_2 を15分、sec_3 は持ち越し。
const replanJSON = `{
  "covered_section_ids": ["sec_2"],
  "deferred_section_ids": ["sec_3"],
  "agenda": [
    {"id": "item_1", "activity": "presentation", "section_ids": ["sec_2"], "presenter_member_id": "mem_c", "minutes": 15},
    {"id": "item_2", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 20},
    {"id": "item_3", "activity": "discussion", "section_ids": ["sec_2"], "presenter_member_id": null, "minutes": 25}
  ]
}`

func withConfirmed(s coord.Snapshot, plans ...string) coord.Snapshot {
	for i, p := range plans {
		kind := coord.ChangeInitial
		if i > 0 {
			kind = coord.ChangeReplan
		}
		s.ConfirmedPlans = append(s.ConfirmedPlans, coord.PlanRecord{ProposalID: "prop", Version: i + 1, ChangeKind: kind, Data: json.RawMessage(p)})
	}
	return s
}

// validationPaths は検証エラーのパス一覧を返す。検証エラー以外なら失敗させる。
func validationPaths(t *testing.T, err error) []string {
	t.Helper()
	var v *coord.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("ValidationError を期待したが %v", err)
	}
	var paths []string
	for _, f := range v.Fields {
		paths = append(paths, f.Path)
	}
	return paths
}

func hasPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want || strings.HasPrefix(p, want+".") || strings.HasPrefix(p, want+"[") {
			return true
		}
	}
	return false
}
