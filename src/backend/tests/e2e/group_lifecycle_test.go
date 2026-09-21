package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

func TestMemberLeavesGroupAndFutureSessionIsReplanned(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	people := s.personas()
	key := newKey()

	var result apitypes.GroupLifecycleResult
	people["B"].sendKey(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", key, map[string]any{}).
		mustStatus(t, http.StatusOK).decode(t, &result)
	if result.Status != "left" || result.AffectedSessionCount != 1 || result.NotificationCount != 3 {
		t.Fatalf("脱退結果 = %s", dump(result))
	}
	// 通信再送は、脱退後に権限がなくなっていても最初と同じ成功応答を返す。
	people["B"].sendKey(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", key, map[string]any{}).mustStatus(t, http.StatusOK)
	if r := people["B"].send(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", map[string]any{}); r.status != http.StatusNotFound {
		t.Fatalf("別操作としての再脱退 = %d %s", r.status, r.body)
	}

	var groups apitypes.GroupList
	people["B"].get("/api/groups").mustStatus(t, http.StatusOK).decode(t, &groups)
	if len(groups.Items) != 0 {
		t.Fatalf("脱退者のグループ一覧 = %s", dump(groups))
	}
	if r := people["B"].get("/api/sessions/" + seed.SessionID); r.status != http.StatusNotFound {
		t.Fatalf("脱退者が開催回を閲覧できる: %d", r.status)
	}

	var group apitypes.Group
	people["A"].get("/api/groups/"+seed.GroupID).mustStatus(t, http.StatusOK).decode(t, &group)
	if len(group.Members) != 3 {
		t.Fatalf("在籍メンバー = %s", dump(group))
	}
	detail := people["A"].detail(seed.SessionID)
	if len(detail.Members) != 4 {
		t.Fatalf("開催回の過去メンバーが消えた: %s", dump(detail.Members))
	}
	foundAbsent := false
	for _, p := range detail.Preparations {
		if p.MemberID == detail.Members[1].ID && p.Value != nil && p.Value.Attendance == "absent" {
			foundAbsent = true
		}
	}
	if !foundAbsent || detail.Session.Revision <= 1 || detail.ActiveCase == nil {
		t.Fatalf("欠席・再調整になっていない: %s", dump(detail))
	}

	s.process()
	msgs := s.sender.all()
	if len(msgs) != 3 {
		t.Fatalf("脱退DM = %d件, want 3", len(msgs))
	}
	seen := map[string]bool{}
	for _, msg := range msgs {
		if msg.Kind != "member_left" || len(msg.DMUserIDs) != 1 || msg.DMUserIDs[0] == persona["B"] {
			t.Fatalf("脱退DMの宛先 = %+v", msg)
		}
		seen[msg.DMUserIDs[0]] = true
	}
	if len(seen) != 3 {
		t.Fatalf("個人ごとのDMになっていない: %v", seen)
	}
}

func TestGroupDeletionPermissionsAndPersonalDMs(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	people := s.personas()

	if r := people["B"].send(http.MethodDelete, "/api/groups/"+seed.GroupID, map[string]any{}); r.status != http.StatusForbidden {
		t.Fatalf("一般メンバーの削除 = %d %s", r.status, r.body)
	}
	if r := people["A"].send(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", map[string]any{}); r.status != http.StatusForbidden {
		t.Fatalf("管理者の脱退 = %d %s", r.status, r.body)
	}
	key := newKey()
	var result apitypes.GroupLifecycleResult
	people["A"].sendKey(http.MethodDelete, "/api/groups/"+seed.GroupID, key, map[string]any{}).
		mustStatus(t, http.StatusOK).decode(t, &result)
	if result.Status != "deleted" || result.NotificationCount != 4 {
		t.Fatalf("削除結果 = %s", dump(result))
	}
	people["A"].sendKey(http.MethodDelete, "/api/groups/"+seed.GroupID, key, map[string]any{}).mustStatus(t, http.StatusOK)
	for name, client := range people {
		if r := client.get("/api/groups/" + seed.GroupID); r.status != http.StatusNotFound {
			t.Fatalf("%s が削除済みグループを閲覧: %d", name, r.status)
		}
	}

	s.process()
	msgs := s.sender.all()
	if len(msgs) != 4 {
		t.Fatalf("削除DM = %d件, want 4", len(msgs))
	}
	seen := map[string]bool{}
	for _, msg := range msgs {
		if msg.Kind != "group_deleted" || len(msg.DMUserIDs) != 1 {
			t.Fatalf("削除DMの宛先 = %+v", msg)
		}
		seen[msg.DMUserIDs[0]] = true
	}
	if len(seen) != 4 {
		t.Fatalf("個人ごとのDMになっていない: %v", seen)
	}
}

func TestLeaveAndDeleteAreBlockedWhileSessionIsRunning(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	people := s.personas()
	s.clk.Advance(72 * time.Hour)

	if r := people["B"].send(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", map[string]any{}); r.status != http.StatusConflict || r.errorCode(t) != "invalid_state" {
		t.Fatalf("開催中の脱退 = %d %s", r.status, r.body)
	}
	if r := people["A"].send(http.MethodDelete, "/api/groups/"+seed.GroupID, map[string]any{}); r.status != http.StatusConflict || r.errorCode(t) != "invalid_state" {
		t.Fatalf("開催中の削除 = %d %s", r.status, r.body)
	}

	// 60分の開催回が終われば操作でき、終了済みの回は書き換えない。
	s.clk.Advance(time.Hour)
	var result apitypes.GroupLifecycleResult
	people["B"].send(http.MethodPost, "/api/groups/"+seed.GroupID+"/leave", map[string]any{}).
		mustStatus(t, http.StatusOK).decode(t, &result)
	if result.AffectedSessionCount != 0 {
		t.Fatalf("終了済み開催回を変更した: %s", dump(result))
	}
}
