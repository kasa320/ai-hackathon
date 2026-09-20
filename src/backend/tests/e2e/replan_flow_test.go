package e2e

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

// E02：中心の流れ。B が辞退 → AI が C に確認 → C が「第2節なら15分」と回答 → 範囲を縮めた案 →
// C 本人の引き受けと参加予定者の過半数で確定 → 計画・未消化範囲を保存 → Discord に通知。
func TestReplanDemoFlow(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID

	d := p["B"].detail(id)
	if d.Session.Status != "confirmed" || d.ConfirmedPlan == nil || !d.Permissions.CanWithdrawAssignment {
		t.Fatalf("初期状態：B の担当で確定済みのはず: %s", dump(d.Session))
	}
	initialPlan := d.ConfirmedPlan.ID
	rev := d.Session.Revision

	// 1. B が担当を辞退する（私的な理由は送らない）。
	var acc apitypes.MutationAccepted
	p["B"].withdraw(id, "assignment").mustStatus(t, http.StatusAccepted).decode(t, &acc)
	if acc.Revision != rev+1 || acc.ProcessingStatus != "queued" || acc.CaseID == d.ActiveCase.ID {
		t.Fatalf("受付結果: %s", dump(acc))
	}
	d = p["A"].detail(id)
	if d.Session.Status != "needs_attention" || d.ConfirmedPlan == nil || d.ConfirmedPlan.ID != initialPlan {
		t.Fatalf("辞退後は要調整。以前の確定計画は履歴として残す: %s", dump(d.Session))
	}

	// 2. AI（仮の判断処理）が、担当できる人がいないため準備済みの C に確認する。
	s.process()
	if p["C"].openTask(id, "preparation") == nil {
		t.Fatalf("C への確認依頼がない: %s", dump(p["A"].detail(id).ActiveCase))
	}
	if p["B"].openTask(id, "preparation") != nil || p["D"].openTask(id, "preparation") != nil {
		t.Fatal("辞退した B・準備していない D には依頼しない")
	}
	if !sentTo(s, persona["C"]) {
		t.Fatal("C への依頼が Discord に通知されていない")
	}

	// 3. C が「第2節なら15分説明できる」と回答する。参加条件の回答では引き受けにならない。
	p["C"].submitPreparation(id, "attending", prepData(true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)).mustStatus(t, http.StatusAccepted)
	s.process()
	d = p["C"].detail(id)
	prop := d.CurrentProposal
	if prop == nil || prop.Status != "pending" || prop.ChangeKind != "replan" || prop.Author != "agent" || d.ActiveCase.Status != "awaiting_consent" {
		t.Fatalf("再計画の案がない: %s", dump(d.ActiveCase))
	}
	var plan struct {
		Covered  []string `json:"covered_section_ids"`
		Deferred []string `json:"deferred_section_ids"`
		Agenda   []struct {
			Activity  string  `json:"activity"`
			Presenter *string `json:"presenter_member_id"`
			Minutes   int     `json:"minutes"`
		} `json:"agenda"`
	}
	_ = json.Unmarshal(prop.Data, &plan)
	cID := memberID(t, d, "C")
	if !reflect.DeepEqual(plan.Covered, []string{"sec_2"}) || !reflect.DeepEqual(plan.Deferred, []string{"sec_3"}) ||
		plan.Agenda[0].Activity != "presentation" || str(plan.Agenda[0].Presenter) != cID || plan.Agenda[0].Minutes != 15 {
		t.Fatalf("C の条件（第2節・15分）に沿った案になっていない: %s", prop.Data)
	}
	total := 0
	for _, a := range plan.Agenda {
		total += a.Minutes
	}
	if total > 60 {
		t.Fatalf("合計 %d 分が持ち時間を超えている", total)
	}
	if len(prop.Assignments) != 1 || prop.Assignments[0].MemberID != cID || prop.Assignments[0].Status != "pending" {
		t.Fatalf("C 本人の引き受けが必要: %s", dump(prop.Assignments))
	}
	if len(prop.Approvals) != 1 || prop.Approvals[0].Kind != "majority" || prop.Approvals[0].RequiredCount != 3 || len(prop.Approvals[0].EligibleMemberIDs) != 4 {
		t.Fatalf("範囲の変更には参加予定者4人の過半数（3人）が必要: %s", dump(prop.Approvals))
	}
	// 1人に複数タスク（C は引き受けと投票）。
	if p["C"].openTask(id, "assignment") == nil || p["C"].openTask(id, "approval") == nil {
		t.Fatal("C には assignment と approval の両方が必要")
	}

	// 4. 投票が過半数に達しても、C 本人の引き受けがなければ確定しない。
	for _, name := range []string{"A", "B", "D"} {
		p[name].respond(id, "approval", "approve").mustStatus(t, http.StatusAccepted)
	}
	if d := p["A"].detail(id); d.Session.Status == "confirmed" || len(d.CurrentProposal.Approvals[0].ApprovedMemberIDs) != 3 {
		t.Fatalf("引き受け前に確定した: %s", dump(d.Session))
	}
	p["C"].respond(id, "assignment", "accept").mustStatus(t, http.StatusAccepted)

	// 5. 必要な条件が揃うとバックエンドが確定する。未消化範囲（sec_3）も保存される。
	d = p["D"].detail(id)
	if d.Session.Status != "confirmed" || d.ConfirmedPlan == nil || d.ConfirmedPlan.ID != prop.ID || d.ActiveCase.Status != "confirmed" {
		t.Fatalf("確定していない: %s", dump(d.Session))
	}
	if d.ConfirmedPlan.Assignments[0].Status != "accepted" {
		t.Fatalf("引き受け状況: %s", dump(d.ConfirmedPlan.Assignments))
	}
	if p["C"].openTask(id, "approval") != nil {
		t.Fatal("確定後に投票タスクが残っている")
	}

	// 6. 確定の通知は計画確定とは別に記録され、送信後に sent になる（revision は変わらない）。
	revConfirmed := d.Session.Revision
	if d.NotificationSummary.PendingCount == 0 {
		t.Fatal("確定直後は通知待ちがあるはず")
	}
	s.process()
	d = p["D"].detail(id)
	if d.NotificationSummary.PendingCount != 0 || d.NotificationSummary.SentCount == 0 || d.Session.Revision != revConfirmed {
		t.Fatalf("通知状況: %s rev=%d", dump(d.NotificationSummary), d.Session.Revision)
	}
	if !sentContaining(s, "計画が確定しました") || !sentContaining(s, "/session.html?id="+id) {
		t.Fatal("確定の通知（画面へのリンク付き）が送られていない")
	}

	// 7. 管理者は実行履歴・費用・通知状況を確認できる。
	var act apitypes.ActivityResponse
	p["A"].get("/api/sessions/"+id+"/activity").mustStatus(t, 200).decode(t, &act)
	kinds := map[string]bool{}
	for _, it := range act.Items {
		kinds[it.Kind] = true
	}
	for _, k := range []string{"input_received", "proposal_created", "response_recorded", "plan_confirmed", "notification_updated"} {
		if !kinds[k] {
			t.Fatalf("履歴に %s がない: %v", k, kinds)
		}
	}
	if act.Summary.CaseID == nil || *act.Summary.CaseID != acc.CaseID || act.Summary.ToolCallCount < 2 || act.Summary.LLMCallCount != 0 || len(act.Summary.Costs) != 0 {
		t.Fatalf("集計（fake モードは LLM 0回・費用なし）: %s", dump(act.Summary))
	}
	for i := 1; i < len(act.Items); i++ {
		if act.Items[i].OccurredAt.After(act.Items[i-1].OccurredAt) {
			t.Fatal("履歴は新しい順")
		}
	}
	if r := p["B"].get("/api/sessions/" + id + "/activity"); r.status != 403 || r.errorCode(t) != "forbidden" {
		t.Fatalf("メンバーは実行履歴を見られない: %d", r.status)
	}
}

