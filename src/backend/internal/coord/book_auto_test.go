package coord_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// restart はプロセスの再起動を模す。DB・時計は引き継ぎ、Coordinator を作り直す。
func (h *harness) restart() {
	h.t.Helper()
	reg, err := coord.NewService(readingPlaybook())
	if err != nil {
		h.t.Fatal(err)
	}
	h.c = coord.NewCoordinator(reg, h.st, h.clk, coord.DraftOnlyPlanner{}, coord.Options{PublicBaseURL: "http://localhost:24680", Rand: func() float64 { return 0.5 }, Interpreter: coord.DraftOnlyInterpreter{}})
}

func TestSlotsStartExactlyOnceWhenAdjustmentDayArrives(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3)) // 調整開始日は 9/24・10/1・10/8
	d := h.approveBook(b.ID)
	for i, want := range []string{"2026-09-24", "2026-10-01", "2026-10-08"} {
		if d.Sessions[i].AdjustmentStartsOn == nil || *d.Sessions[i].AdjustmentStartsOn != want {
			t.Fatalf("枠%dの調整開始日 = %v, want %s", i+1, d.Sessions[i].AdjustmentStartsOn, want)
		}
	}
	// 計画が成立しても、開始日前は何も作らない。
	h.process()
	h.setNow(jst0(2026, 9, 23).Add(23*time.Hour + 59*time.Minute))
	h.process()
	if h.sessionCount() != 0 || h.taskCount() != 0 {
		t.Fatalf("調整開始日の前に実セッションができた: %d", h.sessionCount())
	}

	// 時計を進めると、その日を迎えた枠（第1回）だけが開始される。何度処理しても、再起動しても1回だけ。
	h.setNow(jst0(2026, 9, 24))
	for i := 0; i < 3; i++ {
		h.process()
	}
	h.restart()
	h.process()
	if n := h.sessionCount(); n != 1 {
		t.Fatalf("セッション数 = %d, want 1", n)
	}
	if n := h.taskCount(); n != 4 {
		t.Fatalf("参加条件タスク数 = %d, want 4（全メンバーに1件ずつ）", n)
	}
	if n := h.sessionNotificationCount("task_requested"); n != 4 {
		t.Fatalf("参加条件の依頼通知数 = %d, want 4", n)
	}
	got := h.book("A", b.ID)
	if got.Sessions[0].Status != "active" || got.Sessions[0].Session == nil || got.Sessions[0].SchedulingStatus != "scheduling" {
		t.Fatalf("第1回 = %+v", got.Sessions[0])
	}
	for _, s := range got.Sessions[1:] {
		if s.Status != "planned" || s.Session != nil || s.SchedulingStatus != "waiting" {
			t.Fatalf("対象外の枠が開始された: %+v", s)
		}
	}
	before := h.kinds()

	// 次の枠は、その調整開始日になったときに1回だけ始まる。既存の枠を作り直さない。
	h.setNow(jst0(2026, 10, 1))
	h.process()
	h.process()
	if h.sessionCount() != 2 || h.taskCount() != 8 || h.sessionNotificationCount("task_requested") != 8 {
		t.Fatalf("第2回の開始 = sessions %d, tasks %d, notifications %d", h.sessionCount(), h.taskCount(), h.sessionNotificationCount("task_requested"))
	}
	if h.kinds()["book_plan_proposed"] != before["book_plan_proposed"] {
		t.Fatalf("担当承認の依頼が重複した: %v", h.kinds())
	}
}

func TestFirstSlotWhoseAdjustmentDayHasPassedStartsOnlyAfterPlanApproval(t *testing.T) {
	h := newHarness(t, nil)
	in := bookInput("設計の本", 6, 3)
	in.PeriodStart, in.PeriodEnd = "2026-09-20", "2026-10-10" // 第1回の調整開始日 9/13 は過去
	b := h.createBook(in)
	h.process()
	h.process()
	if h.sessionCount() != 0 {
		t.Fatal("計画の承認前に実セッションができた")
	}
	d := h.book("A", b.ID)
	for _, s := range d.Sessions {
		// 一部の担当だけが承認しても開始しない。
		if h.memberName(*s.AssigneeMemberID) == "A" {
			h.mustAssign("A", b.ID, "", "accept")
		}
	}
	h.process()
	if h.sessionCount() != 0 {
		t.Fatal("承認が揃う前に実セッションができた")
	}
	h.approveBookRest(b.ID)
	h.process()
	if h.sessionCount() != 1 || h.book("A", b.ID).Sessions[0].Status != "active" {
		t.Fatalf("承認後に第1回が始まっていない: sessions=%d", h.sessionCount())
	}
	h.process()
	if h.sessionCount() != 1 || h.taskCount() != 4 {
		t.Fatalf("重複: sessions=%d tasks=%d", h.sessionCount(), h.taskCount())
	}
}

