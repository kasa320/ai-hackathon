package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

// E03：確認した C も担当できず、他に成立する案もない → 同じ依頼を繰り返さず管理者判断待ち →
// 管理者が代案（復習回）を出し、過半数の同意で確定する。
func TestNoFeasiblePlanThenOwnerProposal(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID

	p["B"].withdraw(id, "assignment").mustStatus(t, http.StatusAccepted)
	s.process()
	// C は確認依頼に「担当できない」と回答する。
	p["C"].submitPreparation(id, "attending", prepData(false, []string{"sec_1", "sec_2"}, []string{}, 0)).mustStatus(t, http.StatusAccepted)
	s.process()

	d := p["A"].detail(id)
	if d.ActiveCase.Status != "needs_owner" || str(d.ActiveCase.ReasonCode) != "no_feasible_plan" || d.ActiveCase.Summary == "" {
		t.Fatalf("管理者判断待ちになっていない: %s", dump(d.ActiveCase))
	}
	if p["C"].openTask(id, "preparation") != nil {
		t.Fatal("同じ確認依頼を繰り返した")
	}
	if !d.Permissions.CanSubmitProposal || p["B"].detail(id).Permissions.CanSubmitProposal {
		t.Fatal("代案を出せるのは判断待ちの管理者だけ")
	}
	if !sentTo(s, persona["A"]) {
		t.Fatal("管理者への通知がない")
	}

	// メンバーは代案を出せない。持ち時間を超える代案は保存しない。
	review := map[string]any{"covered_section_ids": []string{}, "deferred_section_ids": []string{"sec_2", "sec_3"}, "agenda": []map[string]any{
		{"id": "r1", "activity": "review", "section_ids": []string{"sec_1"}, "presenter_member_id": nil, "minutes": 60},
	}}
	body := map[string]any{"expected_revision": d.Session.Revision, "data": review}
	if r := p["B"].send(http.MethodPost, "/api/sessions/"+id+"/proposals", body); r.status != 403 {
		t.Fatalf("メンバーの代案: %d", r.status)
	}
	bad := map[string]any{"expected_revision": d.Session.Revision, "data": map[string]any{"covered_section_ids": []string{}, "deferred_section_ids": []string{"sec_2", "sec_3"},
		"agenda": []map[string]any{{"id": "r1", "activity": "review", "section_ids": []string{"sec_1"}, "presenter_member_id": nil, "minutes": 90}}}}
	if r := p["A"].send(http.MethodPost, "/api/sessions/"+id+"/proposals", bad); r.status != 422 || !strings.Contains(string(r.body), `"path":"data.agenda"`) {
		t.Fatalf("持ち時間超過の代案: %d %s", r.status, r.body)
	}
	p["A"].send(http.MethodPost, "/api/sessions/"+id+"/proposals", body).mustStatus(t, http.StatusAccepted)
	d = p["A"].detail(id)
	if d.CurrentProposal.Author != "owner" || d.ActiveCase.Status != "awaiting_consent" || len(d.CurrentProposal.Approvals[0].ApprovedMemberIDs) != 0 {
		t.Fatalf("代案の提出を承認と数えない: %s", dump(d.CurrentProposal))
	}
	// 判断待ちでなくなったので再提出はできない。
	if r := p["A"].send(http.MethodPost, "/api/sessions/"+id+"/proposals", map[string]any{"expected_revision": d.Session.Revision, "data": review}); r.status != 409 || r.errorCode(t) != "invalid_state" {
		t.Fatalf("判断待ち以外の代案: %d", r.status)
	}
	for _, name := range []string{"A", "C", "D"} {
		p[name].respond(id, "approval", "approve").mustStatus(t, http.StatusAccepted)
	}
	if d := p["B"].detail(id); d.Session.Status != "confirmed" || d.ConfirmedPlan.Author != "owner" {
		t.Fatalf("過半数で確定するはず: %s", dump(d.Session))
	}
}

// E04：必要な票が未回答。期限の中間で1人1回だけ催促し、期限後に管理者へ戻す。未回答を賛成にしない。
func TestUnansweredVotesExpireToOwner(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID
	p["B"].withdraw(id, "assignment").mustStatus(t, http.StatusAccepted)
	s.process()
	p["C"].submitPreparation(id, "attending", prepData(true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)).mustStatus(t, http.StatusAccepted)
	s.process()
	p["C"].respond(id, "assignment", "accept").mustStatus(t, http.StatusAccepted)
	p["A"].respond(id, "approval", "approve").mustStatus(t, http.StatusAccepted)
	p["B"].respond(id, "approval", "approve").mustStatus(t, http.StatusAccepted)
	// C・D の投票は未回答のまま（2/4 で過半数3に届かない）。
	due := p["D"].openTask(id, "approval").DueAt
	if want := t0.Add(24 * time.Hour); !due.Equal(want) {
		t.Fatalf("期限は作成から24時間: %v", due)
	}
	before := len(s.sender.all())
	s.advance(12*time.Hour + time.Minute)
	s.advance(time.Hour)
	reminders := 0
	for _, m := range s.sender.all()[before:] {
		if strings.Contains(m.Content, "未回答の依頼があります") {
			reminders++
		}
	}
	if reminders != 2 {
		t.Fatalf("C・D に1回ずつ催促するはず: %d", reminders)
	}
	s.advance(11 * time.Hour)
	d := p["A"].detail(id)
	if d.ActiveCase.Status != "needs_owner" || str(d.ActiveCase.ReasonCode) != "deadline_expired" || d.Session.Status == "confirmed" {
		t.Fatalf("期限後は管理者判断待ち: %s", dump(d.ActiveCase))
	}
	if len(d.CurrentProposal.Approvals[0].ApprovedMemberIDs) != 2 {
		t.Fatal("未回答を賛成として数えてはいけない")
	}
	// 期限切れのタスクには回答できない。
	tk := p["D"].detail(id).MyTasks
	last := tk[len(tk)-1]
	if last.Status != "expired" || len(last.AllowedDecisions) != 0 {
		t.Fatalf("D のタスク: %s", dump(last))
	}
	r := p["D"].send(http.MethodPost, "/api/tasks/"+last.ID+"/responses", map[string]any{"decision": "approve", "proposal_id": last.ProposalID, "proposal_version": last.ProposalVersion})
	if r.status != 409 || r.errorCode(t) != "task_expired" {
		t.Fatalf("期限切れへの回答: %d %s", r.status, r.body)
	}
}

