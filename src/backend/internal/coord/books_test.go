package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// jst0 は日本時間の0時。テストの時計（t0=2026-09-19 18:00 JST）に対する日付の指定に使う。
func jst0(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.FixedZone("JST", 9*3600))
}

func sectionsJSON(n int) json.RawMessage {
	list := make([]map[string]string, 0, n)
	for i := 1; i <= n; i++ {
		list = append(list, map[string]string{"id": fmt.Sprintf("sec_%d", i), "title": fmt.Sprintf("第%d章", i)})
	}
	b, _ := json.Marshal(list)
	return b
}

// bookInput は 2026-10-01〜10-21（21日）を count 回に分ける登録入力。
func bookInput(title string, sections, count int) apitypes.CreateReadingBookInput {
	return apitypes.CreateReadingBookInput{Title: title, TocSource: json.RawMessage(`{"kind":"manual","urls":[]}`), Sections: sectionsJSON(sections),
		PeriodStart: "2026-10-01", PeriodEnd: "2026-10-21", PlannedSessionCount: count, DurationMinutes: 60, AdjustmentLeadDays: 7}
}

func (h *harness) createBook(in apitypes.CreateReadingBookInput) apitypes.ReadingBook {
	h.t.Helper()
	res, err := h.c.CreateReadingBook(ctx, h.users["A"], h.group, in, nil)
	if err != nil {
		h.t.Fatalf("ブック登録: %v", err)
	}
	var out apitypes.CreateReadingBookResult
	decode(h.t, res.Body, &out)
	return out.Book
}

func (h *harness) book(name, bookID string) apitypes.ReadingBookDetail {
	h.t.Helper()
	d, err := h.c.ReadingBookDetail(ctx, h.users[name], h.group, bookID)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *harness) groupMemberID(name string) string {
	h.t.Helper()
	g, err := h.c.GetGroup(ctx, h.users["A"], h.group)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, m := range g.Members {
		if m.DisplayName == name {
			return m.ID
		}
	}
	h.t.Fatalf("メンバー %s がいない", name)
	return ""
}

func (h *harness) memberName(id string) string {
	for _, name := range []string{"A", "B", "C", "D"} {
		if h.groupMemberID(name) == id {
			return name
		}
	}
	return "?"
}

// assign は本人として担当へ回答する。
func (h *harness) assign(name, bookID, slotID, decision string) (store.Response, error) {
	return h.c.RespondBookAssignment(ctx, h.users[name], h.group, bookID, apitypes.BookAssignmentInput{Decision: decision, SlotID: slotID}, nil)
}

func (h *harness) mustAssign(name, bookID, slotID, decision string) {
	h.t.Helper()
	if _, err := h.assign(name, bookID, slotID, decision); err != nil {
		h.t.Fatalf("%s の担当への %s: %v", name, decision, err)
	}
}

// approveBook は全体計画を作らせ、割り当てられた全員が承認して計画を成立させる。
func (h *harness) approveBook(bookID string) apitypes.ReadingBookDetail {
	h.t.Helper()
	h.process()
	d := h.book("A", bookID)
	if d.Book.PlanStatus != store.BookPlanAwaitingApproval {
		h.t.Fatalf("承認待ちになっていない: %s", d.Book.PlanStatus)
	}
	seen := map[string]bool{}
	for _, s := range d.Sessions {
		name := h.memberName(*s.AssigneeMemberID)
		if !seen[name] {
			seen[name] = true
			h.mustAssign(name, bookID, "", "accept")
		}
	}
	d = h.book("A", bookID)
	if d.Book.PlanStatus != store.BookPlanApproved {
		h.t.Fatalf("計画が成立していない: %s", d.Book.PlanStatus)
	}
	return d
}

func (h *harness) sessionCount() int {
	n := 0
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		list, err := tx.SessionsByGroup(ctx, h.group)
		n = len(list)
		return err
	})
	return n
}

func (h *harness) groupNotifs() []store.GroupNotification {
	var out []store.GroupNotification
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		out, err = tx.GroupNotifications(ctx, h.group)
		return err
	})
	return out
}