// approveBookRest は未承認の担当者が、それぞれ承認する。
func (h *harness) approveBookRest(bookID string) {
	h.t.Helper()
	for _, name := range []string{"A", "B", "C", "D"} {
		d := h.book(name, bookID)
		if d.Permissions.CanRespondAssignment {
			h.mustAssign(name, bookID, "", "accept")
		}
	}
	if got := h.book("A", bookID).Book.PlanStatus; got != "approved" {
		h.t.Fatalf("計画が成立していない: %s", got)
	}
}

func TestManualSessionStartIsRejectedForPlannedBooks(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	d := h.book("A", b.ID)
	_, err := h.c.StartReadingBookSession(ctx, h.users["A"], h.group, b.ID, apitypes.ReadingBookSessionInput{SlotID: d.Sessions[0].SlotID, PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", DurationMinutes: 60, TargetSectionIDs: []string{"sec_1"}}, nil)
	if code(err) != apperr.InvalidState {
		t.Fatalf("手動開始 = %v", err)
	}
	if h.sessionCount() != 0 {
		t.Fatal("手動開始で計画を迂回できた")
	}
}

// weeklyAll は毎日 19:00〜22:00（JST）に参加できる、回の参加条件の時間帯。
func weeklyAll() json.RawMessage {
	var ws []map[string]any
	for day := 0; day <= 6; day++ {
		ws = append(ws, map[string]any{"weekday": day, "start": "19:00", "end": "22:00"})
	}
	b, _ := json.Marshal(map[string]any{"declined_presentation": false, "unavailable_dates": []string{}, "schedule": map[string]any{
		"status": "provided", "weekly_windows": ws, "date_windows": []any{}, "max_duration_minutes": 0}})
	return b
}

func (h *harness) startedSession(bookID string, seq int) string {
	h.t.Helper()
	d := h.book("A", bookID)
	if d.Sessions[seq-1].Session == nil {
		h.t.Fatalf("第%d回が始まっていない", seq)
	}
	return d.Sessions[seq-1].Session.ID
}

func (h *harness) setWeekly(name string, windows ...apitypes.WeeklyWindow) {
	h.t.Helper()
	if _, err := h.c.PutWeeklyAvailability(ctx, h.users[name], apitypes.PutWeeklyAvailabilityInput{Timezone: "Asia/Tokyo", Windows: &windows}); err != nil {
		h.t.Fatal(err)
	}
}

// 担当者確認までを通す：承認済みの計画 → 調整開始 → 日時の提案・同意 → 開催3日前の確認・催促・エスカレーション。
func TestSessionDateAndAssigneeConfirmationTimeline(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	h.setNow(jst0(2026, 9, 24))
	h.process()
	h.sess = h.startedSession(b.ID, 1)
	d := h.book("A", b.ID)
	assignee := h.memberName(*d.Sessions[0].AssigneeMemberID)

	// 開催回のデータに担当が固定される。
	var data struct {
		Assignee string   `json:"assignee_member_id"`
		Target   []string `json:"target_section_ids"`
	}
	if err := json.Unmarshal(h.detail("A").Data, &data); err != nil || data.Assignee != *d.Sessions[0].AssigneeMemberID || len(data.Target) != 2 {
		t.Fatalf("開催回データ = %s, %v", h.detail("A").Data, err)
	}
	// 各メンバーは「今回だけ参加不可」を含む参加条件を答える。
	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustPrep(name, "attending", weeklyAll())
	}
	h.process()
	if h.detail("A").CurrentProposal == nil {
		t.Fatalf("案が作られていない: %+v", h.detail("A").ActiveCase)
	}
	// 発表の担当は、本人が承認した担当者に限られる。
	var plan struct {
		StartsAt string `json:"starts_at"`
		Agenda   []struct {
			Presenter *string `json:"presenter_member_id"`
		} `json:"agenda"`
	}
	_ = json.Unmarshal(h.detail("A").CurrentProposal.Data, &plan)
	for _, item := range plan.Agenda {
		if item.Presenter != nil && *item.Presenter != *d.Sessions[0].AssigneeMemberID {
			t.Fatalf("担当以外が発表者になっている: %+v", plan)
		}
	}
	h.mustRespond(assignee, "assignment", "accept")
	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustRespond(name, "approval", "approve")
	}
	got := h.book("A", b.ID).Sessions[0]
	if got.SchedulingStatus != "scheduled" || got.Session.ScheduleStatus != "confirmed" {
		t.Fatalf("日時が確定していない: %+v", got)
	}
	starts := got.Session.StartsAt

	// 開催3日前になる前は、確認を作らない。
	h.setNow(starts.Add(-72*time.Hour - time.Minute))
	h.process()
	if h.kinds()["book_assignee_confirm"] != 0 || h.book("A", b.ID).Sessions[0].AssigneeConfirmationStatus != nil {
		t.Fatalf("3日前より前に確認を作った: %v", h.kinds())
	}
	// 3日前：担当者へ確認を一度だけ作る（再処理・再起動でも重複しない）。
	h.setNow(starts.Add(-72 * time.Hour))
	h.process()
	h.process()
	h.restart()
	h.process()
	if h.kinds()["book_assignee_confirm"] != 1 {
		t.Fatalf("最終確認の通知 = %v", h.kinds())
	}
	slot := h.book(assignee, b.ID).Sessions[0]
	if slot.AssigneeConfirmationStatus == nil || *slot.AssigneeConfirmationStatus != "open" || !slot.CanConfirmAssignment {
		t.Fatalf("担当者の確認 = %+v", slot)
	}
	if other := h.book("A", b.ID).Sessions[0]; h.memberName(*other.AssigneeMemberID) != "A" && other.CanConfirmAssignment {
		t.Fatal("担当者以外に確認の操作権限がある")
	}
	// 担当者本人以外は回答できない。
	for _, name := range []string{"A", "B", "C", "D"} {
		if name == assignee {
			continue
		}
		if _, err := h.c.RespondAssigneeConfirmation(ctx, h.users[name], h.group, b.ID, slot.SlotID, apitypes.AssigneeConfirmationInput{Decision: "confirm"}, nil); code(err) != apperr.Forbidden {
			t.Fatalf("%s による代理の確認: %v", name, err)
		}
	}
	// 2日前：未回答なら一度だけ催促する。
	h.setNow(starts.Add(-48 * time.Hour))
	h.process()
	h.process()
	h.restart()
	h.process()
	if h.kinds()["book_assignee_reminder"] != 1 || h.kinds()["book_assignee_escalation"] != 0 {
		t.Fatalf("催促 = %v", h.kinds())
	}
	// 1日前でも未回答なら管理者判断待ちとして管理者へ一度だけ通知する。自動では担当を交代しない。
	h.setNow(starts.Add(-24 * time.Hour))
	h.process()
	h.process()
	if h.kinds()["book_assignee_escalation"] != 1 {
		t.Fatalf("エスカレーション = %v", h.kinds())
	}
	slot = h.book(assignee, b.ID).Sessions[0]
	if *slot.AssigneeConfirmationStatus != "needs_owner" || *slot.AssigneeMemberID != *d.Sessions[0].AssigneeMemberID || slot.AssignmentStatus != "accepted" {
		t.Fatalf("エスカレーション後 = %+v", slot)
	}
	// 管理者判断待ちでも、担当者本人はなお確認できる。
	if _, err := h.c.RespondAssigneeConfirmation(ctx, h.users[assignee], h.group, b.ID, slot.SlotID, apitypes.AssigneeConfirmationInput{Decision: "confirm"}, nil); err != nil {
		t.Fatal(err)
	}
	if s := h.book("A", b.ID).Sessions[0]; *s.AssigneeConfirmationStatus != "confirmed" {
		t.Fatalf("確認後 = %+v", s)
	}
	if _, err := h.c.RespondAssigneeConfirmation(ctx, h.users[assignee], h.group, b.ID, slot.SlotID, apitypes.AssigneeConfirmationInput{Decision: "confirm"}, nil); code(err) != apperr.InvalidState {
		t.Fatalf("回答済みへの再回答: %v", err)
	}
}

