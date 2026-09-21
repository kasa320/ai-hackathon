package reading_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/playbook/reading"
)

func bookSections(n int) []coord.BookSection {
	out := make([]coord.BookSection, n)
	for i := range out {
		out[i] = coord.BookSection{ID: fmt.Sprintf("s%d", i+1), Title: fmt.Sprintf("第%d章", i+1)}
	}
	return out
}

func planInput(sections, count int) coord.BookPlanInput {
	return coord.BookPlanInput{
		Title: "本", Sections: bookSections(sections), PeriodStart: "2026-10-01", PeriodEnd: "2026-10-21", SlotCount: count, DurationMinutes: 60,
		Members: []coord.BookPlanMember{{ID: "a", DisplayName: "A", Role: "owner"}, {ID: "b", DisplayName: "B"}, {ID: "c", DisplayName: "C"}},
		Loads:   map[string]coord.BookMemberLoad{"a": {}, "b": {}, "c": {}},
	}
}

func TestSplitBookDividesPeriodAndAssignsEverySectionInOrder(t *testing.T) {
	slots, err := reading.New().SplitBook(bookSections(7), "2026-10-01", "2026-10-22", 3)
	if err != nil {
		t.Fatal(err)
	}
	// 22日を3回に分ける（7・7・8日）。7章は 3・2・2。
	wantWindows := [][2]string{{"2026-10-01", "2026-10-07"}, {"2026-10-08", "2026-10-14"}, {"2026-10-15", "2026-10-22"}}
	wantCounts := []int{3, 2, 2}
	next := 1
	for i, s := range slots {
		if s.Sequence != i+1 || s.PeriodStart != wantWindows[i][0] || s.PeriodEnd != wantWindows[i][1] || len(s.SectionIDs) != wantCounts[i] {
			t.Fatalf("枠%d = %+v", i+1, s)
		}
		for _, id := range s.SectionIDs {
			if id != fmt.Sprintf("s%d", next) {
				t.Fatalf("章の順序が登録順でない: %+v", slots)
			}
			next++
		}
	}
	for name, tc := range map[string]struct {
		sections int
		end      string
		n        int
		path     string
	}{
		"章が少ない":  {2, "2026-10-21", 3, "sections"},
		"日数が少ない": {6, "2026-10-02", 3, "period_end"},
		"回数0":    {6, "2026-10-21", 0, "planned_session_count"},
	} {
		_, err := reading.New().SplitBook(bookSections(tc.sections), "2026-10-01", tc.end, tc.n)
		if !hasPath(validationPaths(t, err), tc.path) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestDraftBookPlanBalancesLoadAcrossConcurrentBooks(t *testing.T) {
	pb := reading.New()
	in := planInput(6, 3)
	// a と b は別ブックで既に1件ずつ担当している。負担のない c が最初に選ばれ、同じ人は連続しない。
	in.Loads = map[string]coord.BookMemberLoad{"a": {Concurrent: 1}, "b": {Concurrent: 1}, "c": {}}
	p, err := pb.DraftBookPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Slots[0].AssigneeMemberID != "c" {
		t.Fatalf("負担のない人が最初でない: %+v", p.Slots)
	}
	for i := 1; i < len(p.Slots); i++ {
		if p.Slots[i].AssigneeMemberID == p.Slots[i-1].AssigneeMemberID {
			t.Fatalf("同じ人が連続: %+v", p.Slots)
		}
	}
	if err := pb.ValidateBookPlan(in, p); err != nil {
		t.Fatalf("規則で作った計画が自身の検証を通らない: %v", err)
	}
	// 別ブックの担当と日程が重なる人は、負担が同じなら後回しにする。
	in = planInput(6, 3)
	in.Others = []coord.BookOtherSlot{{BookTitle: "他", PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", AssigneeMemberID: "a"}}
	in.Loads["a"] = coord.BookMemberLoad{}
	p, _ = pb.DraftBookPlan(in)
	if p.Slots[0].AssigneeMemberID == "a" {
		t.Fatalf("日程が重なる人が最初に選ばれた: %+v", p.Slots)
	}
	// メンバーがいなければ作らない。
	in.Members = nil
	if _, err := pb.DraftBookPlan(in); err == nil {
		t.Fatal("メンバーがいないのに計画を作った")
	}
}

func TestValidateBookPlanRejectsFabricationAndImbalance(t *testing.T) {
	pb := reading.New()
	in := planInput(6, 3)
	good, _ := pb.DraftBookPlan(in)
	if err := pb.ValidateBookPlan(in, good); err != nil {
		t.Fatal(err)
	}
	clone := func() coord.BookPlan {
		out := coord.BookPlan{Summary: good.Summary}
		for _, s := range good.Slots {
			s.SectionIDs = append([]string{}, s.SectionIDs...)
			out.Slots = append(out.Slots, s)
		}
		return out
	}
	for name, tc := range map[string]struct {
		mutate func(*coord.BookPlan)
		path   string
	}{
		"存在しない章":    {func(p *coord.BookPlan) { p.Slots[0].SectionIDs[0] = "zzz" }, "slots"},
		"章の欠落":      {func(p *coord.BookPlan) { p.Slots[2].SectionIDs = p.Slots[2].SectionIDs[:1] }, "slots"},
		"空の回":       {func(p *coord.BookPlan) { p.Slots[1].SectionIDs = nil }, "slots[1].section_ids"},
		"捏造したメンバー":  {func(p *coord.BookPlan) { p.Slots[1].AssigneeMemberID = "ghost" }, "slots[1].assignee_member_id"},
		"担当なし":      {func(p *coord.BookPlan) { p.Slots[1].AssigneeMemberID = "" }, "slots[1].assignee_member_id"},
		"回の番号":      {func(p *coord.BookPlan) { p.Slots[1].Sequence = 5 }, "slots[1].sequence"},
		"期間の外":      {func(p *coord.BookPlan) { p.Slots[2].PeriodEnd = "2027-01-01" }, "slots[2].period_start"},
		"日付の形式":     {func(p *coord.BookPlan) { p.Slots[0].PeriodStart = "10/01" }, "slots[0].period_start"},
		"目安の順序が逆":   {func(p *coord.BookPlan) { p.Slots[1].PeriodStart, p.Slots[1].PeriodEnd = "2026-10-14", "2026-10-08" }, "slots[1].period_end"},
		"同じ人が連続":    {func(p *coord.BookPlan) { p.Slots[1].AssigneeMemberID = p.Slots[0].AssigneeMemberID }, "slots[1].assignee_member_id"},
		"負担が偏っている":  {func(p *coord.BookPlan) { p.Slots[1].AssigneeMemberID, p.Slots[2].AssigneeMemberID = "b", "b" }, "slots"},
	} {
		p := clone()
		tc.mutate(&p)
		if err := pb.ValidateBookPlan(in, p); err == nil || !hasPath(validationPaths(t, err), tc.path) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	// 同時進行中の別ブックの負担を考慮する：a が別ブックで重い場合、a を多く担当する計画は拒否される。
	in.Loads["a"] = coord.BookMemberLoad{Concurrent: 3}
	heavy := coord.BookPlan{Slots: clone().Slots}
	heavy.Slots[0].AssigneeMemberID, heavy.Slots[1].AssigneeMemberID, heavy.Slots[2].AssigneeMemberID = "a", "b", "a"
	if err := pb.ValidateBookPlan(in, heavy); err == nil {
		t.Fatal("別ブックで負担の重い人へ偏る計画が受理された")
	}
}

func TestReplacementRulesNeverPickCurrentExcludedOrHeavilyLoadedMember(t *testing.T) {
	pb := reading.New()
	in := coord.ReplacementInput{
		Sequence: 2, Current: "a", Excluded: []string{"b"},
		Members:       []coord.BookPlanMember{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}},
		Loads:         map[string]coord.BookMemberLoad{"a": {}, "b": {}, "c": {Concurrent: 2}, "d": {Concurrent: 1}},
		BookAssignees: map[int]string{1: "c", 2: "a", 3: "b"},
	}
	// a（現在の担当）・b（断った）は除き、隣の回の担当 c を避けて d を選ぶ。
	got, err := pb.DraftReplacement(in)
	if err != nil || got != "d" {
		t.Fatalf("候補 = %q, %v", got, err)
	}
	for _, bad := range []string{"a", "b", "c", "ghost"} {
		if err := pb.ValidateReplacement(in, bad); err == nil {
			t.Fatalf("%s を候補として受理した", bad)
		}
	}
	if err := pb.ValidateReplacement(in, "d"); err != nil {
		t.Fatal(err)
	}
	// 隣の担当を除くと候補がいなくなる場合は、隣の担当も候補にする（負担が最小の人）。
	in.Members = []coord.BookPlanMember{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	if got, err := pb.DraftReplacement(in); err != nil || got != "c" {
		t.Fatalf("隣の担当しか残らない場合 = %q, %v", got, err)
	}
	// 誰もいなければ ErrNoReplacement。
	in.Excluded = []string{"b", "c"}
	if _, err := pb.DraftReplacement(in); err == nil {
		t.Fatal("候補がいないのに選んだ")
	}
}

func TestValidateBookMaterial(t *testing.T) {
	pb := reading.New()
	secs := json.RawMessage(`[{"id":"a","title":"第1章"},{"id":"b","title":"第2章"}]`)
	m, err := pb.ValidateBookMaterial("  本  ", nil, json.RawMessage(`{}`), secs)
	if err != nil || m.Title != "本" || len(m.List) != 2 || string(m.TocSource) != `{"kind":"manual","urls":[]}` {
		t.Fatalf("%+v, %v", m, err)
	}
	empty := ""
	if m, err := pb.ValidateBookMaterial("本", &empty, nil, secs); err != nil || m.ISBN != nil {
		t.Fatalf("空のISBN: %+v, %v", m.ISBN, err)
	}
	bad := "9784297127832"
	if _, err := pb.ValidateBookMaterial("本", &bad, nil, secs); !hasPath(validationPaths(t, err), "isbn") {
		t.Fatalf("不正なISBN: %v", err)
	}
	if _, err := pb.ValidateBookMaterial("本", nil, nil, json.RawMessage(`[]`)); !hasPath(validationPaths(t, err), "sections") {
		t.Fatalf("章なし: %v", err)
	}
}

func TestPresenterMustBeTheApprovedAssignee(t *testing.T) {
	s := baseSnapshot()
	var d map[string]any
	_ = json.Unmarshal([]byte(sessionJSON), &d)
	d["assignee_member_id"] = "mem_c"
	raw, _ := json.Marshal(d)
	s.SessionData = raw
	pb := reading.New()
	// 担当（C）以外が発表する案は拒否する。
	err := pb.ValidatePlan(context.Background(), s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: json.RawMessage(initialPlanJSON)})
	if !hasPath(validationPaths(t, err), "agenda[1].presenter_member_id") {
		t.Fatalf("担当以外の発表者: %v", err)
	}
	draft, err := pb.DraftPlan(context.Background(), s)
	if err != nil || draft.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", draft, err)
	}
	var plan reading.PlanData
	_ = json.Unmarshal(draft.Plan, &plan)
	for _, item := range plan.Agenda {
		if item.PresenterMemberID != nil && *item.PresenterMemberID != "mem_c" {
			t.Fatalf("担当以外を割り当てた: %+v", plan)
		}
	}
	// 担当者が欠席なら、他の人へ回さず管理者へ戻す。
	setPrep(&s, "mem_c", prep("absent", false))
	draft, err = pb.DraftPlan(context.Background(), s)
	if err != nil || draft.Kind != coord.DraftNoFeasible {
		t.Fatalf("担当者が欠席: %+v, %v", draft, err)
	}
}

func TestStandingAvailabilityConvertsTimezoneAndYieldsToSessionAnswers(t *testing.T) {
	// New York の水曜 07:00〜09:00（EDT）は JST の水曜 20:00〜22:00。
	standing := &coord.StandingAvailability{Timezone: "America/New_York", Windows: []coord.StandingWindow{{Weekday: 3, StartMinute: 7 * 60, EndMinute: 9 * 60}}}
	s := baseSnapshot()
	s.Now = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	s.ScheduleStatus, s.PeriodStart, s.PeriodEnd = coord.ScheduleProposed, "2026-09-20", "2026-09-30"
	for i := range s.Members {
		s.Members[i].Standing = standing
	}
	pb := reading.New()
	draft, err := pb.DraftPlan(context.Background(), s)
	if err != nil || draft.Kind != coord.DraftProposal {
		t.Fatalf("普段の空き時間から案が作れない: %+v, %v", draft, err)
	}
	var plan reading.PlanData
	_ = json.Unmarshal(draft.Plan, &plan)
	if plan.StartsAt != "2026-09-23T20:00:00+09:00" {
		t.Fatalf("タイムゾーン変換 = %s", plan.StartsAt)
	}
	// 夏時間の終了後（11月）は EST（UTC-5）で JST の水曜 21:00 になる。
	s.PeriodStart, s.PeriodEnd = "2026-11-02", "2026-11-08"
	s.Now = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	draft, _ = pb.DraftPlan(context.Background(), s)
	_ = json.Unmarshal(draft.Plan, &plan)
	if plan.StartsAt != "2026-11-04T21:00:00+09:00" {
		t.Fatalf("夏時間終了後 = %s", plan.StartsAt)
	}
	// この回で時間帯を答えた人は、本人の回答を優先する（普段の空き時間は使わない）。
	s = baseSnapshot()
	s.Now = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	s.ScheduleStatus, s.PeriodStart, s.PeriodEnd = coord.ScheduleProposed, "2026-09-20", "2026-09-30"
	for i := range s.Members {
		s.Members[i].Standing = &coord.StandingAvailability{Timezone: "Asia/Tokyo", Windows: []coord.StandingWindow{{Weekday: 3, StartMinute: 20 * 60, EndMinute: 22 * 60}}}
	}
	changeAvailability(t, &s, "mem_a", func(d *reading.PreparationData) {
		d.Schedule = &reading.ScheduleAvailability{Status: "unavailable", WeeklyWindows: []reading.WeeklyWindow{}, DateWindows: []reading.DateWindow{}}
	})
	if draft, _ := pb.DraftPlan(context.Background(), s); draft.Kind == coord.DraftProposal {
		t.Fatalf("参加不可と答えた人がいるのに、普段の空き時間で案が作られた: %+v", draft)
	}
}

func TestOverlapWithConfirmedBusyIntervalIsRejected(t *testing.T) {
	s := scheduledSnapshot(t)
	pb := reading.New()
	draft, err := pb.DraftPlan(context.Background(), s)
	if err != nil || draft.Kind != coord.DraftProposal {
		t.Fatalf("draft = %+v, %v", draft, err)
	}
	var plan reading.PlanData
	_ = json.Unmarshal(draft.Plan, &plan)
	at, _ := time.Parse(time.RFC3339, plan.StartsAt)
	// 同じ時間帯に、メンバーの別の確定済み予定（別ブック・別グループを含む）がある。
	s.BusyIntervals = []coord.BusyInterval{{StartsAt: at.Add(-30 * time.Minute), EndsAt: at.Add(30 * time.Minute)}}
	err = pb.ValidatePlan(context.Background(), s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: draft.Plan})
	if !hasPath(validationPaths(t, err), "starts_at") {
		t.Fatalf("衝突する日時が受理された: %v", err)
	}
	// 規則の候補生成も衝突を避け、予定が終わった直後の候補を選ぶ。
	next, err := pb.DraftPlan(context.Background(), s)
	if err != nil || next.Kind != coord.DraftProposal {
		t.Fatalf("next = %+v, %v", next, err)
	}
	var np reading.PlanData
	_ = json.Unmarshal(next.Plan, &np)
	nat, _ := time.Parse(time.RFC3339, np.StartsAt)
	if nat.Before(s.BusyIntervals[0].EndsAt) {
		t.Fatalf("衝突を避けていない: %s", np.StartsAt)
	}
	// 終了時刻ちょうどから始まる予定は衝突しない。
	s.BusyIntervals = []coord.BusyInterval{{StartsAt: at.Add(-time.Hour), EndsAt: at}}
	if err := pb.ValidatePlan(context.Background(), s, coord.Proposal{ChangeKind: coord.ChangeInitial, Data: draft.Plan}); err != nil {
		t.Fatalf("隣り合う予定を衝突扱いにした: %v", err)
	}
}
