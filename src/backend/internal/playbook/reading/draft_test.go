package reading_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

func TestDraftInitialPlanUsesWillingPresenter(t *testing.T) {
	pb := reading.New()
	s := baseSnapshot()
	d, err := pb.DraftPlan(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != coord.DraftProposal {
		t.Fatalf("kind = %s (%s)", d.Kind, d.Summary)
	}
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: d.Plan}); err != nil {
		t.Fatalf("仮の案が検証を通らない: %v", err)
	}
	var p reading.PlanData
	_ = json.Unmarshal(d.Plan, &p)
	if !reflect.DeepEqual(p.CoveredSectionIDs, []string{"sec_2", "sec_3"}) {
		t.Fatalf("covered = %v", p.CoveredSectionIDs)
	}
}

// E02：B が辞退し、C は第2節のみ15分可能。範囲を縮めて60分以内に収め、残りを持ち越す。
func TestDraftReplanAfterWithdrawal(t *testing.T) {
	pb := reading.New()
	s := withConfirmed(baseSnapshot(), initialPlanJSON)
	withdrawn, _ := pb.ApplyWithdrawal(ctx, s, coord.WithdrawAssignment, s.Preparation("mem_b").Data)
	setPrep(&s, "mem_b", &coord.Preparation{Attendance: "attending", Data: withdrawn})
	s.Case.WithdrawnMemberIDs = []string{"mem_b"}

	d, err := pb.DraftPlan(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != coord.DraftProposal {
		t.Fatalf("kind = %s (%s)", d.Kind, d.Summary)
	}
	var p reading.PlanData
	_ = json.Unmarshal(d.Plan, &p)
	var got reading.PlanData
	_ = json.Unmarshal([]byte(replanJSON), &got)
	// C 15分、復習20分、議論25分、sec_3 持ち越しになる。
	if !reflect.DeepEqual(p.CoveredSectionIDs, got.CoveredSectionIDs) || !reflect.DeepEqual(p.DeferredSectionIDs, got.DeferredSectionIDs) {
		t.Fatalf("範囲: %+v", p)
	}
	total := 0
	for _, item := range p.Agenda {
		total += item.Minutes
	}
	if total > 60 || p.Agenda[0].Minutes != 15 || *p.Agenda[0].PresenterMemberID != "mem_c" {
		t.Fatalf("進行表: %+v", p.Agenda)
	}
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: coord.ChangeReplan, Data: d.Plan}); err != nil {
		t.Fatal(err)
	}
}

func TestDraftAsksThenStops(t *testing.T) {
	pb := reading.New()
	s := withConfirmed(baseSnapshot(), initialPlanJSON)
	withdrawn, _ := pb.ApplyWithdrawal(ctx, s, coord.WithdrawAssignment, s.Preparation("mem_b").Data)
	setPrep(&s, "mem_b", &coord.Preparation{Attendance: "attending", Data: withdrawn})
	setPrep(&s, "mem_c", prep("attending", false, []string{"sec_1", "sec_2"}, []string{}, 0))
	s.Case.WithdrawnMemberIDs = []string{"mem_b"}

	d, _ := pb.DraftPlan(ctx, s)
	if d.Kind != coord.DraftAsk || !reflect.DeepEqual(d.AskMemberIDs, []string{"mem_c"}) {
		t.Fatalf("辞退者以外で準備済みの C に確認すべき: %+v", d)
	}

	// E03：確認済みでも担当できる人がいなければ、同じ依頼を繰り返さずに管理者へ戻す。
	s.Case.AskedMemberIDs = []string{"mem_c"}
	d, _ = pb.DraftPlan(ctx, s)
	if d.Kind != coord.DraftNoFeasible {
		t.Fatalf("管理者判断待ちにすべき: %+v", d)
	}
}

func TestDraftSkipsDeclinedMember(t *testing.T) {
	pb := reading.New()
	s := withConfirmed(baseSnapshot(), initialPlanJSON)
	withdrawn, _ := pb.ApplyWithdrawal(ctx, s, coord.WithdrawAssignment, s.Preparation("mem_b").Data)
	setPrep(&s, "mem_b", &coord.Preparation{Attendance: "attending", Data: withdrawn})
	s.Case.WithdrawnMemberIDs = []string{"mem_b"}
	s.Case.DeclinedMemberIDs = []string{"mem_c"}
	d, _ := pb.DraftPlan(ctx, s)
	if d.Kind != coord.DraftNoFeasible {
		t.Fatalf("断った C に再依頼してはいけない: %+v", d)
	}
}
