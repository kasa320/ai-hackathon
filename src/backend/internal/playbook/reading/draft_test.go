package reading_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

// presenters は案の説明の担当者を、進行表の順に節ごとに返す。
func presenters(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var p reading.PlanData
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, item := range p.Agenda {
		if item.Activity == reading.ActivityPresentation {
			for _, sec := range item.SectionIDs {
				out[sec] = *item.PresenterMemberID
			}
		}
	}
	return out
}

// 担当は辞退していない参加予定者に、範囲のまとまりごとに割り振る。
func TestDraftAssignsSectionsToAvailableMembers(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot() // A・D は辞退、B・C に割り振れる
	d, err := pb.DraftPlan(ctx, s)
	if err != nil || d.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	got := presenters(t, d.Plan)
	if !reflect.DeepEqual(got, map[string]string{"sec_2": "mem_b", "sec_3": "mem_c"}) {
		t.Fatalf("担当 = %v", got)
	}
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: d.Plan}); err != nil {
		t.Fatalf("自分の案が検証を通らない: %v", err)
	}
}

// 担当した回数が少ない人を優先する。
func TestDraftPrefersMembersWhoPresentedLess(t *testing.T) {
	s := baseSnapshot()
	// B は過去2回担当している
	past := coord.PastSession{ConfirmedPlans: []coord.PlanRecord{{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)}}}
	s.History = []coord.PastSession{past, past}
	setPrep(&s, "mem_a", prep("attending", false))
	d, err := reading.New().DraftPlan(ctx, s)
	if err != nil || d.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	got := presenters(t, d.Plan)
	if got["sec_2"] == "mem_b" || got["sec_3"] == "mem_b" {
		t.Fatalf("担当の多い B を優先した: %v", got)
	}
}

// 担当を辞退した人を外して組み直す。今の担当者は同じ範囲を維持する。
func TestDraftReplanAfterWithdrawal(t *testing.T) {
	pb := reading.New()
	confirmed := `{
  "covered_section_ids": ["sec_2", "sec_3"], "deferred_section_ids": [],
  "agenda": [
    {"id": "item_1", "activity": "presentation", "section_ids": ["sec_2"], "presenter_member_id": "mem_b", "minutes": 13},
    {"id": "item_2", "activity": "presentation", "section_ids": ["sec_3"], "presenter_member_id": "mem_c", "minutes": 13},
    {"id": "item_3", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 20},
    {"id": "item_4", "activity": "discussion", "section_ids": ["sec_2", "sec_3"], "presenter_member_id": null, "minutes": 14}
  ]}`
	s := withConfirmed(baseSnapshot(), confirmed)
	setPrep(&s, "mem_b", prep("attending", true)) // B が担当を辞退
	d, err := pb.DraftPlan(ctx, s)
	if err != nil || d.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	got := presenters(t, d.Plan)
	if got["sec_2"] != "mem_c" || got["sec_3"] != "mem_c" {
		t.Fatalf("C が全範囲を引き継ぐ: %v", got)
	}
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: d.Plan}); err != nil {
		t.Fatalf("自分の案が検証を通らない: %v", err)
	}
}

// 誰にも割り振れなければ管理者判断待ちにする。
func TestDraftStopsWhenNobodyCanPresent(t *testing.T) {
	s := baseSnapshot()
	setPrep(&s, "mem_b", prep("attending", true))
	setPrep(&s, "mem_c", prep("absent", false))
	d, err := reading.New().DraftPlan(ctx, s)
	if err != nil || d.Kind != coord.DraftNoFeasible {
		t.Fatalf("draft = %+v, %v", d, err)
	}
}

// この案件で引き受けを断った人には、同じ案件で再び割り振らない。
func TestDraftSkipsDeclinedMember(t *testing.T) {
	s := baseSnapshot()
	s.Case.DeclinedMemberIDs = []string{"mem_b"}
	d, err := reading.New().DraftPlan(ctx, s)
	if err != nil || d.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	for sec, id := range presenters(t, d.Plan) {
		if id == "mem_b" {
			t.Fatalf("%s を断った人に割り振った", sec)
		}
	}
}

// 担当を辞退した人の分だけを別の人に回し、ほかの人は今の範囲を続ける。
func TestDraftKeepsOtherPresentersInPlace(t *testing.T) {
	confirmed := `{
  "covered_section_ids": ["sec_2", "sec_3"], "deferred_section_ids": [],
  "agenda": [
    {"id": "item_1", "activity": "presentation", "section_ids": ["sec_2"], "presenter_member_id": "mem_b", "minutes": 13},
    {"id": "item_2", "activity": "presentation", "section_ids": ["sec_3"], "presenter_member_id": "mem_c", "minutes": 13},
    {"id": "item_3", "activity": "review", "section_ids": ["sec_1"], "presenter_member_id": null, "minutes": 20},
    {"id": "item_4", "activity": "discussion", "section_ids": ["sec_2", "sec_3"], "presenter_member_id": null, "minutes": 14}
  ]}`
	s := withConfirmed(baseSnapshot(), confirmed)
	setPrep(&s, "mem_b", prep("attending", true))  // B が辞退
	setPrep(&s, "mem_d", prep("attending", false)) // D は割り振れる
	d, err := reading.New().DraftPlan(ctx, s)
	if err != nil || d.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	if got := presenters(t, d.Plan); !reflect.DeepEqual(got, map[string]string{"sec_2": "mem_d", "sec_3": "mem_c"}) {
		t.Fatalf("C は sec_3 を続け、D が sec_2 を引き継ぐ: %v", got)
	}
}
