package coord_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

func (h *harness) createPeriodSession() {
	h.t.Helper()
	r, err := h.c.CreateSession(ctx, h.users["A"], h.group, apitypes.CreateSessionInput{PlaybookID: "reading", PeriodStart: "2026-09-20", PeriodEnd: "2026-09-30", DurationMinutes: 60, Data: json.RawMessage(sessionData)}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	var s apitypes.SessionCreated
	decode(h.t, r.Body, &s)
	h.sess = s.Session.ID
}

func (h *harness) allScheduled() {
	h.allPrepared()
	for _, name := range []string{"A", "B", "C", "D"} {
		p := myPrep(h.t, h, name)
		var d map[string]any
		json.Unmarshal(p.Data, &d)
		d["schedule"] = map[string]any{"status": "provided", "weekly_windows": []any{map[string]any{"weekday": 3, "start": "20:00", "end": "22:00"}}, "date_windows": []any{}, "max_duration_minutes": 60}
		raw, _ := json.Marshal(d)
		h.mustPrep(name, "attending", raw)
	}
}

func TestScheduledProposalRequiresEveryMemberAndBoundCards(t *testing.T) {
	h := newHarness(t, nil)
	h.createPeriodSession()
	h.allScheduled()
	h.process()
	d := h.detail("A")
	if d.CurrentProposal == nil {
		t.Fatal("missing proposal")
	}
	if len(d.CurrentProposal.Approvals) != 2 || d.CurrentProposal.Approvals[1].Kind != "all" || d.CurrentProposal.Approvals[1].RequiredCount != 4 {
		t.Fatalf("requirements: %+v", d.CurrentProposal.Approvals)
	}
	cards, err := h.c.DialogTasks(ctx, h.users["B"], h.sess)
	if err != nil || len(cards) != 2 {
		t.Fatalf("cards: %+v %v", cards, err)
	}
	for _, card := range cards {
		if !strings.Contains(strings.Join(card.Lines, "\n"), "2026/09/23 20:00") {
			t.Fatalf("missing concrete time: %v", card.Lines)
		}
		if _, err := h.c.RespondTask(ctx, h.users["C"], card.Task.ID, apitypes.TaskResponseInput{Decision: card.Task.AllowedDecisions[0], ProposalID: card.Task.ProposalID, ProposalVersion: card.Task.ProposalVersion}, nil); code(err) != apperr.NotFound {
			t.Fatalf("proxy response: %v", err)
		}
	}
	h.mustRespond("A", "owner_approval", "approve")
	h.mustRespond("B", "assignment", "accept")
	for _, name := range []string{"A", "B", "C"} {
		h.mustRespond(name, "approval", "approve")
	}
	if h.detail("A").Session.ScheduleStatus == "confirmed" {
		t.Fatal("majority incorrectly confirmed date")
	}
	h.mustRespond("D", "approval", "approve")
	if h.detail("A").Session.ScheduleStatus != "confirmed" {
		t.Fatal("did not confirm unanimous date")
	}
	if got, err := h.c.DialogTasks(ctx, h.users["B"], h.sess); err != nil || len(got) != 0 {
		t.Fatalf("old tasks still shown: %+v %v", got, err)
	}
}

func TestScheduledRejectReplansAndInvalidatesOldConsent(t *testing.T) {
	h := newHarness(t, nil)
	h.createPeriodSession()
	h.allScheduled()
	h.process()
	old := h.openTask("B", "approval")
	first := h.detail("A").CurrentProposal
	h.mustRespond("D", "approval", "reject")
	h.process()
	next := h.detail("A").CurrentProposal
	if next == nil || next.ID == first.ID || next.Version <= first.Version {
		t.Fatal("rejection did not create new proposal")
	}
	var a, b struct {
		StartsAt string `json:"starts_at"`
	}
	json.Unmarshal(first.Data, &a)
	json.Unmarshal(next.Data, &b)
	if a.StartsAt == b.StartsAt {
		t.Fatal("repeated rejected date")
	}
	_, err := h.c.RespondTask(ctx, h.users["B"], old.ID, apitypes.TaskResponseInput{Decision: "approve", ProposalID: old.ProposalID, ProposalVersion: old.ProposalVersion}, nil)
	if code(err) != apperr.ProposalSuperseded {
		t.Fatalf("old consent accepted: %v", err)
	}
}

func TestScheduleDialogRequiresConfirmationAndPreservesOtherFields(t *testing.T) {
	h := newHarness(t, nil)
	h.createPeriodSession()
	h.allPrepared()
	h.mustPrep("B", "attending", prepData(true)) // 担当の辞退は時間帯の会話で失われない
	r, err := h.c.StartDialog(ctx, h.users["B"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ready || r.State.Pending != "schedule" {
		t.Fatalf("missing schedule not asked: %+v", r)
	}
	r = h.turn(t, r.State, "水 20:00-22:00 60分")
	if !r.Ready {
		t.Fatalf("expected confirmation: %+v", r)
	}
	if strings.Contains(string(myPrep(t, h, "B").Data), "weekly_windows") {
		t.Fatal("saved before confirmation")
	}
	if !strings.Contains(strings.Join(r.Confirm, "\n"), "20:00〜22:00") {
		t.Fatal("conditions not shown for confirmation")
	}
	if _, err := h.c.SaveDialogPreparation(ctx, h.users["B"], r.State, nil); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Declined bool `json:"declined_presentation"`
		Schedule any  `json:"schedule"`
	}
	json.Unmarshal(myPrep(t, h, "B").Data, &d)
	if !d.Declined || d.Schedule == nil {
		t.Fatalf("wrong saved fields: %+v", d)
	}
	// A placeholder date expiring must not prevent input during the remaining period.
	h.clk.Advance(3 * 24 * time.Hour)
	if _, err := h.c.StartDialog(ctx, h.users["B"], h.sess); err != nil {
		t.Fatalf("placeholder blocked remaining period: %v", err)
	}
}

func TestNoCommonTimeDoesNotReduceParticipantSet(t *testing.T) {
	h := newHarness(t, nil)
	h.createPeriodSession()
	h.allScheduled()
	p := myPrep(t, h, "D")
	h.mustPrep("D", coord.AttendanceAbsent, p.Data)
	h.process()
	d := h.detail("A")
	if d.CurrentProposal != nil && d.CurrentProposal.Status == "pending" {
		t.Fatal("excluded absent member to propose")
	}
	if d.ActiveCase == nil || d.ActiveCase.Status != "needs_owner" {
		t.Fatalf("no safe handoff: %+v", d.ActiveCase)
	}
}