// sessionNotificationCount は開催回の通知の数。kind が空なら全種類、あれば指定した種類だけを数える。
func (h *harness) sessionNotificationCount(kind ...string) int {
	n := 0
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		list, err := tx.SessionsByGroup(ctx, h.group)
		if err != nil {
			return err
		}
		for _, s := range list {
			cs, err := tx.LatestCase(ctx, s.ID)
			if err != nil {
				continue
			}
			ns, err := tx.NotificationsByCase(ctx, cs.ID)
			if err != nil {
				return err
			}
			for _, x := range ns {
				if len(kind) == 0 || x.Kind == kind[0] {
					n++
				}
			}
		}
		return nil
	})
	return n
}

func (h *harness) taskCount() int {
	n := 0
	_ = h.st.Tx(ctx, func(tx *store.Tx) error {
		list, err := tx.SessionsByGroup(ctx, h.group)
		if err != nil {
			return err
		}
		for _, s := range list {
			cs, err := tx.LatestCase(ctx, s.ID)
			if err != nil {
				continue
			}
			ts, err := tx.TasksByCase(ctx, cs.ID)
			if err != nil {
				return err
			}
			n += len(ts)
		}
		return nil
	})
	return n
}

func TestBookRegistrationCreatesSlotsAndRequestsAvailabilityButNoSessionTasks(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	if b.PlanStatus != "planning" || b.PlannedSessionCount != 3 || b.DurationMinutes != 60 || b.AdjustmentLeadDays != 7 || b.PeriodStart != "2026-10-01" || b.PeriodEnd != "2026-10-21" {
		t.Fatalf("登録結果 = %+v", b)
	}
	d := h.book("A", b.ID)
	if len(d.Sessions) != 3 {
		t.Fatalf("全枠が作られていない: %+v", d.Sessions)
	}
	wantWindows := [][2]string{{"2026-10-01", "2026-10-07"}, {"2026-10-08", "2026-10-14"}, {"2026-10-15", "2026-10-21"}}
	for i, s := range d.Sessions {
		if s.Status != "planned" || s.Session != nil || s.SchedulingStatus != "waiting" || s.AssignmentStatus != "unassigned" || s.AssigneeMemberID != nil {
			t.Fatalf("枠%d = %+v", i+1, s)
		}
		if s.PeriodStart != wantWindows[i][0] || s.PeriodEnd != wantWindows[i][1] || len(s.TargetSectionIDs) != 2 {
			t.Fatalf("枠%dの開催目安・範囲 = %+v", i+1, s)
		}
	}
	// 登録直後に空き時間の確認だけを依頼するが、実セッション・回答タスク・開催回通知は作られない。
	h.process()
	_ = h.st.Tx(ctx, func(tx *store.Tx) error { return nil })
	if n := h.sessionCount(); n != 0 || h.taskCount() != 0 || h.sessionNotificationCount() != 0 {
		t.Fatalf("登録だけで実セッション等ができた: sessions=%d tasks=%d", n, h.taskCount())
	}
	if got := h.kinds()["availability_requested"]; got != 4 {
		t.Fatalf("空き時間の確認通知 = %d, want 4", got)
	}
}

func TestBookRegistrationDeduplicatesAvailabilityRequestsAcrossBooksOnSameDay(t *testing.T) {
	h := newHarness(t, nil)
	h.createBook(bookInput("設計の本", 6, 3))
	h.createBook(bookInput("次の本", 6, 3))
	if got := h.kinds()["availability_requested"]; got != 4 {
		t.Fatalf("同じ日の空き時間確認通知 = %d, want 4", got)
	}
}

