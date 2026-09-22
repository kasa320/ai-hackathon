package e2e

import (
	"net/http"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

func scheduleBody(status string, weekday int, start, end string) map[string]any {
	sched := map[string]any{"status": status, "max_duration_minutes": 0, "weekly_windows": []map[string]any{}, "date_windows": []map[string]any{}}
	if status == "provided" {
		sched["weekly_windows"] = []map[string]any{{"weekday": weekday, "start": start, "end": end}}
	}
	return map[string]any{"declined_presentation": false, "schedule": sched}
}

// HTTP経由で、参加条件の直接PUTがupdate_weekly_availability=trueで普段の空き時間を全置換すること、
// task submitでも同じ結果になることを確認する。
func TestPreparationSaveReplacesWeeklyAvailabilityOverHTTP(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	p := s.personas()
	id := seed.SessionID

	// 直接PUT：水曜（session weekday=3）20:00-22:00、update_weekly_availability=true。
	rev := p["B"].detail(id).Session.Revision
	body := map[string]any{
		"expected_revision": rev,
		"preparation": map[string]any{
			"attendance": "attending", "data": scheduleBody("provided", 3, "20:00", "22:00"),
			"update_weekly_availability": true,
		},
	}
	p["B"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", body).mustStatus(t, http.StatusAccepted)

	var got apitypes.WeeklyAvailability
	p["B"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &got)
	if len(got.Windows) != 1 || got.Windows[0].Weekday != 3 || got.Windows[0].Start != "20:00" || got.Windows[0].End != "22:00" {
		t.Fatalf("直接PUTで普段の空き時間が置換されていない: %s", dump(got))
	}
	if got.Timezone != "Asia/Tokyo" {
		t.Fatalf("timezone = %s", got.Timezone)
	}

	// task submit：日曜（session weekday=0）は週間契約の7（日曜）に変換される。同じ意味になる。
	tk := p["C"].openTask(id, "preparation")
	if tk == nil {
		t.Fatal("C に open な preparation タスクがない")
	}
	rev = p["C"].detail(id).Session.Revision
	taskBody := map[string]any{
		"decision": "submit", "expected_revision": rev,
		"preparation": map[string]any{
			"attendance": "attending", "data": scheduleBody("provided", 0, "09:00", "10:30"),
			"update_weekly_availability": true,
		},
	}
	p["C"].send(http.MethodPost, "/api/tasks/"+tk.ID+"/responses", taskBody).mustStatus(t, http.StatusAccepted)

	var gotC apitypes.WeeklyAvailability
	p["C"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &gotC)
	if len(gotC.Windows) != 1 || gotC.Windows[0].Weekday != 7 || gotC.Windows[0].Start != "09:00" || gotC.Windows[0].End != "10:30" {
		t.Fatalf("task submitで普段の空き時間が置換されていない: %s", dump(gotC))
	}

	// flagがfalse（省略）なら、週間時間帯を送っても普段の空き時間は変わらない。
	rev = p["D"].detail(id).Session.Revision
	bodyD := map[string]any{
		"expected_revision": rev,
		"preparation":       map[string]any{"attendance": "attending", "data": scheduleBody("provided", 5, "18:00", "19:00")},
	}
	p["D"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", bodyD).mustStatus(t, http.StatusAccepted)
	var gotD apitypes.WeeklyAvailability
	p["D"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &gotD)
	if len(gotD.Windows) != 0 || gotD.UpdatedAt != nil {
		t.Fatalf("flag falseなのに普段の空き時間が変わった: %s", dump(gotD))
	}

	// 有効な週間枠がないtrueは422で、何も保存されない。
	rev = p["D"].detail(id).Session.Revision
	badBody := map[string]any{
		"expected_revision": rev,
		"preparation": map[string]any{
			"attendance": "attending", "data": scheduleBody("unknown", 0, "", ""),
			"update_weekly_availability": true,
		},
	}
	if r := p["D"].send(http.MethodPut, "/api/sessions/"+id+"/preparations/me", badBody); r.status != 422 || r.errorCode(t) != "validation_failed" {
		t.Fatalf("有効な週間枠なしのtrueが受理された: %d %s", r.status, r.body)
	}
	p["D"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &gotD)
	if len(gotD.Windows) != 0 || gotD.UpdatedAt != nil {
		t.Fatalf("拒否されたのに普段の空き時間が変わった: %s", dump(gotD))
	}
}

// unknown/schedule省略でも、参加を明示していれば登録済みの普段の空き時間を候補計算に使う
// （バグ修正の統合確認）。今回だけの不可日・特別可日時と正しく合成する。
func TestUnansweredScheduleUsesStandingAvailabilityEndToEnd(t *testing.T) {
	s := newServer(t)
	seed := s.seed("initial_demo")
	p := s.personas()
	id := seed.SessionID

	// 全員が水曜20:00-22:00を普段の空き時間として登録する。
	for _, name := range []string{"A", "B", "C", "D"} {
		p[name].send(http.MethodPut, "/api/me/weekly-availability", map[string]any{
			"timezone": "Asia/Tokyo",
			"windows":  []map[string]any{{"weekday": 3, "start": "20:00", "end": "22:00"}},
		}).mustStatus(t, 200)
	}

	// A・D は明示的にschedule省略（参加のみ表明）、B・Cはstatus=unknownを明示。誰も個別の時間帯を答えない。
	p["A"].submitPreparation(id, "attending", prepData(true)).mustStatus(t, http.StatusAccepted)
	p["B"].submitPreparation(id, "attending", scheduleBody("unknown", 0, "", "")).mustStatus(t, http.StatusAccepted)
	p["C"].submitPreparation(id, "attending", scheduleBody("unknown", 0, "", "")).mustStatus(t, http.StatusAccepted)
	p["D"].submitPreparation(id, "attending", prepData(true)).mustStatus(t, http.StatusAccepted)
	s.process()

	d := p["A"].detail(id)
	if d.ActiveCase.Status != "awaiting_consent" && d.ActiveCase.Status != "confirmed" {
		t.Fatalf("普段の空き時間から案が作れない: %s", dump(d.ActiveCase))
	}
}