func TestAssigneeChangeAtFinalCheckNeedsCandidateApproval(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	h.setNow(jst0(2026, 9, 24))
	h.process()
	h.sess = h.startedSession(b.ID, 1)
	oldAssigneeID := *h.book("A", b.ID).Sessions[0].AssigneeMemberID
	assignee := h.memberName(oldAssigneeID)
	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustPrep(name, "attending", weeklyAll())
	}
	h.process()
	h.mustRespond(assignee, "assignment", "accept")
	for _, name := range []string{"A", "B", "C", "D"} {
		h.mustRespond(name, "approval", "approve")
	}
	starts := h.book("A", b.ID).Sessions[0].Session.StartsAt
	h.setNow(starts.Add(-72 * time.Hour))
	h.process()
	slotID := h.book("A", b.ID).Sessions[0].SlotID

	if _, err := h.c.RespondAssigneeConfirmation(ctx, h.users[assignee], h.group, b.ID, slotID, apitypes.AssigneeConfirmationInput{Decision: "request_change"}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		h.process()
	}
	s := h.book("A", b.ID).Sessions[0]
	if s.AssignmentStatus != "change_proposed" || s.ProposedAssigneeMemberID == nil {
		t.Fatalf("候補が提案されていない: %+v", s)
	}
	candidate := h.memberName(*s.ProposedAssigneeMemberID)
	if candidate == assignee {
		t.Fatal("本人が候補になっている")
	}
	// AIは無承認で担当者を交代させない。候補本人が承認するまで、担当も開催回のデータも変わらない。
	for i := 0; i < 3; i++ {
		h.process()
	}
	if s2 := h.book("A", b.ID).Sessions[0]; *s2.AssigneeMemberID != oldAssigneeID || s2.AssignmentStatus != "change_proposed" {
		t.Fatalf("承認なしに担当が変わった: %+v", s2)
	}
	if !strings.Contains(string(h.detail("A").Data), oldAssigneeID) {
		t.Fatal("承認前に開催回データの担当が変わった")
	}
	// 候補以外は承認できない。元の担当者も、管理者も代理で承認できない。
	for _, name := range []string{"A", "B", "C", "D"} {
		if name == candidate {
			continue
		}
		if _, err := h.assign(name, b.ID, slotID, "accept"); code(err) != apperr.Forbidden && code(err) != apperr.InvalidState {
			t.Fatalf("%s による候補の承認: %v", name, err)
		}
	}
	if s2 := h.book("A", b.ID).Sessions[0]; *s2.AssigneeMemberID != oldAssigneeID {
		t.Fatalf("代理の承認で担当が変わった: %+v", s2)
	}
	// 候補が辞退すると別の候補を探す（辞退した人は再び候補にならない）。
	h.mustAssign(candidate, b.ID, slotID, "request_change")
	h.process()
	s = h.book("A", b.ID).Sessions[0]
	if s.AssignmentStatus != "change_proposed" || h.memberName(*s.ProposedAssigneeMemberID) == candidate || h.memberName(*s.ProposedAssigneeMemberID) == assignee {
		t.Fatalf("別の候補が提案されていない: %+v", s)
	}
	candidate = h.memberName(*s.ProposedAssigneeMemberID)
	beforeDMs := len(h.groupNotifs())
	// 候補本人が承認すると担当が変わり、担当だけの再調整が始まる（他の人の承認の取り直しは要らない）。
	h.mustAssign(candidate, b.ID, slotID, "accept")
	s = h.book("A", b.ID).Sessions[0]
	if h.memberName(*s.AssigneeMemberID) != candidate || s.AssignmentStatus != "accepted" || s.ProposedAssigneeMemberID != nil {
		t.Fatalf("承認後の担当 = %+v", s)
	}
	if got := h.book("A", b.ID); got.Book.PlanStatus != "approved" {
		t.Fatalf("担当変更で計画の承認が失われた: %s", got.Book.PlanStatus)
	}
	if !strings.Contains(string(h.detail("A").Data), *s.AssigneeMemberID) {
		t.Fatalf("開催回データの担当が更新されていない: %s", h.detail("A").Data)
	}
	if len(h.groupNotifs()) <= beforeDMs {
		t.Fatal("担当変更の通知がない")
	}
	// 新しい担当者は候補として本人が承認済みなので、直前確認は済みとして記録され、旧担当の確認は無効になる。
	if s.AssigneeConfirmationStatus == nil || *s.AssigneeConfirmationStatus != "confirmed" {
		t.Fatalf("新担当の確認 = %+v", s)
	}
	// 担当の変更は同じ操作を繰り返しても二重に効かない。
	if _, err := h.assign(candidate, b.ID, slotID, "accept"); code(err) != apperr.InvalidState && code(err) != apperr.Forbidden {
		t.Fatalf("再承認: %v", err)
	}
}

