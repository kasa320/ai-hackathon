package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

// periodSessionBody は日時を入れず、期間だけを渡す登録本文。回の名前も省く。
func periodSessionBody(from, to string) string {
	return `{"playbook_id":"reading","period_start":"` + from + `","period_end":"` + to + `","duration_minutes":60,"data":` + sessionData + `}`
}

// 期間だけで登録すると、日時はエージェントが決める。
// 出られないと答えた日は選ばれず、決まった日時が開催回に反映される。
func TestPeriodOnlyRegistrationDecidesTheDate(t *testing.T) {
	s := newServer(t)
	// 先に本人がログインしてから招待すると、その場で参加済みになる。
	people := map[string]*client{}
	for _, name := range []string{"A", "B", "C"} {
		c := s.client()
		c.oauthLogin(persona[name], name, "/")
		people[name] = c
	}
	a := people["A"]

	res := a.send(http.MethodPost, "/api/groups", map[string]any{"name": "期間で登録する会", "invitees": []map[string]string{
		{"discord_user_id": persona["B"], "display_name": "B"},
		{"discord_user_id": persona["C"], "display_name": "C"},
	}}).mustStatus(t, http.StatusCreated)
	var g apitypes.Group
	res.decode(t, &g)

	// t0 は 2026-09-19 09:00 UTC（＝JST 18:00）。9/20〜9/30 の間で開く。
	res = a.send(http.MethodPost, "/api/groups/"+g.ID+"/sessions", periodSessionBody("2026-09-20", "2026-09-30")).
		mustStatus(t, http.StatusCreated)
	var created apitypes.SessionCreated
	res.decode(t, &created)
	id := created.Session.ID

	if created.Session.Title != "第1回" {
		t.Fatalf("名前を省いたら通し番号を振る: %q", created.Session.Title)
	}
	if created.Session.ScheduleStatus != "proposed" {
		t.Fatalf("日時は未確定のはず: %q", created.Session.ScheduleStatus)
	}
	if created.Session.PeriodStart != "2026-09-20" || created.Session.PeriodEnd != "2026-09-30" {
		t.Fatalf("期間が返っていない: %+v", created.Session)
	}

	// 全員が参加条件を出す。Bは 9/20〜9/23 に出られない。
	busy := []string{"2026-09-20", "2026-09-21", "2026-09-22", "2026-09-23"}
	prep := func(c *client, unavailable []string) {
		d := prepData(true, []string{"sec_2", "sec_3"}, []string{"sec_2", "sec_3"}, 20)
		d["unavailable_dates"] = unavailable
		c.submitPreparation(id, "attending", d).mustStatus(t, 202)
	}
	prep(a, []string{})
	prep(people["B"], busy)
	prep(people["C"], []string{})
	s.process()

	d := a.detail(id)
	if d.CurrentProposal == nil {
		t.Fatalf("案が出ていない: %s", dump(d.ActiveCase))
	}

	// 管理者の承認と、出席予定者の過半数の同意を1回で集める。
	a.respond(id, "owner_approval", "approve").mustStatus(t, 202)
	for _, name := range []string{"A", "B", "C"} {
		if tk := people[name].openTask(id, "approval"); tk != nil {
			people[name].respond(id, "approval", "approve").mustStatus(t, 202)
		}
		if tk := people[name].openTask(id, "assignment"); tk != nil {
			people[name].respond(id, "assignment", "accept").mustStatus(t, 202)
		}
	}
	s.process()

	d = a.detail(id)
	if d.Session.ScheduleStatus != "confirmed" {
		t.Fatalf("同意がそろったら日時が決まる: %s", dump(d.Session))
	}
	got := d.Session.StartsAt.In(time.FixedZone("JST", 9*3600))
	day := got.Format("2006-01-02")
	for _, ng := range busy {
		if day == ng {
			t.Fatalf("出られないと答えた日が選ばれた: %s", day)
		}
	}
	if day < "2026-09-20" || day > "2026-09-30" {
		t.Fatalf("期間の外が選ばれた: %s", day)
	}
	if !got.After(t0) {
		t.Fatalf("過去の日時が選ばれた: %s", got)
	}
}

// 期間の指定が壊れている登録は 422 で弾く。
func TestPeriodValidation(t *testing.T) {
	s := newServer(t)
	a := s.client()
	a.oauthLogin(persona["A"], "A", "/")
	b := s.client()
	b.oauthLogin(persona["B"], "B", "/")
	var g apitypes.Group
	a.send(http.MethodPost, "/api/groups", map[string]any{"name": "検証用", "invitees": []map[string]string{
		{"discord_user_id": persona["B"], "display_name": "B"},
	}}).mustStatus(t, http.StatusCreated).decode(t, &g)

	for name, tc := range map[string]struct{ from, to, path string }{
		"開始日が空":    {"", "2026-09-30", "period_start"},
		"日付の形式が違う": {"2026/09/20", "2026-09-30", "period_start"},
		"終了が開始より前": {"2026-09-30", "2026-09-20", "period_end"},
		"期間が過ぎている": {"2026-09-01", "2026-09-10", "period_end"},
		"1年より長い":   {"2026-09-20", "2027-10-01", "period_end"},
	} {
		t.Run(name, func(t *testing.T) {
			r := a.send(http.MethodPost, "/api/groups/"+g.ID+"/sessions", periodSessionBody(tc.from, tc.to))
			if r.status != 422 {
				t.Fatalf("status = %d, body = %s", r.status, r.body)
			}
			var body struct {
				Error struct {
					Details struct {
						Fields []struct {
							Path string `json:"path"`
						} `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			r.decode(t, &body)
			found := false
			for _, f := range body.Error.Details.Fields {
				if f.Path == tc.path {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s の指摘がない: %s", tc.path, r.body)
			}
		})
	}
}