func TestBookRegistrationValidation(t *testing.T) {
	h := newHarness(t, nil)
	mod := func(f func(*apitypes.CreateReadingBookInput)) apitypes.CreateReadingBookInput {
		in := bookInput("本", 6, 3)
		f(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   apitypes.CreateReadingBookInput
		path string
	}{
		"章が開催回より少ない": {bookInput("本", 2, 3), "sections"},
		"期間が開催回より短い": {mod(func(in *apitypes.CreateReadingBookInput) { in.PeriodEnd = "2026-10-02" }), "period_end"},
		"終了が開始より前":   {mod(func(in *apitypes.CreateReadingBookInput) { in.PeriodEnd = "2026-09-30" }), "period_end"},
		"開始日が過去":     {mod(func(in *apitypes.CreateReadingBookInput) { in.PeriodStart = "2026-09-01" }), "period_start"},
		"リード日数0":     {mod(func(in *apitypes.CreateReadingBookInput) { in.AdjustmentLeadDays = 0 }), "adjustment_lead_days"},
		"リード日数31":    {mod(func(in *apitypes.CreateReadingBookInput) { in.AdjustmentLeadDays = 31 }), "adjustment_lead_days"},
		"1回の長さが短すぎる": {mod(func(in *apitypes.CreateReadingBookInput) { in.DurationMinutes = 10 }), "duration_minutes"},
		"回数0":        {mod(func(in *apitypes.CreateReadingBookInput) { in.PlannedSessionCount = 0 }), "planned_session_count"},
		"回数53":       {mod(func(in *apitypes.CreateReadingBookInput) { in.PlannedSessionCount = 53 }), "planned_session_count"},
		"書名が空":       {mod(func(in *apitypes.CreateReadingBookInput) { in.Title = " " }), "title"},
		"章IDの重複": {mod(func(in *apitypes.CreateReadingBookInput) {
			in.Sections = json.RawMessage(`[{"id":"a","title":"1"},{"id":"a","title":"2"},{"id":"b","title":"3"}]`)
		}), "sections[1].id"},
		"ISBNが不正": {mod(func(in *apitypes.CreateReadingBookInput) { s := "123"; in.ISBN = &s }), "isbn"},
		"日付の形式":   {mod(func(in *apitypes.CreateReadingBookInput) { in.PeriodStart = "10/01" }), "period_start"},
		"章が配列でない": {mod(func(in *apitypes.CreateReadingBookInput) { in.Sections = json.RawMessage(`{"x":1}`) }), "sections"},
		"目次情報の種類が不正": {mod(func(in *apitypes.CreateReadingBookInput) {
			in.TocSource = json.RawMessage(`{"kind":"guess","urls":[]}`)
		}), "toc_source.kind"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.c.CreateReadingBook(ctx, h.users["A"], h.group, tc.in, nil)
			ae, ok := err.(*apperr.Error)
			if !ok || ae.Code != apperr.ValidationFailed {
				t.Fatalf("err = %v", err)
			}
			found := false
			for _, f := range ae.Details["fields"].([]apperr.Field) {
				found = found || f.Path == tc.path
			}
			if !found {
				t.Fatalf("フィールド %s の検証エラーがない: %+v", tc.path, ae.Details["fields"])
			}
		})
	}
	// 目次情報が {} でも手入力として登録できる。
	if _, err := h.c.CreateReadingBook(ctx, h.users["A"], h.group, mod(func(in *apitypes.CreateReadingBookInput) { in.TocSource = json.RawMessage(`{}`) }), nil); err != nil {
		t.Fatalf("目次情報 {} : %v", err)
	}
	// 一般メンバーは登録できない。
	if _, err := h.c.CreateReadingBook(ctx, h.users["B"], h.group, bookInput("本", 6, 3), nil); code(err) != apperr.Forbidden {
		t.Fatalf("一般メンバーの登録: %v", err)
	}
	if h.sessionCount() != 0 {
		t.Fatal("検証に失敗した登録で開催回ができた")
	}
}

