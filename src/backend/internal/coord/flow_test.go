package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

func code(err error) string {
	var e *apperr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestSessionCreationStartsCollecting(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	d := h.detail("B")
	if d.Session.Status != "draft" || d.Session.Revision != 1 || d.ActiveCase.Status != "collecting" {
		t.Fatalf("初期状態が違う: %+v %+v", d.Session, d.ActiveCase)
	}
	if len(d.MyTasks) != 1 || d.MyTasks[0].Kind != "preparation" || d.MyTasks[0].AllowedDecisions[0] != "submit" {
		t.Fatalf("B の preparation タスクがない: %+v", d.MyTasks)
	}
	for _, p := range d.Preparations {
		if p.Value != nil {
			t.Fatal("初期の参加条件は未回答（null）のはず")
		}
	}
	if d.NotificationSummary.PendingCount != 4 {
		t.Fatalf("全員への依頼通知が登録されていない: %+v", d.NotificationSummary)
	}
	if d.Permissions.CanSubmitProposal || d.Permissions.CanViewActivity || !d.Permissions.CanWithdrawAttendance {
		t.Fatalf("メンバーの権限表示が違う: %+v", d.Permissions)
	}
}

// 初回の未回答者を欠席扱いせず、全員の回答が揃うまで案を作らない。
func TestNoProposalUntilAllPrepared(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.mustPrep("A", "attending", prepData(true))
	h.mustPrep("B", "attending", prepData(false))
	h.process()
	if d := h.detail("A"); d.CurrentProposal != nil || d.ActiveCase.Status != "collecting" {
		t.Fatalf("回答が揃う前に案が作られた: %+v", d.ActiveCase)
	}
}

func TestInitialPlanNeedsAcceptanceAndOwnerApproval(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()

	d := h.detail("A")
	p := d.CurrentProposal
	if p == nil || p.Status != "pending" || p.ChangeKind != "initial" || p.Author != "agent" {
		t.Fatalf("初回案がない: %+v", p)
	}
	if len(p.Assignments) != 1 || p.Assignments[0].MemberID != h.memberID("B") || len(p.Approvals) != 1 || p.Approvals[0].Kind != "owner" {
		t.Fatalf("初回案の条件が違う: %+v", p)
	}
	if h.openTask("A", "owner_approval") == nil || h.openTask("B", "assignment") == nil {
		t.Fatal("担当者の assignment と管理者の owner_approval が必要")
	}
	// 管理者の承認だけでは本人の引き受けを代行しない。
	h.mustRespond("A", "owner_approval", "approve")
	if d := h.detail("A"); d.Session.Status == "confirmed" {
		t.Fatal("本人の引き受けなしに確定した")
	}
	h.mustRespond("B", "assignment", "accept")
	d = h.detail("C")
	if d.Session.Status != "confirmed" || d.ConfirmedPlan == nil || d.ConfirmedPlan.ID != p.ID || d.ActiveCase.Status != "confirmed" {
		t.Fatalf("確定していない: %+v", d.Session)
	}
	found := false
	for _, n := range h.notifications() {
		found = found || n.Kind == "plan_confirmed"
	}
	if !found {
		t.Fatal("確定の通知待ちが登録されていない")
	}
}

// E01・E02：B が担当を辞退すると、辞退していない C に割り振り直す。担当だけの変更なので、
// C の引き受けだけで確定する（範囲や時間配分が変わる場合の過半数は reading のテストで確かめる）。
func TestWithdrawalReassignsToAvailableMember(t *testing.T) {
	h := newHarness(t, nil)
	h.confirmInitial()
	// C はあとから「担当もできる」に変えた（確定した計画はそのまま有効なので、再計画しない）
	before := h.detail("A")
	h.mustPrep("C", "attending", prepData(false))
	if d := h.detail("A"); d.ActiveCase.ID != before.ActiveCase.ID || d.Session.Status != "confirmed" {
		t.Fatalf("計画が有効なら再計画しない: %+v", d.ActiveCase)
	}

	if _, err := h.withdraw("B", "assignment"); err != nil {
		t.Fatal(err)
	}
	d := h.detail("A")
	if d.Session.Status != "needs_attention" || d.ConfirmedPlan == nil || d.ActiveCase.ID == before.ActiveCase.ID {
		t.Fatalf("辞退後は要調整・新しい案件になるはず: %+v %+v", d.Session, d.ActiveCase)
	}
	h.process()
	d = h.detail("C")
	p := d.CurrentProposal
	if p == nil || p.ChangeKind != "replan" || p.Status != "pending" {
		t.Fatalf("再計画の案がない: %+v", d.ActiveCase)
	}
	if len(p.Assignments) != 1 || p.Assignments[0].MemberID != h.memberID("C") || len(p.Approvals) != 0 {
		t.Fatalf("C の引き受けだけが必要: %+v %+v", p.Assignments, p.Approvals)
	}
	// 準備状況の確認依頼は出さない
	if h.openTask("C", "preparation") != nil || h.openTask("B", "preparation") != nil {
		t.Fatal("確認依頼を出した")
	}
	h.mustRespond("C", "assignment", "accept")
	d = h.detail("A")
	if d.Session.Status != "confirmed" || d.ConfirmedPlan.ID != p.ID {
		t.Fatalf("再計画が確定していない: %+v", d.Session)
	}
}

// E03：担当を割り振れる人がいなければ、確認依頼を出さずに管理者判断待ちにする。管理者は代案を出せる。
func TestNoFeasiblePlanGoesToOwnerAndOwnerProposal(t *testing.T) {
	h := newHarness(t, nil)
	h.confirmInitial()
	// B 以外は担当を辞退している。B も辞退すると割り振れる人がいない
	if _, err := h.withdraw("B", "assignment"); err != nil {
		t.Fatal(err)
	}
	h.process()
	d := h.detail("A")
	if d.ActiveCase.Status != "needs_owner" || d.ActiveCase.ReasonCode == nil || *d.ActiveCase.ReasonCode != "no_feasible_plan" {
		t.Fatalf("管理者判断待ちになっていない: %+v", d.ActiveCase)
	}
	if h.openTask("C", "preparation") != nil {
		t.Fatal("確認依頼を出した")
	}
	if !d.Permissions.CanSubmitProposal || h.detail("B").Permissions.CanSubmitProposal {
		t.Fatal("代案を出せるのは判断待ちの管理者だけ")
	}

	// 管理者の代案：復習回にする。範囲の変更なので過半数の承認が必要。
	plan := `{"covered_section_ids":[],"deferred_section_ids":["sec_2","sec_3"],"agenda":[{"id":"r","activity":"review","section_ids":["sec_1"],"presenter_member_id":null,"minutes":60}]}`
	bad := `{"covered_section_ids":[],"deferred_section_ids":["sec_2","sec_3"],"agenda":[{"id":"r","activity":"review","section_ids":["sec_1"],"presenter_member_id":null,"minutes":61}]}`
	rev := d.Session.Revision
	if _, err := h.c.SubmitProposal(ctx, h.users["B"], h.sess, apitypes.SubmitProposalInput{ExpectedRevision: &rev, Data: json.RawMessage(plan)}, nil); code(err) != apperr.Forbidden {
		t.Fatalf("管理者以外の代案は 403: %v", err)
	}
	if _, err := h.c.SubmitProposal(ctx, h.users["A"], h.sess, apitypes.SubmitProposalInput{ExpectedRevision: &rev, Data: json.RawMessage(bad)}, nil); code(err) != apperr.ValidationFailed {
		t.Fatalf("持ち時間超過の代案は 422: %v", err)
	}
	if _, err := h.c.SubmitProposal(ctx, h.users["A"], h.sess, apitypes.SubmitProposalInput{ExpectedRevision: &rev, Data: json.RawMessage(plan)}, nil); err != nil {
		t.Fatal(err)
	}
	d = h.detail("A")
	if d.CurrentProposal.Author != "owner" || d.ActiveCase.Status != "awaiting_consent" || d.CurrentProposal.Approvals[0].Kind != "majority" {
		t.Fatalf("代案の状態が違う: %+v", d.CurrentProposal)
	}
	// 管理者が案を提出したことを承認として数えない。
	if len(d.CurrentProposal.Approvals[0].ApprovedMemberIDs) != 0 {
		t.Fatal("提出者の承認が自動で記録された")
	}
}

// E06：版1の投票中に参加条件が変わると、旧版への投票は拒否され、旧版では確定しない。
func TestStaleVoteIsRejected(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	old := h.openTask("B", "assignment")
	oldOwner := h.openTask("A", "owner_approval")

	// D が参加条件を変えると、現在の案は旧版になる。
	h.mustPrep("D", "absent", prepData(true))
	if _, err := h.c.RespondTask(ctx, h.users["B"], old.ID, apitypes.TaskResponseInput{Decision: "accept", ProposalID: old.ProposalID, ProposalVersion: old.ProposalVersion}, nil); code(err) != apperr.ProposalSuperseded {
		t.Fatalf("旧版への回答は 409 proposal_superseded: %v", err)
	}
	if _, err := h.c.RespondTask(ctx, h.users["A"], oldOwner.ID, apitypes.TaskResponseInput{Decision: "approve", ProposalID: oldOwner.ProposalID, ProposalVersion: oldOwner.ProposalVersion}, nil); code(err) != apperr.ProposalSuperseded {
		t.Fatalf("旧版への承認は拒否: %v", err)
	}
	h.process()
	d := h.detail("A")
	if d.CurrentProposal.Version != 2 || d.Session.Status == "confirmed" {
		t.Fatalf("新しい版で取り直すはず: %+v", d.CurrentProposal)
	}
}

func TestRevisionConflictAndPermissions(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	stale := int64(99)
	_, err := h.c.PutPreparation(ctx, h.users["B"], h.sess, apitypes.PutPreparationInput{ExpectedRevision: &stale,
		Preparation: &apitypes.Preparation{Attendance: "attending", Data: prepData(true)}}, nil)
	var e *apperr.Error
	if !errors.As(err, &e) || e.Code != apperr.RevisionConflict || e.Details["current_revision"] != int64(1) {
		t.Fatalf("revision_conflict と現在の revision を返すはず: %v", err)
	}
	// E07：他人のタスクには同じグループ内でも 404。
	tk := h.openTask("B", "preparation")
	if _, err := h.c.RespondTask(ctx, h.users["C"], tk.ID, apitypes.TaskResponseInput{Decision: "submit"}, nil); code(err) != apperr.NotFound {
		t.Fatalf("他人のタスクは 404: %v", err)
	}
	// 管理者専用 API をメンバーが呼ぶと 403。
	if _, err := h.c.Activity(ctx, h.users["B"], h.sess); code(err) != apperr.Forbidden {
		t.Fatalf("メンバーの実行履歴は 403: %v", err)
	}
	// 担当がないのに担当辞退はできない。
	if _, err := h.withdraw("C", "assignment"); code(err) != apperr.InvalidState {
		t.Fatalf("担当がない辞退は invalid_state: %v", err)
	}
}

// E05：同じ辞退を別のキーで二重に送っても、二重の案件を作らない。
func TestDuplicateWithdrawalDoesNotCreateSecondCase(t *testing.T) {
	h := newHarness(t, nil)
	h.confirmInitial()
	res1, err := h.withdraw("B", "assignment")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := h.withdraw("B", "assignment")
	if err != nil {
		t.Fatal(err)
	}
	var a1, a2 apitypes.MutationAccepted
	decode(t, res1.Body, &a1)
	decode(t, res2.Body, &a2)
	if a1.CaseID != a2.CaseID || a1.Revision != a2.Revision {
		t.Fatalf("二重の案件・版が作られた: %+v %+v", a1, a2)
	}
}

// E04：期限の中間で1回だけ催促し、期限までに揃わなければ管理者へ戻す。未回答を賛成にしない。
func TestReminderOnceAndDeadlineToOwner(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	h.mustRespond("B", "assignment", "accept")

	h.clk.Advance(13 * time.Hour)
	h.process()
	h.clk.Advance(time.Hour)
	h.process()
	reminders := 0
	for _, n := range h.notifications() {
		if n.Kind == "reminder" {
			reminders++
		}
	}
	if reminders != 1 {
		t.Fatalf("未回答の A への催促は1回だけのはず: %d", reminders)
	}
	h.clk.Advance(11 * time.Hour)
	h.process()
	d := h.detail("A")
	if d.ActiveCase.Status != "needs_owner" || *d.ActiveCase.ReasonCode != "deadline_expired" || d.Session.Status == "confirmed" {
		t.Fatalf("期限後は管理者判断待ち: %+v", d.ActiveCase)
	}
	if tk := d.MyTasks[len(d.MyTasks)-1]; tk.Status != "expired" || len(tk.AllowedDecisions) != 0 {
		t.Fatalf("期限切れのタスク: %+v", tk)
	}
}

// scripted は決まった順に結果を返す Planner。
type scripted struct {
	errs  []error
	calls int
}

func (s *scripted) Plan(ctx context.Context, req coord.PlanRequest) (coord.Outcome, coord.Usage, error) {
	s.calls++
	usage := coord.Usage{LLMCalls: []coord.LLMCall{{Model: "test", Currency: "unknown"}}, ToolCalls: 1}
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return coord.Outcome{}, usage, err
	}
	o, u, err := coord.DraftOnlyPlanner{}.Plan(ctx, req)
	u.LLMCalls = usage.LLMCalls
	return o, u, err
}

