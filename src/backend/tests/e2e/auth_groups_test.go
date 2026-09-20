package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

const sessionData = `{
  "book_title": "サンプル技術書", "isbn": null, "toc_source": {"kind": "manual", "urls": []},
  "sections": [{"id": "sec_1", "title": "前回の範囲"}, {"id": "sec_2", "title": "今回の前半"}, {"id": "sec_3", "title": "今回の後半"}],
  "completed_section_ids": ["sec_1"], "target_section_ids": ["sec_2", "sec_3"]
}`

func sessionBody(startsAt time.Time) string {
	return `{"playbook_id":"reading","title":"第2回","starts_at":"` + startsAt.Format(time.RFC3339) + `","duration_minutes":60,"data":` + sessionData + `}`
}

// Discord ログイン → グループ作成（招待）→ 本人ログインで参加 → 開催回の登録。
func TestLoginInviteAndSessionCreation(t *testing.T) {
	s := newServer(t)
	a := s.client()
	if loc := a.oauthLogin("111111111111111111", "Aさん", "/session.html?id=x"); loc != "/session.html?id=x" {
		t.Fatalf("ログイン後の戻り先 = %q", loc)
	}
	var me struct {
		User struct {
			ID            string `json:"id"`
			DisplayName   string `json:"display_name"`
			DiscordUserID string `json:"discord_user_id"`
		} `json:"user"`
	}
	a.get("/api/me").mustStatus(t, 200).decode(t, &me)
	if me.User.DisplayName != "Aさん" || me.User.DiscordUserID != "111111111111111111" {
		t.Fatalf("me = %+v", me)
	}

	// 招待先は本人ログイン前は joined=false。表示名は仮のもの。
	res := a.send(http.MethodPost, "/api/groups", map[string]any{"name": "技術書輪読", "invitees": []map[string]string{
		{"discord_user_id": "222222222222222222", "display_name": "仮B"},
		{"discord_user_id": "333333333333333333", "display_name": "仮C"},
	}}).mustStatus(t, http.StatusCreated)
	var g apitypes.Group
	res.decode(t, &g)
	if res.header.Get("Location") != "/api/groups/"+g.ID || len(g.Members) != 3 || g.Members[0].Role != "owner" || g.Members[1].Joined {
		t.Fatalf("group = %s", dump(g))
	}
	if strings.Contains(string(res.body), "222222222222222222") {
		t.Fatal("招待先の Discord ID を返してはいけない")
	}

	// 未参加者がいる間は開催回を作れない。
	start := t0.Add(72 * time.Hour)
	if r := a.send(http.MethodPost, "/api/groups/"+g.ID+"/sessions", sessionBody(start)); r.status != 409 || r.errorCode(t) != "members_not_joined" {
		t.Fatalf("未参加者がいる: %d %s", r.status, r.body)
	}

	b := s.client()
	b.oauthLogin("222222222222222222", "B本人", "/")
	c := s.client()
	c.oauthLogin("333333333333333333", "C本人", "/")
	var got apitypes.Group
	b.get("/api/groups/"+g.ID).mustStatus(t, 200).decode(t, &got)
	if !got.Members[1].Joined || got.Members[1].DisplayName != "B本人" || got.CurrentMemberID != got.Members[1].ID {
		t.Fatalf("本人ログイン後は参加済み・本人の表示名: %s", dump(got))
	}
	var list apitypes.GroupList
	b.get("/api/groups").mustStatus(t, 200).decode(t, &list)
	if len(list.Items) != 1 || list.Items[0].Role != "member" || list.Items[0].MemberCount != 3 {
		t.Fatalf("groups = %s", dump(list))
	}

	// メンバーは開催回を作れない（403）。
	if r := b.send(http.MethodPost, "/api/groups/"+g.ID+"/sessions", sessionBody(start)); r.status != 403 || r.errorCode(t) != "forbidden" {
		t.Fatalf("メンバーの開催回登録: %d", r.status)
	}
	res = a.send(http.MethodPost, "/api/groups/"+g.ID+"/sessions", sessionBody(start)).mustStatus(t, http.StatusCreated)
	var created apitypes.SessionCreated
	res.decode(t, &created)
	if res.header.Get("Location") != "/api/sessions/"+created.Session.ID || created.Session.Status != "draft" || created.Session.Revision != 1 || created.CaseID == "" {
		t.Fatalf("session = %s", dump(created))
	}
	var sessions apitypes.SessionList
	c.get("/api/groups/"+g.ID+"/sessions").mustStatus(t, 200).decode(t, &sessions)
	if len(sessions.Items) != 1 || !sessions.Items[0].StartsAt.Equal(start) {
		t.Fatalf("sessions = %s", dump(sessions))
	}

	// ログアウトでサーバー側のセッションも無効になる。
	a.do(http.MethodPost, "/api/auth/logout", nil, map[string]string{"X-CSRF-Token": a.csrf}).mustStatus(t, http.StatusNoContent)
	if r := a.get("/api/me"); r.status != 401 || r.errorCode(t) != "unauthenticated" {
		t.Fatalf("ログアウト後: %d", r.status)
	}
}