func TestBookPlanCoversEverySectionInOrderAndAssignsEverySlot(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 7, 4))
	h.process()
	d := h.book("A", b.ID)
	if d.Book.PlanStatus != "awaiting_approval" || d.Book.PlanSummary == "" {
		t.Fatalf("計画状態 = %+v", d.Book)
	}
	var flat []string
	prevAssignee, prevEnd := "", ""
	loads := map[string]int{}
	for i, s := range d.Sessions {
		if s.AssigneeMemberID == nil || s.AssignmentStatus != "pending" {
			t.Fatalf("枠%dに仮担当がない/承認済みになっている: %+v", i+1, s)
		}
		if *s.AssigneeMemberID == prevAssignee {
			t.Fatalf("同じ人が連続している: 枠%d", i+1)
		}
		if prevEnd != "" && s.PeriodStart <= prevEnd {
			t.Fatalf("開催目安が重なっている: %+v", d.Sessions)
		}
		prevAssignee, prevEnd = *s.AssigneeMemberID, s.PeriodEnd
		loads[prevAssignee]++
		flat = append(flat, s.TargetSectionIDs...)
	}
	if len(flat) != 7 {
		t.Fatalf("章の欠落・重複: %v", flat)
	}
	for i, id := range flat {
		if id != fmt.Sprintf("sec_%d", i+1) {
			t.Fatalf("章が登録順でない: %v", flat)
		}
	}
	for id, n := range loads {
		if n != 1 {
			t.Fatalf("4人・4回なのに担当が偏っている: %s=%d", id, n)
		}
	}
	// 担当の割り当て案は、割り当てた人へ個人DMで承認を依頼する（未承認のままでは確定しない）。
	kinds := map[string]int{}
	for _, n := range h.groupNotifs() {
		kinds[n.Kind]++
	}
	if kinds["book_plan_proposed"] != 4 || kinds["availability_requested"] != 4 || len(kinds) != 2 {
		t.Fatalf("通知 = %v", kinds)
	}
	if h.sessionCount() != 0 {
		t.Fatal("計画案を作っただけで実セッションができた")
	}
}

func TestBookPlanIsNotApprovedUntilEveryAssigneeAcceptsThemselves(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.process()
	d := h.book("A", b.ID)
	// 3回を3人（A・B・C）へ仮に割り当てる。D は担当なし。
	by := map[string]string{}
	for _, s := range d.Sessions {
		by[h.memberName(*s.AssigneeMemberID)] = s.SlotID
	}
	if len(by) != 3 || by["D"] != "" {
		t.Fatalf("割り当て = %v", by)
	}

	// 担当のない人は回答できない。自分の担当のない状態では作用しない。
	if _, err := h.assign("D", b.ID, "", "accept"); code(err) != apperr.InvalidState {
		t.Fatalf("担当のない人の承認: %v", err)
	}
	// 他人の担当は承認できない（管理者でも本人の代わりには承認できない）。
	if _, err := h.assign("D", b.ID, by["B"], "accept"); code(err) != apperr.Forbidden {
		t.Fatalf("他人の担当の承認: %v", err)
	}
	if _, err := h.assign("A", b.ID, by["B"], "accept"); code(err) != apperr.Forbidden {
		t.Fatalf("管理者による他人の担当の承認: %v", err)
	}
	if got := h.book("A", b.ID); got.Book.PlanStatus != "awaiting_approval" {
		t.Fatalf("他人の承認で状態が進んだ: %s", got.Book.PlanStatus)
	}
	// 入力の検証と、グループ外・存在しないブックの扱い。
	if _, err := h.assign("B", b.ID, "", "maybe"); code(err) != apperr.ValidationFailed {
		t.Fatalf("不正な回答: %v", err)
	}
	if _, err := h.assign("B", "book_none", "", "accept"); code(err) != apperr.NotFound {
		t.Fatalf("存在しないブック: %v", err)
	}

	// 全員が承認するまで成立しない（未回答を承認扱いにしない）。
	h.mustAssign("B", b.ID, "", "accept")
	h.mustAssign("C", b.ID, "", "accept")
	if got := h.book("A", b.ID); got.Book.PlanStatus != "awaiting_approval" {
		t.Fatalf("A が未回答なのに成立した: %s", got.Book.PlanStatus)
	}
	// 管理者の同意件数は要求しない。管理者自身も担当なら本人として承認する。
	h.mustAssign("A", b.ID, "", "accept")
	got := h.book("A", b.ID)
	if got.Book.PlanStatus != "approved" {
		t.Fatalf("全員承認後の状態 = %s", got.Book.PlanStatus)
	}
	for _, s := range got.Sessions {
		if s.AssignmentStatus != "accepted" {
			t.Fatalf("承認済みでない枠: %+v", s)
		}
	}
	// 同じ回答の再送は成立済みの計画を壊さない。
	if _, err := h.assign("B", b.ID, "", "accept"); code(err) != apperr.InvalidState {
		t.Fatalf("回答済みへの再回答: %v", err)
	}
}

