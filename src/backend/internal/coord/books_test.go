package coord_test

import (
	"encoding/json"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func bookInput(mode string, n int) apitypes.CreateReadingBookInput {
	return apitypes.CreateReadingBookInput{Title: "新しい本", TocSource: json.RawMessage(`{"kind":"manual","urls":[]}`), Sections: json.RawMessage(`[{"id":"sec_1","title":"第1章"},{"id":"sec_2","title":"第2章"}]`), PlannedSessionCount: n, SessionCreationMode: mode, InitialSession: apitypes.ReadingBookSessionInput{PeriodStart: "2026-09-22", PeriodEnd: "2026-09-30", DurationMinutes: 60, TargetSectionIDs: []string{"sec_1"}}}
}

func createBook(t *testing.T, h *harness, mode string, n int) (apitypes.CreateReadingBookResult, string) {
	t.Helper()
	res, err := h.c.CreateReadingBook(ctx, h.users["A"], h.group, bookInput(mode, n), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out apitypes.CreateReadingBookResult
	decode(t, res.Body, &out)
	return out, out.InitialSession.ID
}

func TestReadingBookSequentialAndAllSlots(t *testing.T) {
	h := newHarness(t, nil)
	seq, _ := createBook(t, h, "sequential", 3)
	d, err := h.c.ReadingBookDetail(ctx, h.users["A"], h.group, seq.Book.ID)
	if err != nil || len(d.Sessions) != 1 || d.Sessions[0].Status != "active" {
		t.Fatalf("sequential: %+v %v", d, err)
	}
	all, _ := createBook(t, h, "all", 3)
	d, err = h.c.ReadingBookDetail(ctx, h.users["A"], h.group, all.Book.ID)
	if err != nil || len(d.Sessions) != 3 || d.Sessions[0].Status != "active" || d.Sessions[1].Status != "planned" || d.Sessions[1].Session != nil {
		t.Fatalf("all: %+v %v", d, err)
	}
	// Planned slots have no session object, therefore no common case/task/notification flow.
}

func TestReadingBookStartLimitsPermissionsAndCompletion(t *testing.T) {
	h := newHarness(t, nil)
	seq, _ := createBook(t, h, "sequential", 2)
	// A regular member cannot start a slot.
	if _, err := h.c.StartReadingBookSession(ctx, h.users["B"], h.group, seq.Book.ID, apitypes.ReadingBookSessionInput{PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", DurationMinutes: 60, TargetSectionIDs: []string{"sec_2"}}, nil); code(err) != apperr.Forbidden {
		t.Fatalf("member start: %v", err)
	}
	if _, err := h.c.StartReadingBookSession(ctx, h.users["A"], h.group, seq.Book.ID, apitypes.ReadingBookSessionInput{PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", DurationMinutes: 60, TargetSectionIDs: []string{"sec_2"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.StartReadingBookSession(ctx, h.users["A"], h.group, seq.Book.ID, apitypes.ReadingBookSessionInput{PeriodStart: "2026-10-10", PeriodEnd: "2026-10-17", DurationMinutes: 60, TargetSectionIDs: []string{"sec_2"}}, nil); code(err) != apperr.InvalidState {
		t.Fatalf("limit: %v", err)
	}

	all, _ := createBook(t, h, "all", 2)
	d, _ := h.c.ReadingBookDetail(ctx, h.users["A"], h.group, all.Book.ID)
	if _, err := h.c.StartReadingBookSession(ctx, h.users["A"], h.group, all.Book.ID, apitypes.ReadingBookSessionInput{SlotID: d.Sessions[1].SlotID, PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", DurationMinutes: 60, TargetSectionIDs: []string{"sec_2"}}, nil); err != nil {
		t.Fatal(err)
	}
	// A slot cannot be completed while its session has no confirmed proposal.
	rev := int64(1)
	if _, err := h.c.CompleteReadingBookSession(ctx, h.users["A"], h.group, all.Book.ID, d.Sessions[0].SlotID, apitypes.CompleteReadingBookSessionInput{ExpectedRevision: &rev}, nil); code(err) != apperr.InvalidState {
		t.Fatalf("unconfirmed complete: %v", err)
	}

	// Store a confirmed plan just as the existing confirmation flow does.
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		s, err := tx.Session(ctx, d.Sessions[0].Session.ID)
		if err != nil {
			return err
		}
		cs, err := tx.LatestCase(ctx, s.ID)
		if err != nil {
			return err
		}
		p := store.Proposal{ID: store.NewID("prop"), SessionID: s.ID, CaseID: cs.ID, Version: 1, Status: store.ProposalConfirmed, ChangeKind: "initial", Author: "owner", Summary: "confirmed", Data: json.RawMessage(`{"covered_section_ids":["sec_1"]}`), Requirements: json.RawMessage(`{}`), Revision: s.Revision, CreatedAt: t0}
		if err := tx.CreateProposal(ctx, p); err != nil {
			return err
		}
		s.ConfirmedProposalID = p.ID
		s.Status = "confirmed"
		return tx.UpdateSessionState(ctx, s)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CompleteReadingBookSession(ctx, h.users["A"], h.group, all.Book.ID, d.Sessions[0].SlotID, apitypes.CompleteReadingBookSessionInput{ExpectedRevision: &rev}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CompleteReadingBookSession(ctx, h.users["A"], h.group, all.Book.ID, d.Sessions[0].SlotID, apitypes.CompleteReadingBookSessionInput{ExpectedRevision: &rev}, nil); code(err) != apperr.InvalidState {
		t.Fatalf("double complete: %v", err)
	}
	updated, _ := h.c.ReadingBookDetail(ctx, h.users["A"], h.group, all.Book.ID)
	if len(updated.Book.CompletedSectionIDs) != 1 || updated.Book.CompletedSectionIDs[0] != "sec_1" {
		t.Fatalf("progress: %+v", updated.Book)
	}
}
