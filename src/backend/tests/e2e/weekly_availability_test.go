package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

func TestWeeklyAvailabilityRequiresLoginAndIsPerUser(t *testing.T) {
	s := newServer(t)
	s.seed("initial_demo")
	people := s.personas()

	if r := s.client().get("/api/me/weekly-availability"); r.status != 401 {
		t.Fatalf("未ログインのGET = %d %s", r.status, r.body)
	}
	if r := s.client().do(http.MethodPut, "/api/me/weekly-availability", jsonBody(map[string]any{"timezone": "Asia/Tokyo", "windows": []any{}}), nil); r.status != 401 {
		t.Fatalf("未ログインのPUT = %d %s", r.status, r.body)
	}
	// CSRF トークンなしの更新は拒否する。
	if r := people["A"].do(http.MethodPut, "/api/me/weekly-availability", jsonBody(map[string]any{"timezone": "Asia/Tokyo", "windows": []any{}}), nil); r.status != 403 {
		t.Fatalf("CSRFなしのPUT = %d %s", r.status, r.body)
	}

	// 未登録は空の区間。updated_at は null（時刻を偽らない）。
	var got apitypes.WeeklyAvailability
	people["A"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &got)
	if got.Timezone != "Asia/Tokyo" || len(got.Windows) != 0 || got.UpdatedAt != nil {
		t.Fatalf("未登録 = %s", dump(got))
	}

	body := map[string]any{"timezone": "Asia/Tokyo", "windows": []map[string]any{
		{"weekday": 3, "start": "19:00", "end": "22:00"},
		{"weekday": 1, "start": "20:00", "end": "24:00"},
		{"weekday": 1, "start": "09:00", "end": "10:00"},
	}}
	s.clk.Advance(time.Hour)
	people["A"].send(http.MethodPut, "/api/me/weekly-availability", body).mustStatus(t, 200).decode(t, &got)
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(t0.Add(time.Hour)) || len(got.Windows) != 3 || got.Windows[0].Weekday != 1 || got.Windows[0].Start != "09:00" || got.Windows[2].Weekday != 3 {
		t.Fatalf("保存結果 = %s", dump(got))
	}

	// 別の人の設定は見えず、本人だけのものになる。
	var other apitypes.WeeklyAvailability
	people["B"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &other)
	if len(other.Windows) != 0 || other.UpdatedAt != nil {
		t.Fatalf("他人の空き時間が見える: %s", dump(other))
	}
	var again apitypes.WeeklyAvailability
	people["A"].get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &again)
	if len(again.Windows) != 3 {
		t.Fatalf("再取得 = %s", dump(again))
	}

	// 全置換：空にできる。
	s.clk.Advance(time.Hour)
	people["A"].send(http.MethodPut, "/api/me/weekly-availability", map[string]any{"timezone": "Asia/Tokyo", "windows": []any{}}).mustStatus(t, 200).decode(t, &got)
	if len(got.Windows) != 0 || got.UpdatedAt == nil || !got.UpdatedAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("空への置換 = %s", dump(got))
	}
}

func TestWeeklyAvailabilityValidation(t *testing.T) {
	s := newServer(t)
	s.seed("initial_demo")
	a := s.client().devLogin("A")
	win := func(day int, start, end string) map[string]any { return map[string]any{"weekday": day, "start": start, "end": end} }
	ok := func(ws ...map[string]any) map[string]any {
		return map[string]any{"timezone": "Asia/Tokyo", "windows": ws}
	}
	for name, tc := range map[string]struct {
		body any
		code string
		path string
	}{
		"区間の重複":       {ok(win(1, "19:00", "21:00"), win(1, "20:00", "22:00")), "validation_failed", "windows[1]"},
		"包含も重複":       {ok(win(2, "09:00", "18:00"), win(2, "10:00", "11:00")), "validation_failed", "windows[1]"},
		"開始と終了が同じ":    {ok(win(1, "19:00", "19:00")), "validation_failed", "windows[0]"},
		"開始が終了より後":    {ok(win(1, "22:00", "19:00")), "validation_failed", "windows[0]"},
		"曜日が0":        {ok(win(0, "19:00", "20:00")), "validation_failed", "windows[0].weekday"},
		"曜日が8":        {ok(win(8, "19:00", "20:00")), "validation_failed", "windows[0].weekday"},
		"時刻の形式":       {ok(win(1, "7:00", "20:00")), "validation_failed", "windows[0].start"},
		"存在しない時刻":     {ok(win(1, "19:00", "25:00")), "validation_failed", "windows[0].end"},
		"開始に24:00":    {ok(win(1, "24:00", "24:00")), "validation_failed", "windows[0].start"},
		"分が不正":        {ok(win(1, "19:60", "20:00")), "validation_failed", "windows[0].start"},
		"未知のタイムゾーン":   {map[string]any{"timezone": "Mars/Base", "windows": []any{}}, "validation_failed", "timezone"},
		"環境依存のタイムゾーン": {map[string]any{"timezone": "Local", "windows": []any{}}, "validation_failed", "timezone"},
		"タイムゾーンなし":    {map[string]any{"windows": []any{}}, "validation_failed", "timezone"},
		"windowsなし":   {map[string]any{"timezone": "Asia/Tokyo"}, "validation_failed", "windows"},
		"windowsがnull": {`{"timezone":"Asia/Tokyo","windows":null}`, "validation_failed", "windows"},
		"updated_atは入力不可": {map[string]any{"timezone": "Asia/Tokyo", "windows": []any{}, "updated_at": "2026-09-19T00:00:00Z"}, "invalid_json", ""},
		"未知の項目":       {map[string]any{"timezone": "Asia/Tokyo", "windows": []any{}, "user_id": "usr_x"}, "invalid_json", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := a.send(http.MethodPut, "/api/me/weekly-availability", tc.body)
			if r.errorCode(t) != tc.code || (tc.path != "" && !strings.Contains(string(r.body), `"path":"`+tc.path+`"`)) {
				t.Fatalf("%d %s", r.status, r.body)
			}
		})
	}
	// 拒否された入力は保存されない。
	var got apitypes.WeeklyAvailability
	a.get("/api/me/weekly-availability").mustStatus(t, 200).decode(t, &got)
	if len(got.Windows) != 0 || got.UpdatedAt != nil {
		t.Fatalf("不正な入力が保存された: %s", dump(got))
	}
	// 隣り合う区間は重複ではない。24:00 終了と別タイムゾーンは受け付ける。
	a.send(http.MethodPut, "/api/me/weekly-availability", map[string]any{"timezone": "America/New_York", "windows": []map[string]any{win(7, "19:00", "20:00"), win(7, "20:00", "24:00")}}).mustStatus(t, 200)
}