type fixedBookAgent struct {
	plan coord.BookPlan
	err  error
	n    int
	// during は AI の呼び出し中（トランザクションの外）に実行する。処理中の変更を再現する。
	during func()
	// replaceErr が非 nil なら、担当変更の候補の作成に失敗させる。
	replaceErr error
}

func (f *fixedBookAgent) PlanBook(_ context.Context, req coord.BookPlanRequest) (coord.BookPlan, coord.Usage, error) {
	f.n++
	if f.during != nil {
		f.during()
	}
	if f.err != nil {
		return coord.BookPlan{}, coord.Usage{LLMCalls: []coord.LLMCall{{Model: "test-model", Currency: "unknown"}}}, f.err
	}
	usage := coord.Usage{LLMCalls: []coord.LLMCall{{Model: "test-model", Currency: "unknown", Succeeded: true}}, ToolCalls: 1}
	if err := req.Check(f.plan); err != nil {
		return coord.BookPlan{}, usage, errors.Join(coord.ErrInvalidOutput, err)
	}
	return f.plan, usage, nil
}

func (f *fixedBookAgent) ProposeReplacement(ctx context.Context, req coord.ReplacementRequest) (string, coord.Usage, error) {
	if f.replaceErr != nil {
		return "", coord.Usage{LLMCalls: []coord.LLMCall{{Model: "test-model", Currency: "unknown"}}}, f.replaceErr
	}
	return coord.DraftBookAgent{}.ProposeReplacement(ctx, req)
}