// 一時的な障害は受理済みの入力を取り消さず、指数バックオフで再試行する。費用不明を0にしない。
func TestTransientFailureIsRetried(t *testing.T) {
	p := &scripted{errs: []error{coord.ErrTransient, coord.ErrTransient}}
	h := newHarness(t, p)
	h.createSession()
	h.allPrepared()
	h.process()
	d := h.detail("A")
	if d.ActiveCase.Status != "planning" || d.ActiveCase.NextRetryAt == nil || !d.ActiveCase.NextRetryAt.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("30秒後の再試行が予定されていない: %+v", d.ActiveCase)
	}
	h.clk.Advance(30 * time.Second)
	h.process()
	if d := h.detail("A"); d.ActiveCase.NextRetryAt == nil || !d.ActiveCase.NextRetryAt.Equal(t0.Add(90*time.Second)) {
		t.Fatalf("2回目は60秒後: %+v", d.ActiveCase)
	}
	h.clk.Advance(time.Minute)
	h.process()
	if d := h.detail("A"); d.CurrentProposal == nil || d.ActiveCase.Status != "awaiting_consent" {
		t.Fatalf("再試行後に案が作られていない: %+v", d.ActiveCase)
	}
	act, err := h.c.Activity(ctx, h.users["A"], h.sess)
	if err != nil {
		t.Fatal(err)
	}
	if act.Summary.LLMCallCount != 3 || len(act.Summary.Costs) != 1 || act.Summary.Costs[0].Currency != "unknown" || act.Summary.Costs[0].EstimatedAmount != nil {
		t.Fatalf("呼び出し回数・費用不明の集計が違う: %+v", act.Summary)
	}
}