// E01：C が全範囲を担当できる場合は、範囲・進行表が同じ「担当だけの変更」になり、C 本人の引き受けだけで確定する。
func TestPresenterOnlyChangeNeedsOnlyAcceptance(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID

	rev := p["C"].detail(id).Session.Revision
	p["C"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", map[string]any{
		"expected_revision": rev,
		"preparation":       map[string]any{"attendance": "attending", "data": prepData(true, []string{"sec_1", "sec_2", "sec_3"}, []string{"sec_2", "sec_3"}, 30)},
	}).mustStatus(t, http.StatusAccepted)
	if d := p["A"].detail(id); d.Session.Status != "confirmed" {
		t.Fatal("確定計画に影響しない参加条件の変更で要調整にしない")
	}
	p["B"].withdraw(id, "assignment").mustStatus(t, http.StatusAccepted)
	s.process()
	d := p["C"].detail(id)
	if d.CurrentProposal == nil || len(d.CurrentProposal.Approvals) != 0 || len(d.CurrentProposal.Assignments) != 1 {
		t.Fatalf("担当だけの変更は承認不要: %s", dump(d.CurrentProposal))
	}
	if p["A"].openTask(id, "approval") != nil {
		t.Fatal("投票タスクを作らない")
	}
	p["C"].respond(id, "assignment", "accept").mustStatus(t, http.StatusAccepted)
	if d := p["A"].detail(id); d.Session.Status != "confirmed" || d.ConfirmedPlan.ID != d.CurrentProposal.ID {
		t.Fatalf("C の引き受けで確定するはず: %s", dump(d.Session))
	}
}

func sentTo(s *server, discordID string) bool {
	for _, m := range s.sender.all() {
		for _, u := range m.MentionUserIDs {
			if u == discordID {
				return true
			}
		}
	}
	return false
}

func sentContaining(s *server, text string) bool {
	for _, m := range s.sender.all() {
		if strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}