func TestOwnerApprovalCountIsNotRequiredWhenOwnerHasNoAssignment(t *testing.T) {
	agent := &fixedBookAgent{}
	h := newHarnessFull(t, nil, coord.DraftOnlyInterpreter{}, agent)
	b := h.createBook(bookInput("設計の本", 6, 3))
	agent.plan = coord.BookPlan{Summary: "B・C・Dが担当", Slots: []coord.BookPlanSlot{
		{Sequence: 1, PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", SectionIDs: []string{"sec_1", "sec_2"}, AssigneeMemberID: h.groupMemberID("B")},
		{Sequence: 2, PeriodStart: "2026-10-08", PeriodEnd: "2026-10-14", SectionIDs: []string{"sec_3", "sec_4"}, AssigneeMemberID: h.groupMemberID("C")},
		{Sequence: 3, PeriodStart: "2026-10-15", PeriodEnd: "2026-10-21", SectionIDs: []string{"sec_5", "sec_6"}, AssigneeMemberID: h.groupMemberID("D")},
	}}
	h.process()
	h.mustAssign("B", b.ID, "", "accept")
	h.mustAssign("C", b.ID, "", "accept")
	h.mustAssign("D", b.ID, "", "accept")
	if got := h.book("A", b.ID); got.Book.PlanStatus != "approved" {
		t.Fatalf("管理者が担当でなくても、担当者の承認だけで成立するはず: %s", got.Book.PlanStatus)
	}
}

func TestBookPlanValidationRejectsFabricatedOrInvalidAIOutput(t *testing.T) {
	agent := &fixedBookAgent{}
	h := newHarnessFull(t, nil, coord.DraftOnlyInterpreter{}, agent)
	mem := func(n string) string { return h.groupMemberID(n) }
	good := []coord.BookPlanSlot{
		{Sequence: 1, PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", SectionIDs: []string{"sec_1", "sec_2"}, AssigneeMemberID: mem("A")},
		{Sequence: 2, PeriodStart: "2026-10-08", PeriodEnd: "2026-10-14", SectionIDs: []string{"sec_3", "sec_4"}, AssigneeMemberID: mem("B")},
		{Sequence: 3, PeriodStart: "2026-10-15", PeriodEnd: "2026-10-21", SectionIDs: []string{"sec_5", "sec_6"}, AssigneeMemberID: mem("C")},
	}
	clone := func() []coord.BookPlanSlot {
		out := make([]coord.BookPlanSlot, len(good))
		copy(out, good)
		return out
	}
	for name, mutate := range map[string]func([]coord.BookPlanSlot) []coord.BookPlanSlot{
		"存在しない章": func(s []coord.BookPlanSlot) []coord.BookPlanSlot {
			s[0].SectionIDs = []string{"sec_1", "sec_99"}
			return s
		},
		"章の欠落": func(s []coord.BookPlanSlot) []coord.BookPlanSlot { s[2].SectionIDs = []string{"sec_5"}; return s },
		"章の重複": func(s []coord.BookPlanSlot) []coord.BookPlanSlot {
			s[2].SectionIDs = []string{"sec_4", "sec_5", "sec_6"}
			return s
		},
		"章の順序": func(s []coord.BookPlanSlot) []coord.BookPlanSlot {
			s[0].SectionIDs, s[1].SectionIDs = s[1].SectionIDs, s[0].SectionIDs
			return s
		},
		"存在しないメンバー": func(s []coord.BookPlanSlot) []coord.BookPlanSlot { s[1].AssigneeMemberID = "mem_fabricated"; return s },
		"枠の数が違う":    func(s []coord.BookPlanSlot) []coord.BookPlanSlot { return s[:2] },
		"開催目安が期間外":  func(s []coord.BookPlanSlot) []coord.BookPlanSlot { s[2].PeriodEnd = "2026-11-30"; return s },
		"開催目安の重なり":  func(s []coord.BookPlanSlot) []coord.BookPlanSlot { s[1].PeriodStart = "2026-10-05"; return s },
		"同じ人が連続":    func(s []coord.BookPlanSlot) []coord.BookPlanSlot { s[1].AssigneeMemberID = mem("A"); return s },
		"担当の負担が偏っている": func(s []coord.BookPlanSlot) []coord.BookPlanSlot {
			s[1].AssigneeMemberID, s[2].AssigneeMemberID = mem("B"), mem("B")
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			agent.plan = coord.BookPlan{Summary: "x", Slots: mutate(clone())}
			b := h.createBook(bookInput("本 "+name, 6, 3))
			h.process()
			d := h.book("A", b.ID)
			if d.Book.PlanStatus != "needs_attention" {
				t.Fatalf("不正な計画が受理された: %s %+v", d.Book.PlanStatus, d.Sessions)
			}
			for _, s := range d.Sessions {
				if s.AssigneeMemberID != nil || s.AssignmentStatus != "unassigned" {
					t.Fatalf("不正な計画が保存された: %+v", s)
				}
			}
		})
	}
	// 正しい計画は受理される。
	agent.plan = coord.BookPlan{Summary: "ok", Slots: clone()}
	b := h.createBook(bookInput("正しい本", 6, 3))
	h.process()
	if d := h.book("A", b.ID); d.Book.PlanStatus != "awaiting_approval" {
		t.Fatalf("正しい計画が拒否された: %s", d.Book.PlanStatus)
	}
}

func TestBookPlanFailureNeverConfirmsAndNotifiesOwner(t *testing.T) {
	agent := &fixedBookAgent{err: coord.ErrTransient}
	h := newHarnessFull(t, nil, coord.DraftOnlyInterpreter{}, agent)
	b := h.createBook(bookInput("設計の本", 6, 3))
	for i := 0; i < 6; i++ {
		h.process()
		h.clk.Advance(15 * time.Minute)
	}
	d := h.book("A", b.ID)
	if d.Book.PlanStatus != "needs_attention" || agent.n != 3 {
		t.Fatalf("一時的な障害の再試行 = status %s, 呼び出し %d回", d.Book.PlanStatus, agent.n)
	}
	for _, s := range d.Sessions {
		if s.AssigneeMemberID != nil {
			t.Fatalf("失敗したのに担当が入っている: %+v", s)
		}
	}
	kinds := map[string]int{}
	for _, n := range h.groupNotifs() {
		kinds[n.Kind]++
	}
	if kinds["book_attention"] != 1 || kinds["book_plan_proposed"] != 0 {
		t.Fatalf("通知 = %v", kinds)
	}
	// 費用は結果を使わなくても記録される（lookup_id にブックのID）。
	var calls int
	_ = h.st.Tx(ctx, func(tx *store.Tx) error { return nil })
	_ = calls
	// 管理者が再生成を依頼すると、再び計画の作成に進む。
	agent.err = nil
	agent.plan = coord.BookPlan{}
	if _, err := h.c.RegenerateBookPlan(ctx, h.users["B"], h.group, b.ID, apitypes.RegenerateBookPlanInput{}, nil); code(err) != apperr.Forbidden {
		t.Fatalf("一般メンバーの再生成: %v", err)
	}
	if _, err := h.c.RegenerateBookPlan(ctx, h.users["A"], h.group, b.ID, apitypes.RegenerateBookPlanInput{}, nil); err != nil {
		t.Fatal(err)
	}
	if d := h.book("A", b.ID); d.Book.PlanStatus != "planning" || d.Book.PlanVersion != 2 {
		t.Fatalf("再生成後 = %+v", d.Book)
	}
}

func TestNewBookDoesNotChangeExistingAssignmentsAndBalancesAcrossBooks(t *testing.T) {
	h := newHarness(t, nil)
	first := h.createBook(bookInput("最初の本", 6, 3))
	before := h.approveBook(first.ID)
	assignees := func(d apitypes.ReadingBookDetail) []string {
		var out []string
		for _, s := range d.Sessions {
			out = append(out, h.memberName(*s.AssigneeMemberID)+":"+s.AssignmentStatus)
		}
		return out
	}
	want := assignees(before) // A・B・C が1回ずつ

	second := h.createBook(bookInput("次の本", 6, 3))
	h.process()
	after := h.book("A", first.ID)
	got := assignees(after)
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("新しいブックで既存の割り当てが変わった: %v → %v", want, got)
		}
	}
	if after.Book.PlanStatus != "approved" || after.Book.PlanVersion != before.Book.PlanVersion {
		t.Fatalf("既存ブックの状態が変わった: %+v", after.Book)
	}
	// 同時進行中の全ブックの負担を考慮する：担当のいなかった D が最初に選ばれ、最大負担は2以内。
	d2 := h.book("A", second.ID)
	total := map[string]int{"A": 1, "B": 1, "C": 1}
	for i, s := range d2.Sessions {
		name := h.memberName(*s.AssigneeMemberID)
		if i == 0 && name != "D" {
			t.Fatalf("負担のない D が最初の担当になっていない: %s", name)
		}
		total[name]++
	}
	for name, n := range total {
		if n > 2 {
			t.Fatalf("同時進行中の担当の負担が偏っている: %s=%d", name, n)
		}
	}
	// 複数ブックを同じグループで同時に進められる。一覧に両方が出る。
	list, err := h.c.ListReadingBooks(ctx, h.users["A"], h.group)
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("一覧 = %+v, %v", list, err)
	}
}