func TestInvalidOutputAndBudgetStopSafely(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"不正な出力": {coord.ErrInvalidOutput, "model_error"},
		"予算超過":  {coord.ErrBudgetExceeded, "budget_exceeded"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, &scripted{errs: []error{tc.err}})
			h.createSession()
			h.allPrepared()
			h.process()
			d := h.detail("A")
			if d.ActiveCase.Status != "needs_owner" || *d.ActiveCase.ReasonCode != tc.reason || d.CurrentProposal != nil {
				t.Fatalf("未承認の案を作らずに停止するはず: %+v", d.ActiveCase)
			}
		})
	}
}

// 生成中に参加条件が変わった場合、古い状態に基づく案で上書きしない。
func TestStalePlannerResultIsDiscarded(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	var hook func()
	h.c = coord.NewCoordinator(mustRegistry(t), h.st, h.clk, plannerFunc(func(ctx context.Context, req coord.PlanRequest) (coord.Outcome, coord.Usage, error) {
		if hook != nil {
			hook()
			hook = nil
		}
		return coord.DraftOnlyPlanner{}.Plan(ctx, req)
	}), coord.Options{PublicBaseURL: "http://localhost"})
	// 計画中に D が欠席に変える（計画処理は Tx の外なので更新できる）。
	hook = func() { h.mustPrep("D", "absent", prepData(true)) }
	h.process()
	d := h.detail("A")
	if d.CurrentProposal == nil {
		t.Fatal("新しい入力に基づく案が作られていない")
	}
	if d.CurrentProposal.Version != 1 {
		t.Fatalf("古い結果が保存された: 版%d", d.CurrentProposal.Version)
	}
	for _, p := range d.Preparations {
		if p.MemberID == h.memberID("D") && p.Value.Attendance != "absent" {
			t.Fatal("D の更新が反映されていない")
		}
	}
}