// 開催回登録時の参加条件が期限までに揃わない場合も、未回答者を欠席扱いせず管理者へ戻す。
// その後の新しい入力（参加条件の更新）で自動的に再開する。
func TestMissingPreparationExpiresThenResumes(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	p := s.personas()
	id := seed.SessionID
	p["A"].submitPreparation(id, "attending", prepData(false, []string{"sec_1"}, []string{}, 0)).mustStatus(t, 202)
	p["B"].submitPreparation(id, "attending", prepData(true, []string{"sec_2", "sec_3"}, []string{"sec_2", "sec_3"}, 40)).mustStatus(t, 202)
	p["C"].submitPreparation(id, "absent", prepData(false, []string{}, []string{}, 0)).mustStatus(t, 202)
	s.process()
	if d := p["A"].detail(id); d.CurrentProposal != nil || d.ActiveCase.Status != "collecting" {
		t.Fatal("D の回答前に案を作ってはいけない")
	}
	s.advance(25 * time.Hour)
	d := p["A"].detail(id)
	if d.ActiveCase.Status != "needs_owner" || str(d.ActiveCase.ReasonCode) != "deadline_expired" {
		t.Fatalf("参加条件の期限切れ: %s", dump(d.ActiveCase))
	}
	rev := p["D"].detail(id).Session.Revision
	p["D"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", map[string]any{"expected_revision": rev,
		"preparation": map[string]any{"attendance": "attending", "data": prepData(false, []string{"sec_1"}, []string{}, 0)}}).mustStatus(t, 202)
	s.process()
	d = p["A"].detail(id)
	if d.CurrentProposal == nil || d.CurrentProposal.ChangeKind != "initial" || d.ActiveCase.Status != "awaiting_consent" {
		t.Fatalf("新しい入力で再開するはず: %s", dump(d.ActiveCase))
	}
	// 欠席の C は担当・投票の対象にならない。初回案は担当者 B の引き受けと管理者 A の承認。
	if p["C"].openTask(id, "approval") != nil || p["B"].openTask(id, "assignment") == nil || p["A"].openTask(id, "owner_approval") == nil {
		t.Fatal("初回案のタスクが違う")
	}
}

// E10：AI 処理の障害。受理済みの入力は取り消さず、一時的な障害は再試行、不正な出力は管理者へ戻す。
// 費用が分からない呼び出しを0円にしない。
func TestAgentFailures(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID

	s.setFaults(map[string]any{"llm": "error"})
	p["B"].withdraw(id, "assignment").mustStatus(t, http.StatusAccepted)
	s.process()
	d := p["A"].detail(id)
	if d.ActiveCase.Status != "planning" || d.ActiveCase.NextRetryAt == nil || !strings.Contains(d.ActiveCase.Summary, "再試行") {
		t.Fatalf("一時的な障害は再試行を予定: %s", dump(d.ActiveCase))
	}
	if !d.ActiveCase.NextRetryAt.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("初回の再試行は30秒後: %v", d.ActiveCase.NextRetryAt)
	}
	var act apitypes.ActivityResponse
	p["A"].get("/api/sessions/"+id+"/activity").mustStatus(t, 200).decode(t, &act)
	if act.Summary.LLMCallCount != 1 || len(act.Summary.Costs) != 1 || act.Summary.Costs[0].Currency != "unknown" ||
		act.Summary.Costs[0].EstimatedAmount != nil || act.Summary.Costs[0].BilledAmount != nil {
		t.Fatalf("費用不明は null（0にしない）: %s", dump(act.Summary))
	}
	if act.Items[0].Kind != "agent_retry_scheduled" {
		t.Fatalf("履歴に再試行の予定: %s", dump(act.Items[0]))
	}
	// 待機中は呼ばない（時刻前は処理しない）。
	s.advance(10 * time.Second)
	if d := p["A"].detail(id); d.ActiveCase.Status != "planning" {
		t.Fatal("再試行時刻前に処理した")
	}
	s.setFaults(map[string]any{})
	s.advance(30 * time.Second)
	if p["C"].openTask(id, "preparation") == nil {
		t.Fatalf("障害が解消したら処理が進むはず: %s", dump(p["A"].detail(id).ActiveCase))
	}

	// 不正な出力は未承認の案を作らず、管理者判断待ち（model_error）にする。
	s.setFaults(map[string]any{"llm": "invalid_output"})
	p["C"].submitPreparation(id, "attending", prepData(true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)).mustStatus(t, http.StatusAccepted)
	s.process()
	d = p["A"].detail(id)
	if d.ActiveCase.Status != "needs_owner" || str(d.ActiveCase.ReasonCode) != "model_error" || d.CurrentProposal.Status == "pending" {
		t.Fatalf("不正な出力で停止: %s %s", dump(d.ActiveCase), dump(d.CurrentProposal))
	}
	if d.Session.Status != "needs_attention" || d.ConfirmedPlan == nil {
		t.Fatal("既存の計画は履歴として残し、要調整と表示する")
	}
}