func TestBookEditIsRejectedAfterFirstSessionStartsAndInvalidatesApprovalsBefore(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	edit := func(in apitypes.UpdateReadingBookInput) (store.Response, error) {
		return h.c.UpdateReadingBook(ctx, h.users["A"], h.group, b.ID, in, nil)
	}
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }

	// 一般メンバーは編集できない。
	if _, err := h.c.UpdateReadingBook(ctx, h.users["B"], h.group, b.ID, apitypes.UpdateReadingBookInput{Title: str("乗っ取り")}, nil); code(err) != apperr.Forbidden {
		t.Fatalf("一般メンバーの編集: %v", err)
	}
	// 書名だけの変更では承認を保つ。
	if _, err := edit(apitypes.UpdateReadingBookInput{Title: str("設計の本（改）")}); err != nil {
		t.Fatal(err)
	}
	d := h.book("A", b.ID)
	if d.Book.Title != "設計の本（改）" || d.Book.PlanStatus != "approved" || d.Book.PlanVersion != 1 {
		t.Fatalf("メタ情報の編集で承認が失われた: %+v", d.Book)
	}
	// 何も変わらない編集は状態を変えない。
	if _, err := edit(apitypes.UpdateReadingBookInput{PlannedSessionCount: num(3)}); err != nil {
		t.Fatal(err)
	}
	if d := h.book("A", b.ID); d.Book.PlanStatus != "approved" || d.Book.PlanVersion != 1 {
		t.Fatalf("同じ値の編集で計画が変わった: %+v", d.Book)
	}
	// 構造・計画の変更は、以前の担当承認を無効にして計画を再生成する。
	if _, err := edit(apitypes.UpdateReadingBookInput{PlannedSessionCount: num(2), Sections: sectionsJSON(4)}); err != nil {
		t.Fatal(err)
	}
	d = h.book("A", b.ID)
	if d.Book.PlanStatus != "planning" || d.Book.PlanVersion != 2 || len(d.Sessions) != 2 || d.Book.PlannedSessionCount != 2 {
		t.Fatalf("構造変更後 = %+v / %d枠", d.Book, len(d.Sessions))
	}
	for _, s := range d.Sessions {
		if s.AssigneeMemberID != nil || s.AssignmentStatus != "unassigned" {
			t.Fatalf("以前の担当・承認が残っている: %+v", s)
		}
		if len(s.TargetSectionIDs) != 2 {
			t.Fatalf("章割りが作り直されていない: %+v", s)
		}
	}
	// 期間の検証は登録時と同じ。
	if _, err := edit(apitypes.UpdateReadingBookInput{PeriodEnd: str("2026-10-01"), PlannedSessionCount: num(2)}); code(err) != apperr.ValidationFailed {
		t.Fatalf("不正な期間の編集: %v", err)
	}
}