type plannerFunc func(context.Context, coord.PlanRequest) (coord.Outcome, coord.Usage, error)

func (f plannerFunc) Plan(ctx context.Context, req coord.PlanRequest) (coord.Outcome, coord.Usage, error) {
	return f(ctx, req)
}

func TestIdempotentResponseReplay(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	h.allPrepared()
	h.process()
	tk := h.openTask("B", "assignment")
	key := &store.IdemKey{UserID: h.users["B"], Key: "k", Method: "POST", Path: "/api/tasks/" + tk.ID + "/responses", BodyHash: "h"}
	in := apitypes.TaskResponseInput{Decision: "accept", ProposalID: tk.ProposalID, ProposalVersion: tk.ProposalVersion}
	first, err := h.c.RespondTask(ctx, h.users["B"], tk.ID, in, key)
	if err != nil {
		t.Fatal(err)
	}
	// 成功後の再送は「回答済み」で失敗せず、元の成功を返す。
	again, err := h.c.RespondTask(ctx, h.users["B"], tk.ID, in, key)
	if err != nil || !again.Replayed || string(again.Body) != string(first.Body) {
		t.Fatalf("再送: %+v %v", again, err)
	}
	// 別のキーでの二重回答は 409。
	if _, err := h.c.RespondTask(ctx, h.users["B"], tk.ID, in, nil); code(err) != apperr.TaskClosed {
		t.Fatalf("二重回答は task_closed: %v", err)
	}
}

func mustRegistry(t *testing.T) *coord.Service {
	t.Helper()
	reg, err := coord.NewService(readingPlaybook())
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