func TestInitialChangeRequestProposesCandidateWithoutRevokingOthersApprovals(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.process()
	d := h.book("A", b.ID)
	slotOf := map[string]string{}
	for _, s := range d.Sessions {
		slotOf[h.memberName(*s.AssigneeMemberID)] = s.SlotID
	}
	h.mustAssign("A", b.ID, "", "accept")
	h.mustAssign("C", b.ID, "", "accept")
	h.mustAssign("B", b.ID, "", "request_change")
	if got := h.book("A", b.ID).Book.PlanStatus; got != "awaiting_approval" {
		t.Fatalf("変更希望のある計画が成立した: %s", got)
	}
	h.process()
	s := h.book("A", b.ID)
	var target apitypes.ReadingBookSlot
	for _, x := range s.Sessions {
		if x.SlotID == slotOf["B"] {
			target = x
		}
	}
	if target.AssignmentStatus != "change_proposed" || h.memberName(*target.AssigneeMemberID) != "B" {
		t.Fatalf("候補の提案 = %+v", target)
	}
	candidate := h.memberName(*target.ProposedAssigneeMemberID)
	if candidate == "B" {
		t.Fatal("変更を希望した本人が候補")
	}
	// 他の担当者の承認は保たれ、候補が承認すれば全員の再承認なしで成立する。
	for _, x := range s.Sessions {
		if x.SlotID != slotOf["B"] && x.AssignmentStatus != "accepted" && h.memberName(*x.AssigneeMemberID) != "D" {
			t.Fatalf("他の担当者の承認が失われた: %+v", x)
		}
	}
	h.mustAssign(candidate, b.ID, slotOf["B"], "accept")
	got := h.book("A", b.ID)
	// 候補が別の枠も担当している場合は、その本人の承認が残っていればよい。未承認の枠が残っていれば成立しない。
	pending := 0
	for _, x := range got.Sessions {
		if x.AssignmentStatus != "accepted" {
			pending++
		}
	}
	if (pending == 0) != (got.Book.PlanStatus == "approved") {
		t.Fatalf("承認状況と計画状態が食い違う: pending=%d status=%s", pending, got.Book.PlanStatus)
	}
}

