package coord_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/clock"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func TestInterpretWeeklyAvailability_DraftModeParsesFixedNotation(t *testing.T) {
	h := newHarness(t, nil)
	out, err := h.c.InterpretWeeklyAvailability(ctx, h.users["B"], apitypes.WeeklyAvailabilityInterpretationInput{Text: "水,金 19:00-22:00"})
	if err != nil {
		t.Fatalf("InterpretWeeklyAvailability: %v", err)
	}
	if out.Saved {
		t.Fatal("saved は常に false")
	}
	if out.NeedsFollowup || len(out.Unclear) != 0 {
		t.Fatalf("聞き直し不要のはずが needs_followup=%v unclear=%v", out.NeedsFollowup, out.Unclear)
	}
	if len(out.Availability.Windows) != 2 {
		t.Fatalf("windows件数 = %d, want 2", len(out.Availability.Windows))
	}
	want := map[int]bool{3: true, 5: true} // 水=3, 金=5
	for _, w := range out.Availability.Windows {
		if !want[w.Weekday] {
			t.Fatalf("想定外の曜日: %+v", w)
		}
		if w.Start != "19:00" || w.End != "22:00" {
			t.Fatalf("想定外の時間帯: %+v", w)
		}
	}
	if out.Availability.Timezone != coord.DefaultAvailabilityZone {
		t.Fatalf("timezone = %s, want %s", out.Availability.Timezone, coord.DefaultAvailabilityZone)
	}
}

func TestInterpretWeeklyAvailability_UnrecognizedTextStaysUnclear(t *testing.T) {
	h := newHarness(t, nil)
	out, err := h.c.InterpretWeeklyAvailability(ctx, h.users["B"], apitypes.WeeklyAvailabilityInterpretationInput{Text: "だいたい平日の夜あたりで調整したいです"})
	if err != nil {
		t.Fatalf("InterpretWeeklyAvailability: %v", err)
	}
	if !out.NeedsFollowup || len(out.Unclear) == 0 {
		t.Fatalf("読み取れない文なので聞き直しが必要なはず: needs_followup=%v unclear=%v", out.NeedsFollowup, out.Unclear)
	}
	if len(out.Availability.Windows) != 0 {
		t.Fatalf("読み取れない場合は windows を作らない: %+v", out.Availability.Windows)
	}
}

func TestInterpretWeeklyAvailability_NeverSaves(t *testing.T) {
	h := newHarness(t, nil)
	before, err := h.c.WeeklyAvailability(ctx, h.users["B"])
	if err != nil {
		t.Fatalf("WeeklyAvailability: %v", err)
	}
	if _, err := h.c.InterpretWeeklyAvailability(ctx, h.users["B"], apitypes.WeeklyAvailabilityInterpretationInput{Text: "水,金 19:00-22:00"}); err != nil {
		t.Fatalf("InterpretWeeklyAvailability: %v", err)
	}
	after, err := h.c.WeeklyAvailability(ctx, h.users["B"])
	if err != nil {
		t.Fatalf("WeeklyAvailability: %v", err)
	}
	if after.UpdatedAt != nil || before.UpdatedAt != nil {
		t.Fatal("解釈だけでは保存されない（updated_at が付かない）")
	}
	if len(after.Windows) != 0 {
		t.Fatalf("解釈だけでは保存されない: %+v", after.Windows)
	}
}

func TestInterpretWeeklyAvailability_EmptyTextRejected(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.c.InterpretWeeklyAvailability(ctx, h.users["B"], apitypes.WeeklyAvailabilityInterpretationInput{Text: "  "}); err == nil {
		t.Fatal("空文字は拒否されるはず")
	}
}

// fakeWeeklyInterpreter は自由に値を返せる WeeklyInterpreter。検証・予算の境界を試すのに使う。
type fakeWeeklyInterpreter struct {
	out coord.WeeklyInterpretation
	err error
}

func (f fakeWeeklyInterpreter) InterpretWeekly(ctx context.Context, req coord.WeeklyInterpretRequest) (coord.WeeklyInterpretation, coord.Usage, error) {
	id, err := req.Reserve(ctx, "fake-weekly-model")
	if err != nil {
		return coord.WeeklyInterpretation{}, coord.Usage{}, err
	}
	req.Record(ctx, id, coord.LLMCall{Model: "fake-weekly-model", Currency: "unknown", Succeeded: true})
	if f.err != nil {
		return coord.WeeklyInterpretation{}, coord.Usage{}, f.err
	}
	if err := req.Check(ctx, f.out); err != nil {
		return coord.WeeklyInterpretation{}, coord.Usage{}, err
	}
	return f.out, coord.Usage{}, nil
}

// newWeeklyHarness は普段の空き時間の解釈だけを試す最小の一式（用途はグループ・開催回を必要としない）。
func newWeeklyHarness(t *testing.T, wi coord.WeeklyInterpreter) *harness {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg, err := coord.NewService(readingPlaybook())
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewOffset(clock.Fixed{T: t0})
	h := &harness{t: t, st: st, clk: clk, users: map[string]string{}}
	h.c = coord.NewCoordinator(reg, st, clk, coord.DraftOnlyPlanner{}, coord.Options{
		PublicBaseURL: "http://localhost:24680", Rand: func() float64 { return 0.5 }, WeeklyInterpreter: wi,
	})
	if err := st.Tx(ctx, func(tx *store.Tx) error {
		user, err := tx.UpsertUser(ctx, "222222222222222222", "B", t0)
		if err != nil {
			return err
		}
		h.users["B"] = user.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInterpretWeeklyAvailability_RejectsInvalidWeekdayAndOverlap(t *testing.T) {
	bad := coord.WeeklyInterpretation{Windows: []apitypes.WeeklyWindow{
		{Weekday: 9, Start: "19:00", End: "22:00"},
		{Weekday: 3, Start: "19:00", End: "21:00"},
		{Weekday: 3, Start: "20:00", End: "22:00"},
	}}
	h := newWeeklyHarness(t, fakeWeeklyInterpreter{out: bad})
	if _, err := h.c.InterpretWeeklyAvailability(ctx, h.users["B"], apitypes.WeeklyAvailabilityInterpretationInput{Text: "適当な入力"}); err == nil {
		t.Fatal("不正な曜日・重なった区間は拒否されるはず")
	}
}

func TestInterpretWeeklyAvailability_BudgetExceeded(t *testing.T) {
	good := coord.WeeklyInterpretation{Windows: []apitypes.WeeklyWindow{{Weekday: 3, Start: "19:00", End: "22:00"}}}
	h := newWeeklyHarness(t, fakeWeeklyInterpreter{out: good})
	var lastErr error
	for i := 0; i < coord.MaxWeeklyInterpretCallsPerUser+1; i++ {
		_, lastErr = h.c.InterpretWeeklyAvailability(ctx, h.users["B"], apitypes.WeeklyAvailabilityInterpretationInput{Text: "水 19:00-22:00"})
	}
	if lastErr == nil {
		t.Fatal("累計の呼び出し上限を超えたら拒否されるはず")
	}
}
