package e2e

import (
	"net/http"
	"testing"
)

// 開催回の削除は管理者だけ。再送は同じ応答を返し、削除後は存在しない回として扱う。
func TestDeleteSessionOverHTTP(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	p := s.personas()
	path := "/api/sessions/" + seed.SessionID

	if r := p["B"].send(http.MethodDelete, path, map[string]any{}); r.status != 403 || r.errorCode(t) != "forbidden" {
		t.Fatalf("管理者以外の削除: %d %s", r.status, r.body)
	}

	key := newKey()
	var out map[string]any
	p["A"].sendKey(http.MethodDelete, path, key, map[string]any{}).mustStatus(t, 200).decode(t, &out)
	if out["session_id"] != seed.SessionID || out["group_id"] != seed.GroupID {
		t.Fatalf("応答: %v", out)
	}
	// 通信エラー後の再送は同じ応答になる
	p["A"].sendKey(http.MethodDelete, path, key, map[string]any{}).mustStatus(t, 200)

	if r := p["A"].get(path); r.status != 404 {
		t.Fatalf("削除後の取得: %d", r.status)
	}
	if r := p["A"].send(http.MethodDelete, path, map[string]any{}); r.status != 404 {
		t.Fatalf("2回目の削除: %d", r.status)
	}
	s.process()
}