func TestDepartedAssigneeNeedsReplacementCandidate(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.process()
	d := h.book("A", b.ID)
	bID := h.groupMemberID("B")
	var bSlot string
	for _, s := range d.Sessions {
		if *s.AssigneeMemberID == bID {
			bSlot = s.SlotID
		}
	}
	if _, err := h.c.LeaveGroup(ctx, h.users["B"], h.group, apitypes.LeaveGroupInput{}, nil); err != nil {
		t.Fatal(err)
	}
	h.process()
	s := h.book("A", b.ID)
	for _, x := range s.Sessions {
		if x.SlotID != bSlot {
			continue
		}
		if x.AssignmentStatus != "change_proposed" || x.ProposedAssigneeMemberID == nil || *x.ProposedAssigneeMemberID == bID {
			t.Fatalf("脱退者の担当の候補 = %+v", x)
		}
	}
}

func TestBookAvailabilityRequestsAreDeduplicated(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.setWeekly("B", apitypes.WeeklyWindow{Weekday: 3, Start: "19:00", End: "22:00"})
	if _, err := h.c.RequestAvailabilityUpdate(ctx, h.users["B"], h.group, b.ID, nil); code(err) != apperr.Forbidden {
		t.Fatalf("一般メンバーの依頼: %v", err)
	}
	res, err := h.c.RequestAvailabilityUpdate(ctx, h.users["A"], h.group, b.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out apitypes.AvailabilityRequestResult
	decode(t, res.Body, &out)
	if out.RequestedCount != 3 || out.SkippedCount != 1 {
		t.Fatalf("依頼結果 = %+v（最近登録した B は対象外）", out)
	}
	// 同じ日の再依頼は二重にDMを作らない。
	if _, err := h.c.RequestAvailabilityUpdate(ctx, h.users["A"], h.group, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	if h.kinds()["availability_requested"] != 4 {
		t.Fatalf("通知 = %v", h.kinds())
	}
	// 30日を過ぎた登録は再確認を依頼する。
	h.setNow(h.clk.Now().Add(31 * 24 * time.Hour))
	if _, err := h.c.RequestAvailabilityUpdate(ctx, h.users["A"], h.group, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	if h.kinds()["availability_requested"] != 8 {
		t.Fatalf("再確認の通知 = %v", h.kinds())
	}
}

// 週間空き時間（水曜 20:00〜21:00 だけ）を使い、複数ブックの日程が衝突しない。
func TestSchedulingUsesWeeklyAvailabilityAndAvoidsConflictsAcrossBooks(t *testing.T) {
	h := newHarness(t, nil)
	for _, name := range []string{"A", "B", "C", "D"} {
		h.setWeekly(name, apitypes.WeeklyWindow{Weekday: 3, Start: "20:00", End: "21:00"})
	}
	b1 := h.createBook(bookInput("最初の本", 6, 3))
	b2 := h.createBook(bookInput("次の本", 6, 3))
	h.process()
	// 両方の計画を承認する。
	for _, id := range []string{b1.ID, b2.ID} {
		seen := map[string]bool{}
		for _, s := range h.book("A", id).Sessions {
			name := h.memberName(*s.AssigneeMemberID)
			if !seen[name] {
				seen[name] = true
				h.mustAssign(name, id, "", "accept")
			}
		}
	}
	h.setNow(jst0(2026, 9, 24))
	h.process()
	if h.sessionCount() != 2 {
		t.Fatalf("両ブックの第1回が始まっていない: %d", h.sessionCount())
	}
	sess1, sess2 := h.startedSession(b1.ID, 1), h.startedSession(b2.ID, 1)
	// 時間帯は答えず、今回だけの参加不可（なし）を答える。普段の空き時間が使われる。
	answer := func(session string) {
		h.sess = session
		for _, name := range []string{"A", "B", "C", "D"} {
			h.mustPrep(name, "attending", json.RawMessage(`{"declined_presentation":false,"unavailable_dates":[]}`))
		}
	}
	answer(sess1)
	answer(sess2)
	h.process()
	approve := func(session string) {
		h.sess = session
		d := h.detail("A")
		if d.CurrentProposal == nil {
			t.Fatalf("案がない: %+v", d.ActiveCase)
		}
		var plan struct {
			StartsAt string `json:"starts_at"`
		}
		_ = json.Unmarshal(d.CurrentProposal.Data, &plan)
		at, _ := time.Parse(time.RFC3339, plan.StartsAt)
		if wd := at.In(time.FixedZone("JST", 9*3600)); wd.Weekday() != time.Wednesday || wd.Hour() != 20 || wd.Minute() != 0 {
			t.Fatalf("普段の空き時間（水曜20:00）以外が提案された: %s", plan.StartsAt)
		}
		for _, a := range d.CurrentProposal.Assignments {
			h.mustRespond(h.memberName(a.MemberID), "assignment", "accept")
		}
		for _, name := range []string{"A", "B", "C", "D"} {
			h.mustRespond(name, "approval", "approve")
		}
	}
	// 先に第1ブックの日時を確定する。
	approve(sess1)
	first := h.book("A", b1.ID).Sessions[0].Session
	if first.ScheduleStatus != "confirmed" {
		t.Fatalf("第1ブックの日時が確定していない: %+v", first)
	}
	// 第2ブックは同じ日時になる案しかない。同じ人が同時に2つへ出られないので確定せず、管理者へ戻す。
	h.sess = sess2
	d := h.detail("A")
	if d.CurrentProposal != nil {
		for _, name := range []string{"A", "B", "C", "D"} {
			if tk := h.openTask(name, "assignment"); tk != nil {
				_, _ = h.respond(name, "assignment", "accept")
			}
			if tk := h.openTask(name, "approval"); tk != nil {
				_, _ = h.respond(name, "approval", "approve")
			}
		}
	}
	h.process()
	second := h.book("A", b2.ID).Sessions[0]
	if second.Session.ScheduleStatus == "confirmed" && second.Session.StartsAt.Equal(first.StartsAt) {
		t.Fatalf("別ブックと同じ日時に確定した（衝突）: %+v", second.Session)
	}
	if second.SchedulingStatus != "needs_attention" {
		t.Fatalf("衝突する日時しかないときは管理者判断待ちのはず: %+v", second)
	}
}

func TestSchedulingDoesNotUseUnansweredMembersWeeklyAvailability(t *testing.T) {
	h := newHarness(t, nil)
	for _, name := range []string{"A", "B", "C"} {
		h.setWeekly(name, apitypes.WeeklyWindow{Weekday: 3, Start: "20:00", End: "21:00"})
	}
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID)
	h.setNow(jst0(2026, 9, 24))
	h.process()
	h.sess = h.startedSession(b.ID, 1)
	for _, name := range []string{"A", "B", "C"} {
		h.mustPrep(name, "attending", json.RawMessage(`{"declined_presentation":false,"unavailable_dates":[]}`))
	}
	// D は未回答（普段の空き時間も未登録）：案は作らず、D の回答を待つ。
	h.process()
	if d := h.detail("A"); d.CurrentProposal != nil {
		t.Fatalf("未回答の人がいるのに案が作られた: %+v", d.CurrentProposal)
	}
	// D が参加できると答えても、時間帯が未登録なら日時を提案しない。
	h.mustPrep("D", "attending", json.RawMessage(`{"declined_presentation":false,"unavailable_dates":[]}`))
	h.process()
	if d := h.detail("A"); d.CurrentProposal != nil {
		t.Fatalf("時間帯が分からない人がいるのに案が作られた")
	}
	// D が普段の空き時間を登録すると、時間帯を答えなくても水曜20:00の案が作られる。
	h.setWeekly("D", apitypes.WeeklyWindow{Weekday: 3, Start: "20:00", End: "21:00"})
	h.mustPrep("D", "attending", json.RawMessage(`{"declined_presentation":false,"unavailable_dates":[]}`))
	h.process()
	d := h.detail("A")
	if d.CurrentProposal == nil || !strings.Contains(string(d.CurrentProposal.Data), "2026-10-07T20:00:00+09:00") {
		t.Fatalf("普段の空き時間から案が作られていない: %+v", d.CurrentProposal)
	}
	// 「今回だけ参加不可」にした日は、普段の空き時間があっても候補から外れる（この期間に他の候補はない）。
	h.mustPrep("D", "attending", json.RawMessage(`{"declined_presentation":false,"unavailable_dates":["2026-10-07"]}`))
	h.process()
	d = h.detail("A")
	if (d.CurrentProposal != nil && d.CurrentProposal.Status == "pending") || d.ActiveCase == nil || d.ActiveCase.Status != "needs_owner" {
		t.Fatalf("参加不可の日に案が作られた/管理者へ戻っていない: %+v %+v", d.CurrentProposal, d.ActiveCase)
	}
	_ = store.BookPlanLegacy
}

func TestBookEditIsRejectedAfterFirstSessionStarts(t *testing.T) {
	h := newHarness(t, nil)
	b := h.createBook(bookInput("設計の本", 6, 3))
	edit := func(in apitypes.UpdateReadingBookInput) (store.Response, error) {
		return h.c.UpdateReadingBook(ctx, h.users["A"], h.group, b.ID, in, nil)
	}
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	h.approveBook(b.ID)
	h.advanceToAdjustmentStart(b.ID, 1)
	h.process()
	if h.sessionCount() != 1 {
		t.Fatalf("最初のセッションが始まっていない: %d", h.sessionCount())
	}
	for name, in := range map[string]apitypes.UpdateReadingBookInput{
		"書名":   {Title: str("変更")},
		"章":    {Sections: sectionsJSON(6)},
		"期間":   {PeriodEnd: str("2026-12-31")},
		"リード":  {AdjustmentLeadDays: num(14)},
		"ISBN": {ISBN: str("9784297127831")},
	} {
		if _, err := edit(in); code(err) != apperr.InvalidState {
			t.Fatalf("開始後の%sの編集は 409 相当のはず: %v", name, err)
		}
	}
	if _, err := h.c.RegenerateBookPlan(ctx, h.users["A"], h.group, b.ID, apitypes.RegenerateBookPlanInput{}, nil); code(err) != apperr.InvalidState {
		t.Fatalf("開始後の再生成: %v", err)
	}
	if d := h.book("A", b.ID); d.Permissions.CanEdit {
		t.Fatal("開始後なのに can_edit")
	}
}

// メンバーがまだログインしていないと、実セッションを作れない。自動で進めず、管理者へ一度だけ知らせ、
// 解消されたら次の処理で開始する。
func TestSlotStartBlockedByUnjoinedMemberNotifiesOwnerOnceAndRecovers(t *testing.T) {
	h := newHarness(t, nil)
	res, err := h.c.CreateGroup(ctx, h.users["A"], apitypes.CreateGroupInput{Name: "二人組", Invitees: []apitypes.Invitee{{DiscordUserID: "555555555555555555", DisplayName: "未参加"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var g apitypes.Group
	decode(t, res.Body, &g)
	h.group = g.ID
	b := h.createBook(bookInput("設計の本", 6, 3))
	h.approveBook(b.ID) // 担当を割り当てられるのは、ログイン済みの管理者だけ
	h.setNow(jst0(2026, 9, 24))
	for i := 0; i < 3; i++ {
		h.process()
	}
	d := h.book("A", b.ID)
	if h.sessionCount() != 0 || d.Sessions[0].SchedulingStatus != "needs_attention" || d.Sessions[0].Status != "planned" {
		t.Fatalf("開始できない枠 = %+v sessions=%d", d.Sessions[0], h.sessionCount())
	}
	if h.kinds()["book_attention"] != 1 {
		t.Fatalf("管理者への通知 = %v", h.kinds())
	}
	// 未参加のメンバーがログインすると、次の処理で開始される（通知は増えない）。
	if err := h.st.Tx(ctx, func(tx *store.Tx) error {
		u, err := tx.UpsertUser(ctx, "555555555555555555", "参加した人", t0)
		if err != nil {
			return err
		}
		return tx.ActivateMemberships(ctx, u)
	}); err != nil {
		t.Fatal(err)
	}
	h.process()
	d = h.book("A", b.ID)
	if h.sessionCount() != 1 || d.Sessions[0].Status != "active" || d.Sessions[0].SchedulingStatus != "scheduling" {
		t.Fatalf("解消後 = %+v sessions=%d", d.Sessions[0], h.sessionCount())
	}
	if h.kinds()["book_attention"] != 1 {
		t.Fatalf("通知が重複した: %v", h.kinds())
	}
}

// 担当変更の候補を作れないときは、無承認で交代させず、管理者判断待ちにする。
func TestReplacementFailureNeverSwapsAssigneeAndNotifiesOwner(t *testing.T) {
	agent := &fixedBookAgent{}
	h := newHarnessFull(t, nil, coord.DraftOnlyInterpreter{}, agent)
	b := h.createBook(bookInput("設計の本", 6, 3))
	agent.plan = coord.BookPlan{Summary: "計画", Slots: []coord.BookPlanSlot{
		{Sequence: 1, PeriodStart: "2026-10-01", PeriodEnd: "2026-10-07", SectionIDs: []string{"sec_1", "sec_2"}, AssigneeMemberID: h.groupMemberID("A")},
		{Sequence: 2, PeriodStart: "2026-10-08", PeriodEnd: "2026-10-14", SectionIDs: []string{"sec_3", "sec_4"}, AssigneeMemberID: h.groupMemberID("B")},
		{Sequence: 3, PeriodStart: "2026-10-15", PeriodEnd: "2026-10-21", SectionIDs: []string{"sec_5", "sec_6"}, AssigneeMemberID: h.groupMemberID("C")},
	}}
	h.process()
	slot := h.book("A", b.ID).Sessions[1].SlotID
	h.mustAssign("B", b.ID, "", "request_change")
	agent.replaceErr = coord.ErrTransient
	for i := 0; i < 8; i++ {
		h.process()
		h.clk.Advance(20 * time.Minute)
	}
	s := h.book("A", b.ID).Sessions[1]
	if s.AssignmentStatus != "needs_attention" || h.memberName(*s.AssigneeMemberID) != "B" || s.ProposedAssigneeMemberID != nil || s.SchedulingStatus != "needs_attention" {
		t.Fatalf("失敗後の枠 = %+v", s)
	}
	if h.kinds()["book_attention"] != 1 {
		t.Fatalf("管理者への通知 = %v", h.kinds())
	}
	if got := h.book("A", b.ID).Book.PlanStatus; got != "awaiting_approval" {
		t.Fatalf("候補のない枠があるのに成立した: %s", got)
	}
	_ = slot
}
