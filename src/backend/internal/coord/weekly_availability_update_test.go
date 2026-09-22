package coord_test

import (
	"encoding/json"
	"testing"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// scheduleData builds validated reading.PreparationData JSON for participation-save tests.
// weekday is session-day numbering (0=日曜〜6=土曜), matching the reading playbook contract.
func scheduleData(status string, weekday int, start, end string) json.RawMessage {
	m := map[string]any{"declined_presentation": false}
	switch status {
	case "provided":
		m["schedule"] = map[string]any{
			"status": "provided", "max_duration_minutes": 0,
			"weekly_windows": []map[string]any{{"weekday": weekday, "start": start, "end": end}},
			"date_windows":   []map[string]any{},
		}
	case "unknown", "unavailable":
		m["schedule"] = map[string]any{"status": status, "max_duration_minutes": 0, "weekly_windows": []map[string]any{}, "date_windows": []map[string]any{}}
	}
	b, _ := json.Marshal(m)
	return b
}

func TestExplicitWeeklyChangeReplacesStandingAvailabilityInSameTransaction(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()

	// 水曜（session weekday=3）20:00-22:00 を明示的に入力し、update_weekly_availability=true で保存する。
	rev := h.detail("B").Session.Revision
	data := scheduleData("provided", 3, "20:00", "22:00")
	res, err := h.c.PutPreparation(ctx, h.users["B"], h.sess, apitypes.PutPreparationInput{
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: "attending", Data: data, UpdateWeeklyAvailability: true},
	}, nil)
	if err != nil {
		t.Fatalf("weekly更新付きの保存: %v (%s)", err, res.Body)
	}
	got, err := h.c.WeeklyAvailability(ctx, h.users["B"])
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != coord.DefaultAvailabilityZone {
		t.Fatalf("timezone = %s", got.Timezone)
	}
	if len(got.Windows) != 1 || got.Windows[0].Weekday != 3 || got.Windows[0].Start != "20:00" || got.Windows[0].End != "22:00" {
		t.Fatalf("週間の空き時間が置換されていない: %+v", got.Windows)
	}
	if got.UpdatedAt == nil {
		t.Fatal("updated_at が設定されていない")
	}
}

// 日曜（session weekday=0）は weekly 契約では 7（日曜）に変換される。
func TestSessionWeekdayToWeeklyConversion(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	rev := h.detail("C").Session.Revision
	data := scheduleData("provided", 0, "09:00", "10:00")
	if _, err := h.c.PutPreparation(ctx, h.users["C"], h.sess, apitypes.PutPreparationInput{
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: "attending", Data: data, UpdateWeeklyAvailability: true},
	}, nil); err != nil {
		t.Fatalf("保存: %v", err)
	}
	got, err := h.c.WeeklyAvailability(ctx, h.users["C"])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Weekday != 7 {
		t.Fatalf("曜日変換が正しくない: %+v", got.Windows)
	}
}

func TestWeeklyFlagFalseDoesNotUpdateStanding(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	rev := h.detail("B").Session.Revision
	data := scheduleData("provided", 3, "20:00", "22:00")
	if _, err := h.c.PutPreparation(ctx, h.users["B"], h.sess, apitypes.PutPreparationInput{
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: "attending", Data: data}, // flag omitted = false
	}, nil); err != nil {
		t.Fatalf("保存: %v", err)
	}
	got, err := h.c.WeeklyAvailability(ctx, h.users["B"])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 0 || got.UpdatedAt != nil {
		t.Fatalf("flag falseなのに普段の空き時間が更新された: %+v", got)
	}
}

func TestWeeklyFlagTrueRequiresValidWeeklyWindows(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	cases := map[string]json.RawMessage{
		"unknown status":      scheduleData("unknown", 0, "", ""),
		"only date exception": mustPrepDateOnly(),
		"no schedule at all":  prepData(false),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			rev := h.detail("D").Session.Revision
			_, err := h.c.PutPreparation(ctx, h.users["D"], h.sess, apitypes.PutPreparationInput{
				ExpectedRevision: &rev,
				Preparation:      &apitypes.Preparation{Attendance: "attending", Data: data, UpdateWeeklyAvailability: true},
			}, nil)
			if err == nil {
				t.Fatal("有効な週間枠がないのに受理された")
			}
			if got, gerr := h.c.WeeklyAvailability(ctx, h.users["D"]); gerr != nil || len(got.Windows) != 0 {
				t.Fatalf("拒否されたのに普段の空き時間が変わった: %+v %v", got, gerr)
			}
		})
	}
}

func mustPrepDateOnly() json.RawMessage {
	m := map[string]any{
		"declined_presentation": false,
		"schedule": map[string]any{
			"status": "provided", "max_duration_minutes": 0,
			"weekly_windows": []map[string]any{},
			"date_windows":   []map[string]any{{"date": "2026-10-07", "start": "10:00", "end": "11:00"}},
		},
	}
	b, _ := json.Marshal(m)
	return b
}

// タスクの decision=submit と直接 PUT は同じ意味になる。
func TestTaskSubmitReplacesStandingAvailabilitySameAsDirectPut(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	tk := h.openTask("B", "preparation")
	if tk == nil {
		t.Fatal("B に open な preparation タスクがない")
	}
	rev := h.detail("B").Session.Revision
	data := scheduleData("provided", 5, "18:00", "19:30")
	if _, err := h.c.RespondTask(ctx, h.users["B"], tk.ID, apitypes.TaskResponseInput{
		Decision:         "submit",
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: "attending", Data: data, UpdateWeeklyAvailability: true},
	}, nil); err != nil {
		t.Fatalf("task submit: %v", err)
	}
	got, err := h.c.WeeklyAvailability(ctx, h.users["B"])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Weekday != 5 || got.Windows[0].Start != "18:00" || got.Windows[0].End != "19:30" {
		t.Fatalf("task submitで週間の空き時間が置換されていない: %+v", got.Windows)
	}
}

// 今回だけの例外を変えただけ、既存の普段の空き時間を表示しただけでは更新しない。
func TestSessionExceptionOnlyDoesNotTouchStandingAvailability(t *testing.T) {
	h := newHarness(t, nil)
	h.createSession()
	rev := h.detail("C").Session.Revision
	data := scheduleData("provided", 3, "20:00", "22:00")
	if _, err := h.c.PutPreparation(ctx, h.users["C"], h.sess, apitypes.PutPreparationInput{
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: "attending", Data: data, UpdateWeeklyAvailability: true},
	}, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := h.c.WeeklyAvailability(ctx, h.users["C"])

	// 今回だけの例外（unavailable_dates）だけを変え、flagは送らない。
	var d map[string]any
	_ = json.Unmarshal(data, &d)
	d["unavailable_dates"] = []string{"2026-10-07"}
	raw, _ := json.Marshal(d)
	rev = h.detail("C").Session.Revision
	if _, err := h.c.PutPreparation(ctx, h.users["C"], h.sess, apitypes.PutPreparationInput{
		ExpectedRevision: &rev,
		Preparation:      &apitypes.Preparation{Attendance: "attending", Data: raw},
	}, nil); err != nil {
		t.Fatal(err)
	}
	after, err := h.c.WeeklyAvailability(ctx, h.users["C"])
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt == nil || before.UpdatedAt == nil || !after.UpdatedAt.Equal(*before.UpdatedAt) {
		t.Fatalf("今回だけの例外変更だけで普段の空き時間が更新された: before=%+v after=%+v", before, after)
	}
}