func TestLoginRejectsOpenRedirect(t *testing.T) {
	s := newServer(t)
	if loc := s.client().oauthLogin("111111111111111111", "A", "//evil.example/path"); loc != "/" {
		t.Fatalf("外部への戻り先は / にする: %q", loc)
	}
	c := s.client()
	res := c.get("/api/auth/callback?state=forged&code=x").mustStatus(t, http.StatusSeeOther)
	if res.header.Get("Location") != "/?auth_error=invalid_state" {
		t.Fatalf("偽の state: %s", res.header.Get("Location"))
	}
}

func TestGroupAndSessionValidation(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	a := s.client().devLogin("A")

	for name, tc := range map[string]struct {
		body string
		path string
	}{
		"自分を招待":   {`{"name":"g","invitees":[{"discord_user_id":"100000000000000001","display_name":"A"}]}`, "invitees[0].discord_user_id"},
		"IDの重複":   {`{"name":"g","invitees":[{"discord_user_id":"222222222222222222","display_name":"B"},{"discord_user_id":"222222222222222222","display_name":"B"}]}`, "invitees[1].discord_user_id"},
		"IDの形式":   {`{"name":"g","invitees":[{"discord_user_id":"12345","display_name":"B"}]}`, "invitees[0].discord_user_id"},
		"招待なし":    {`{"name":"g","invitees":[]}`, "invitees"},
		"名前が長すぎる": {`{"name":"` + strings.Repeat("あ", 101) + `","invitees":[{"discord_user_id":"222222222222222222","display_name":"B"}]}`, "name"},
		"表示名が空":   {`{"name":"g","invitees":[{"discord_user_id":"222222222222222222","display_name":"  "}]}`, "invitees[0].display_name"},
	} {
		t.Run(name, func(t *testing.T) {
			r := a.send(http.MethodPost, "/api/groups", tc.body)
			if r.status != 422 || r.errorCode(t) != "validation_failed" || !strings.Contains(string(r.body), `"path":"`+tc.path+`"`) {
				t.Fatalf("%d %s", r.status, r.body)
			}
		})
	}

	start := t0.Add(72 * time.Hour).Format(time.RFC3339)
	for name, tc := range map[string]struct {
		body, code, path string
	}{
		"オフセットなし":  {`{"playbook_id":"reading","title":"t","starts_at":"2026-09-22T11:00:00","duration_minutes":60,"data":` + sessionData + `}`, "validation_failed", "starts_at"},
		"開催が近すぎる":  {`{"playbook_id":"reading","title":"t","starts_at":"` + t0.Add(time.Hour).Format(time.RFC3339) + `","duration_minutes":60,"data":` + sessionData + `}`, "validation_failed", "starts_at"},
		"持ち時間":     {`{"playbook_id":"reading","title":"t","starts_at":"` + start + `","duration_minutes":10,"data":` + sessionData + `}`, "validation_failed", "duration_minutes"},
		"未知の用途":    {`{"playbook_id":"meeting","title":"t","starts_at":"` + start + `","duration_minutes":60,"data":{}}`, "unsupported_playbook", ""},
		"用途データの検証": {`{"playbook_id":"reading","title":"t","starts_at":"` + start + `","duration_minutes":60,"data":` + strings.Replace(sessionData, `"target_section_ids": ["sec_2", "sec_3"]`, `"target_section_ids": []`, 1) + `}`, "validation_failed", "data.target_section_ids"},
	} {
		t.Run(name, func(t *testing.T) {
			r := a.send(http.MethodPost, "/api/groups/"+seed.GroupID+"/sessions", tc.body)
			if r.status != 422 || r.errorCode(t) != tc.code || (tc.path != "" && !strings.Contains(string(r.body), `"path":"`+tc.path+`"`)) {
				t.Fatalf("%d %s", r.status, r.body)
			}
		})
	}
}
