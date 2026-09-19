package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

// E05：同じ操作の再送・二重送信で、投票・計画・案件・通知待ちを二重に作らない。
func TestRetriesAndDuplicates(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID
	path := "/api/sessions/" + id + "/withdrawals"
	body := map[string]any{"expected_revision": p["B"].detail(id).Session.Revision, "scope": "assignment"}

	// 通信失敗時の再送（同じキー・同じ本文）は最初の成功をそのまま返す。
	key := newKey()
	first := p["B"].sendKey(http.MethodPost, path, key, body).mustStatus(t, 202)
	again := p["B"].sendKey(http.MethodPost, path, key, body).mustStatus(t, 202)
	if string(first.body) != string(again.body) {
		t.Fatalf("再送の応答が違う: %s / %s", first.body, again.body)
	}
	// 同じキーを別の操作に使うと 409。
	if r := p["B"].sendKey(http.MethodPost, path, key, map[string]any{"expected_revision": 99, "scope": "attendance"}); r.status != 409 || r.errorCode(t) != "idempotency_key_reused" {
		t.Fatalf("キーの再利用: %d", r.status)
	}
	// 別のキーでも同じ辞退状態なら、二重の案件を作らず現在の受付結果を返す。
	var a1, a2 apitypes.MutationAccepted
	first.decode(t, &a1)
	p["B"].send(http.MethodPost, path, map[string]any{"expected_revision": a1.Revision, "scope": "assignment"}).mustStatus(t, 202).decode(t, &a2)
	if a1.CaseID != a2.CaseID || a1.Revision != a2.Revision {
		t.Fatalf("二重の案件: %+v %+v", a1, a2)
	}

	s.process()
	p["C"].submitPreparation(id, "attending", prepData(true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)).mustStatus(t, 202)
	s.process()

	// 投票の再送は同じ結果、別のキーでの二重投票は 409。
	tk := p["A"].openTask(id, "approval")
	vote := map[string]any{"decision": "approve", "proposal_id": tk.ProposalID, "proposal_version": tk.ProposalVersion}
	vkey := newKey()
	p["A"].sendKey(http.MethodPost, "/api/tasks/"+tk.ID+"/responses", vkey, vote).mustStatus(t, 202)
	p["A"].sendKey(http.MethodPost, "/api/tasks/"+tk.ID+"/responses", vkey, vote).mustStatus(t, 202)
	if r := p["A"].send(http.MethodPost, "/api/tasks/"+tk.ID+"/responses", vote); r.status != 409 || r.errorCode(t) != "task_closed" {
		t.Fatalf("二重投票: %d", r.status)
	}
	if d := p["A"].detail(id); len(d.CurrentProposal.Approvals[0].ApprovedMemberIDs) != 1 {
		t.Fatalf("票が二重に数えられた: %s", dump(d.CurrentProposal.Approvals))
	}

	// 確定と通知も1回だけ。処理を繰り返しても通知を送り直さない。
	p["B"].respond(id, "approval", "approve").mustStatus(t, 202)
	p["D"].respond(id, "approval", "approve").mustStatus(t, 202)
	ckey := newKey()
	ctk := p["C"].openTask(id, "assignment")
	accept := map[string]any{"decision": "accept", "proposal_id": ctk.ProposalID, "proposal_version": ctk.ProposalVersion}
	p["C"].sendKey(http.MethodPost, "/api/tasks/"+ctk.ID+"/responses", ckey, accept).mustStatus(t, 202)
	p["C"].sendKey(http.MethodPost, "/api/tasks/"+ctk.ID+"/responses", ckey, accept).mustStatus(t, 202)
	s.process()
	sent := len(s.sender.all())
	confirms := 0
	for _, m := range s.sender.all() {
		if strings.Contains(m.Content, "計画が確定しました") {
			confirms++
		}
	}
	s.process()
	s.advance(0)
	if confirms != 1 || len(s.sender.all()) != sent {
		t.Fatalf("確定の通知 %d 件、再処理で %d → %d 件", confirms, sent, len(s.sender.all()))
	}
}

// 同じキーの更新が同時に届いても1回分として処理される。
func TestConcurrentSameKey(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	b := s.client().devLogin("B")
	id := seed.SessionID
	body := map[string]any{"expected_revision": b.detail(id).Session.Revision, "scope": "assignment"}
	key := newKey()
	var wg sync.WaitGroup
	results := make([]response, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = b.sendKey(http.MethodPost, "/api/sessions/"+id+"/withdrawals", key, body)
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if r.status != 202 || string(r.body) != string(results[0].body) {
			t.Fatalf("同時の再送: %d %s", r.status, r.body)
		}
	}
	var acc apitypes.MutationAccepted
	_ = json.Unmarshal(results[0].body, &acc)
	if d := b.detail(id); d.Session.Revision != acc.Revision {
		t.Fatalf("更新が複数回行われた: %d vs %d", d.Session.Revision, acc.Revision)
	}
}

// E06：版1の投票中に参加条件が変わると、旧版への回答は 409 になり、新版で同意を取り直す。
func TestStaleVersionAndRevisionConflict(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID
	p["B"].withdraw(id, "assignment").mustStatus(t, 202)
	s.process()
	p["C"].submitPreparation(id, "attending", prepData(true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)).mustStatus(t, 202)
	s.process()
	old := p["A"].openTask(id, "approval")
	oldVersion := *old.ProposalVersion

	// 古い revision での更新は 409 revision_conflict（現在の revision を返す）。
	cur := p["D"].detail(id).Session.Revision
	r := p["D"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", map[string]any{"expected_revision": cur - 1,
		"preparation": map[string]any{"attendance": "absent", "data": prepData(false, []string{}, []string{}, 0)}})
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	r.decode(t, &e)
	if r.status != 409 || e.Error.Code != "revision_conflict" || e.Error.Details["current_revision"] != float64(cur) {
		t.Fatalf("revision_conflict: %d %s", r.status, r.body)
	}

	// D が欠席に変更 → 版1は旧版になり、未回答タスクは無効。
	p["D"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", map[string]any{"expected_revision": cur,
		"preparation": map[string]any{"attendance": "absent", "data": prepData(false, []string{}, []string{}, 0)}}).mustStatus(t, 202)
	r = p["A"].send(http.MethodPost, "/api/tasks/"+old.ID+"/responses", map[string]any{"decision": "approve", "proposal_id": old.ProposalID, "proposal_version": old.ProposalVersion})
	if r.status != 409 || r.errorCode(t) != "proposal_superseded" {
		t.Fatalf("旧版への回答: %d %s", r.status, r.body)
	}
	s.process()
	d := p["A"].detail(id)
	if d.CurrentProposal.Version <= oldVersion || d.CurrentProposal.Status != "pending" {
		t.Fatalf("新しい版で取り直す: %s", dump(d.CurrentProposal))
	}
	// 新しい版の投票対象は案の作成時点の参加予定者（欠席の D を除く3人、必要2人）。
	if a := d.CurrentProposal.Approvals[0]; len(a.EligibleMemberIDs) != 3 || a.RequiredCount != 2 {
		t.Fatalf("投票対象: %s", dump(a))
	}
	// 旧版の同意は新しい版に流用しない。
	if len(d.CurrentProposal.Approvals[0].ApprovedMemberIDs) != 0 {
		t.Fatal("旧版の同意が流用された")
	}
}
