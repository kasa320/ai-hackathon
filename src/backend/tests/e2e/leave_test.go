package e2e

import (
	"net/http"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

func (c *client) leaveGroup(groupID string) response {
	c.t.Helper()
	return c.send(http.MethodPost, "/api/groups/"+groupID+"/leave", map[string]any{})
}

// 脱退すると本人からは見えなくなり、開始前の開催回では欠席として扱われる。
func TestLeaveGroupOverHTTP(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	people := s.personas()

	before := people["A"].detail(seed.SessionID)
	leftID := memberID(t, before, "D")

	var out apitypes.LeaveGroupResult
	people["D"].leaveGroup(seed.GroupID).mustStatus(t, 200).decode(t, &out)
	if out.GroupID != seed.GroupID {
		t.Errorf("group_id が %q（期待 %q）", out.GroupID, seed.GroupID)
	}
	if len(out.AffectedSessionIDs) != 1 || out.AffectedSessionIDs[0] != seed.SessionID {
		t.Errorf("affected_session_ids が %v（期待 [%s]）", out.AffectedSessionIDs, seed.SessionID)
	}

	// 本人からはグループも開催回も見えない。
	people["D"].get("/api/groups/"+seed.GroupID).mustStatus(t, 404)
	people["D"].get("/api/sessions/"+seed.SessionID).mustStatus(t, 404)

	// 在籍者の一覧からは消える。
	var g apitypes.Group
	people["A"].get("/api/groups/"+seed.GroupID).mustStatus(t, 200).decode(t, &g)
	for _, m := range g.Members {
		if m.ID == leftID {
			t.Error("脱退した D がメンバー一覧に残っています")
		}
	}

	// 開催回は固定した顔ぶれを保ち、left で見分けられる。
	after := people["A"].detail(seed.SessionID)
	var found *apitypes.Member
	for i := range after.Members {
		if after.Members[i].ID == leftID {
			found = &after.Members[i]
		}
	}
	if found == nil {
		t.Fatal("開催回の参加対象者から D が消えています")
	}
	if !found.Left {
		t.Error("D の left が false です")
	}
	for _, p := range after.Preparations {
		if p.MemberID == leftID && (p.Value == nil || p.Value.Attendance != "absent") {
			t.Errorf("D の参加条件が %+v（期待 absent）", p.Value)
		}
	}
}

// 管理者の脱退は 409 invalid_state。
func TestOwnerCannotLeaveGroupOverHTTP(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	people := s.personas()

	res := people["A"].leaveGroup(seed.GroupID).mustStatus(t, 409)
	if code := res.errorCode(t); code != "invalid_state" {
		t.Errorf("エラーコードが %q（期待 invalid_state）", code)
	}
	people["A"].get("/api/groups/"+seed.GroupID).mustStatus(t, 200)
}

// 同じ Idempotency-Key での再送は、脱退をやり直さず同じ応答を返す。
func TestLeaveGroupIsIdempotent(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	people := s.personas()

	const key = "leave-once"
	first := people["D"].sendKey(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", key, map[string]any{}).mustStatus(t, 200)
	second := people["D"].sendKey(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", key, map[string]any{}).mustStatus(t, 200)
	if string(first.body) != string(second.body) {
		t.Errorf("再送で応答が変わりました:\n1回目 %s\n2回目 %s", first.body, second.body)
	}

	// 鍵なしで送り直すと、もう所属していないので 404。
	people["D"].leaveGroup(seed.GroupID).mustStatus(t, 404)
}

// 脱退した状態は再起動後も残る。
func TestLeaveGroupSurvivesRestart(t *testing.T) {
	s := newServer(t)
	seed := s.seed("replan_demo")
	s.personas()["D"].leaveGroup(seed.GroupID).mustStatus(t, 200)

	s.restart()

	people := s.personas()
	people["D"].get("/api/groups/"+seed.GroupID).mustStatus(t, 404)
	var g apitypes.Group
	people["A"].get("/api/groups/"+seed.GroupID).mustStatus(t, 200).decode(t, &g)
	if len(g.Members) != 3 {
		t.Errorf("在籍者が %d 人（期待 3）", len(g.Members))
	}
}
