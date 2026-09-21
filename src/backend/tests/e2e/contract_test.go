package e2e

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func split(s string) []string {
	out := strings.Fields(s)
	sort.Strings(out)
	return out
}

// GET /api/sessions/{id} の形が docs/data-structure.md の SessionDetail と同じキーを持つ（初期登録直後）。
func TestSessionDetailShape(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	b := s.client().devLogin("B")
	r := b.get("/api/sessions/"+seed.SessionID).mustStatus(t, 200)
	if r.header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
		t.Fatalf("headers: %v", r.header)
	}
	var d map[string]any
	r.decode(t, &d)
	want := split("session data members preparations confirmed_plan current_proposal active_case my_tasks permissions current_member_id notification_summary server_now")
	if got := keys(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("トップレベルのキー: %v", got)
	}
	sess := d["session"].(map[string]any)
	if got := keys(sess); !reflect.DeepEqual(got, split("id group_id playbook_id title starts_at schedule_status period_start period_end duration_minutes revision status updated_at")) {
		t.Fatalf("session のキー: %v", got)
	}
	for _, k := range []string{"starts_at", "updated_at"} {
		if !strings.HasSuffix(sess[k].(string), "Z") {
			t.Fatalf("日時は UTC の Z 表記: %s=%v", k, sess[k])
		}
	}
	if !strings.HasSuffix(d["server_now"].(string), "Z") {
		t.Fatal("server_now は Z 表記")
	}
	if d["confirmed_plan"] != nil || d["current_proposal"] != nil {
		t.Fatal("初期状態では null")
	}
	for _, p := range d["preparations"].([]any) {
		pm := p.(map[string]any)
		if v, ok := pm["value"]; !ok || v != nil {
			t.Fatalf("未回答は value=null: %v", pm)
		}
	}
	ac := d["active_case"].(map[string]any)
	if got := keys(ac); !reflect.DeepEqual(got, split("id status reason_code summary next_retry_at")) || ac["reason_code"] != nil || ac["next_retry_at"] != nil {
		t.Fatalf("active_case: %v", ac)
	}
	task := d["my_tasks"].([]any)[0].(map[string]any)
	if got := keys(task); !reflect.DeepEqual(got, split("id session_id kind status title due_at proposal_id proposal_version allowed_decisions")) {
		t.Fatalf("task のキー: %v", got)
	}
	if task["proposal_id"] != nil || task["proposal_version"] != nil || !reflect.DeepEqual(task["allowed_decisions"], []any{"submit"}) {
		t.Fatalf("preparation タスク: %v", task)
	}
	perm := d["permissions"].(map[string]any)
	wantPerm := map[string]any{"can_update_preparation": true, "can_withdraw_assignment": false, "can_withdraw_attendance": true, "can_submit_proposal": false, "can_view_activity": false, "can_delete_session": false}
	if !reflect.DeepEqual(perm, wantPerm) {
		t.Fatalf("B の権限: %v", perm)
	}
	member := d["members"].([]any)[0].(map[string]any)
	if got := keys(member); !reflect.DeepEqual(got, split("id display_name role joined")) {
		t.Fatalf("member のキー: %v", got)
	}
	if strings.Contains(string(r.body), persona["A"]) {
		t.Fatal("Discord ID を返してはいけない")
	}
}

// 案（Proposal）と実行履歴の形（docs/data-structure.md）。
func TestProposalAndActivityShape(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	p := s.personas()
	id := seed.SessionID
	p["B"].withdraw(id, "assignment").mustStatus(t, 202)
	s.process()
	p["C"].submitPreparation(id, "attending", prepData(true, []string{"sec_1", "sec_2"}, []string{"sec_2"}, 15)).mustStatus(t, 202)
	s.process()

	var d map[string]any
	p["A"].get("/api/sessions/"+id).mustStatus(t, 200).decode(t, &d)
	prop := d["current_proposal"].(map[string]any)
	if got := keys(prop); !reflect.DeepEqual(got, split("id version status change_kind author summary data approvals assignments")) {
		t.Fatalf("proposal のキー: %v", got)
	}
	appr := prop["approvals"].([]any)[0].(map[string]any)
	if got := keys(appr); !reflect.DeepEqual(got, split("kind eligible_member_ids required_count approved_member_ids rejected_member_ids")) {
		t.Fatalf("approval のキー: %v", got)
	}
	if got := keys(prop["assignments"].([]any)[0].(map[string]any)); !reflect.DeepEqual(got, split("member_id status")) {
		t.Fatalf("assignment のキー: %v", got)
	}
	// 用途固有の中身は data に包まれ、第1部の型に輪読の用語が出ない。
	if _, ok := prop["data"].(map[string]any)["agenda"]; !ok {
		t.Fatal("PlanData が data に入っていない")
	}

	var act map[string]any
	p["A"].get("/api/sessions/"+id+"/activity").mustStatus(t, 200).decode(t, &act)
	if got := keys(act); !reflect.DeepEqual(got, split("items summary")) {
		t.Fatalf("activity: %v", got)
	}
	item := act["items"].([]any)[0].(map[string]any)
	if got := keys(item); !reflect.DeepEqual(got, split("id occurred_at case_id kind summary proposal_id")) {
		t.Fatalf("activity item: %v", got)
	}
	sum := act["summary"].(map[string]any)
	if got := keys(sum); !reflect.DeepEqual(got, split("case_id llm_call_count tool_call_count costs notifications")) {
		t.Fatalf("activity summary: %v", got)
	}
	// 呼び出し0回なら costs は空配列。
	if costs, ok := sum["costs"].([]any); !ok || len(costs) != 0 {
		t.Fatalf("costs: %v", sum["costs"])
	}
	// 履歴には通知本文・プロンプト・モデルの生出力を含めない。
	raw, _ := json.Marshal(act)
	if strings.Contains(string(raw), "<@") || strings.Contains(string(raw), "session.html") {
		t.Fatal("履歴に通知本文が含まれている")
	}
}

func TestPublicAndErrorShapes(t *testing.T) {
	s := newServer(t)
	c := s.client()
	var h map[string]any
	c.get("/api/health").mustStatus(t, 200).decode(t, &h)
	if h["status"] != "ok" || h["now"] != "2026-09-19T09:00:00Z" || h["dev_mode"] != true {
		t.Fatalf("health: %v", h)
	}
	r := c.get("/api/playbooks").mustStatus(t, 200)
	if strings.TrimSpace(string(r.body)) != `{"playbooks":[{"id":"reading","name":"輪読"}]}` {
		t.Fatalf("playbooks: %s", r.body)
	}
	if r := c.get("/api/unknown"); r.status != 404 || r.errorCode(t) != "not_found" {
		t.Fatalf("未定義: %d", r.status)
	}
	if r := c.do(http.MethodPatch, "/api/sessions/x", nil, nil); r.status != 405 || r.errorCode(t) != "method_not_allowed" {
		t.Fatalf("メソッド違い: %d", r.status)
	}
	if r := c.get("/api/groups"); r.status != 401 || r.errorCode(t) != "unauthenticated" {
		t.Fatalf("未ログイン: %d", r.status)
	}
}