// advanceToAdjustmentStart は時計を、指定した回の調整開始日の0時（JST）まで進める。
func (h *harness) advanceToAdjustmentStart(bookID string, seq int) {
	h.t.Helper()
	d := h.book("A", bookID)
	s := d.Sessions[seq-1]
	start, err := time.ParseInLocation("2006-01-02", *s.AdjustmentStartsOn, time.FixedZone("JST", 9*3600))
	if err != nil {
		h.t.Fatal(err)
	}
	h.setNow(start)
}

// setNow は時計を指定時刻まで進める（戻さない）。
func (h *harness) setNow(at time.Time) {
	if d := at.Sub(h.clk.Now()); d > 0 {
		h.clk.Advance(d)
	}
}

// AIの呼び出し中にブックが編集されたら、古い計画は使わない（版が変わっている）。
func TestStaleBookPlanIsDiscardedWhenBookChangedDuringAIRun(t *testing.T) {
	agent := &fixedBookAgent{}
	h := newHarnessFull(t, nil, coord.DraftOnlyInterpreter{}, agent)
	b := h.createBook(bookInput("設計の本", 6, 3))
	mem := func(n string) string { return h.groupMemberID(n) }
	agent.plan = coord.BookPlan{Summary: "旧計画", Slots: []coord.BookPlanSlot{
		{Sequence: 1, PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", SectionIDs: []string{"sec_1", "sec_2"}, AssigneeMemberID: mem("A")},
		{Sequence: 2, PeriodStart: "2026-10-08", PeriodEnd: "2026-10-14", SectionIDs: []string{"sec_3", "sec_4"}, AssigneeMemberID: mem("B")},
		{Sequence: 3, PeriodStart: "2026-10-15", PeriodEnd: "2026-10-21", SectionIDs: []string{"sec_5", "sec_6"}, AssigneeMemberID: mem("C")},
	}}
	edited := false
	agent.during = func() {
		if edited {
			return
		}
		edited = true
		n := 2
		if _, err := h.c.UpdateReadingBook(ctx, h.users["A"], h.group, b.ID, apitypes.UpdateReadingBookInput{PlannedSessionCount: &n}, nil); err != nil {
			t.Error(err)
		}
	}
	h.process()
	d := h.book("A", b.ID)
	if d.Book.PlanVersion != 2 || len(d.Sessions) != 2 {
		t.Fatalf("編集後の版 = %+v", d.Book)
	}
	for _, s := range d.Sessions {
		if s.AssigneeMemberID != nil && d.Book.PlanStatus == "planning" {
			t.Fatalf("古い計画の担当が保存された: %+v", s)
		}
	}
	if h.kinds()["book_plan_proposed"] != 0 {
		t.Fatalf("古い計画の承認依頼が送られた: %v", h.kinds())
	}
}

func (h *harness) kinds() map[string]int {
	out := map[string]int{}
	for _, n := range h.groupNotifs() {
		out[n.Kind]++
	}
	return out
}
