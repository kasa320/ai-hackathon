package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

func TestGroupKindIsReadingAndCannotBeChanged(t *testing.T) {
	s := newServer(t)
	s.seed("initial_demo")
	a := s.client().devLogin("A")
	invitees := []map[string]string{{"discord_user_id": "222222222222222222", "display_name": "B"}}

	// 種別を省略した従来の作成は reading になり、一覧・詳細に出る。
	var g apitypes.Group
	a.send(http.MethodPost, "/api/groups", map[string]any{"name": "従来", "invitees": invitees}).mustStatus(t, http.StatusCreated).decode(t, &g)
	if g.PlaybookID != "reading" {
		t.Fatalf("省略時の種別 = %q", g.PlaybookID)
	}
	a.send(http.MethodPost, "/api/groups", map[string]any{"name": "明示", "playbook_id": "reading", "invitees": invitees}).mustStatus(t, http.StatusCreated)
	var list apitypes.GroupList
	a.get("/api/groups").mustStatus(t, 200).decode(t, &list)
	if len(list.Items) != 3 {
		t.Fatalf("一覧 = %s", dump(list))
	}
	for _, item := range list.Items {
		if item.PlaybookID != "reading" {
			t.Fatalf("一覧の種別 = %s", dump(list))
		}
	}

	// 未対応の種別は作成できない。
	r := a.send(http.MethodPost, "/api/groups", map[string]any{"name": "会議", "playbook_id": "meeting", "invitees": invitees})
	if r.status != 422 || r.errorCode(t) != "validation_failed" || !strings.Contains(string(r.body), `"path":"playbook_id"`) {
		t.Fatalf("未対応の種別 = %d %s", r.status, r.body)
	}

	// 種別は名前変更の入力に含められない（未知フィールドとして拒否）。
	r = a.send(http.MethodPatch, "/api/groups/"+g.ID, map[string]any{"name": "新名称", "playbook_id": "meeting"})
	if r.status != 400 || r.errorCode(t) != "invalid_json" {
		t.Fatalf("種別の変更 = %d %s", r.status, r.body)
	}
	a.get("/api/groups/"+g.ID).mustStatus(t, 200).decode(t, &g)
	if g.PlaybookID != "reading" || g.Name != "従来" {
		t.Fatalf("拒否したのに変更された: %s", dump(g))
	}
}

func TestOnlyOwnerCanRenameGroup(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	people := s.personas()
	path := "/api/groups/" + seed.GroupID

	var g apitypes.Group
	people["A"].send(http.MethodPatch, path, map[string]any{"name": "  改名後の会  "}).mustStatus(t, 200).decode(t, &g)
	if g.Name != "改名後の会" || g.PlaybookID != "reading" || len(g.Members) != 4 {
		t.Fatalf("改名結果 = %s", dump(g))
	}
	people["B"].get(path).mustStatus(t, 200).decode(t, &g)
	if g.Name != "改名後の会" {
		t.Fatalf("他のメンバーに反映されない: %s", dump(g))
	}

	// 一般メンバーは 403、グループ外の人は存在も分からない 404、未ログインは 401。
	if r := people["B"].send(http.MethodPatch, path, map[string]any{"name": "乗っ取り"}); r.status != 403 || r.errorCode(t) != "forbidden" {
		t.Fatalf("一般メンバーの改名 = %d %s", r.status, r.body)
	}
	outsider := s.client()
	outsider.oauthLogin("999999999999999999", "外部", "/")
	if r := outsider.send(http.MethodPatch, path, map[string]any{"name": "外部"}); r.status != 404 {
		t.Fatalf("グループ外の改名 = %d %s", r.status, r.body)
	}
	if r := s.client().do(http.MethodPatch, path, jsonBody(map[string]any{"name": "x"}), nil); r.status != 401 {
		t.Fatalf("未ログインの改名 = %d %s", r.status, r.body)
	}
	for name, body := range map[string]any{"空": map[string]any{"name": "  "}, "長すぎる": map[string]any{"name": strings.Repeat("あ", 101)}} {
		if r := people["A"].send(http.MethodPatch, path, body); r.status != 422 || r.errorCode(t) != "validation_failed" {
			t.Fatalf("%sの名前 = %d %s", name, r.status, r.body)
		}
	}
	people["B"].get(path).mustStatus(t, 200).decode(t, &g)
	if g.Name != "改名後の会" {
		t.Fatalf("不正な改名で名前が変わった: %s", g.Name)
	}
}
