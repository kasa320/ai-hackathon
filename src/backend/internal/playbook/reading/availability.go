package reading

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"github.com/kasa320/ai-hackathon/src/backend/internal/jsonx"
)

const SlotSchedule = "schedule"
const SlotUnavailable = "unavailable_dates"

func (a *ScheduleAvailability) UnmarshalJSON(raw []byte) error {
	if err := jsonx.Require(raw, "status", "weekly_windows", "date_windows", "max_duration_minutes"); err != nil {
		return err
	}
	type plain ScheduleAvailability
	var out plain
	if err := jsonx.Decode(raw, &out); err != nil {
		return err
	}
	var fields struct {
		Weekly []json.RawMessage `json:"weekly_windows"`
		Dates  []json.RawMessage `json:"date_windows"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, w := range fields.Weekly {
		if err := jsonx.Require(w, "weekday", "start", "end"); err != nil {
			return err
		}
	}
	for _, w := range fields.Dates {
		if err := jsonx.Require(w, "date", "start", "end"); err != nil {
			return err
		}
	}
	*a = ScheduleAvailability(out)
	return nil
}

func clockMinute(s string, end bool) (int, bool) {
	if end && s == "24:00" {
		return 1440, true
	}
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	t, err := time.Parse("15:04", s)
	if err != nil || t.Format("15:04") != s {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func validateAvailability(v *coord.ValidationError, a *ScheduleAvailability) {
	if a == nil {
		return
	}
	if a.WeeklyWindows == nil {
		a.WeeklyWindows = []WeeklyWindow{}
	}
	if a.DateWindows == nil {
		a.DateWindows = []DateWindow{}
	}
	if len(a.WeeklyWindows) > 60 || len(a.DateWindows) > 60 {
		v.Add(SlotSchedule, "時間帯は各60件までです")
	}
	switch a.Status {
	case "provided":
		if len(a.WeeklyWindows)+len(a.DateWindows) == 0 {
			v.Add(SlotSchedule, "参加できる時間帯を1つ以上指定してください")
		}
		if a.MaxDurationMinutes < 1 || a.MaxDurationMinutes > 480 {
			v.Add(SlotSchedule, "最大参加時間は1〜480分です")
		}
	case "unknown", "unavailable":
		if len(a.WeeklyWindows)+len(a.DateWindows) != 0 || a.MaxDurationMinutes != 0 {
			v.Add(SlotSchedule, "未定・参加不可の場合は時間帯を空、最大参加時間を0にしてください")
		}
	default:
		v.Add(SlotSchedule, "予定はprovided・unknown・unavailableのいずれかです")
	}
	check := func(start, end string) {
		a, okA := clockMinute(start, false)
		b, okB := clockMinute(end, true)
		if !okA || !okB || a >= b {
			v.Add(SlotSchedule, "時間帯は同日内のHH:mmで、開始より後に終了してください")
		}
	}
	for _, w := range a.WeeklyWindows {
		if w.Weekday < 0 || w.Weekday > 6 {
			v.Add(SlotSchedule, "曜日は日曜0〜土曜6で指定してください")
		}
		check(w.Start, w.End)
	}
	for _, w := range a.DateWindows {
		if d, err := time.Parse(dateLayout, w.Date); err != nil || d.Format(dateLayout) != w.Date {
			v.Add(SlotSchedule, "日付はYYYY-MM-DDで指定してください")
		}
		check(w.Start, w.End)
	}
}

type minuteWindow struct{ start, end int }

func availableWindows(d PreparationData, day time.Time, duration int) []minuteWindow {
	a := d.Schedule
	if a == nil || a.Status != "provided" || a.MaxDurationMinutes < duration {
		return nil
	}
	date := day.Format(dateLayout)
	for _, busy := range d.UnavailableDates {
		if busy == date {
			return nil
		}
	}
	var ws []minuteWindow
	add := func(start, end string) {
		x, okX := clockMinute(start, false)
		y, okY := clockMinute(end, true)
		if okX && okY && x < y {
			ws = append(ws, minuteWindow{x, y})
		}
	}
	for _, w := range a.DateWindows {
		if w.Date == date {
			add(w.Start, w.End)
		}
	}
	if len(ws) == 0 {
		for _, w := range a.WeeklyWindows {
			if w.Weekday == int(day.Weekday()) {
				add(w.Start, w.End)
			}
		}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].start < ws[j].start })
	var merged []minuteWindow
	for _, w := range ws {
		if len(merged) > 0 && w.start <= merged[len(merged)-1].end {
			merged[len(merged)-1].end = max(w.end, merged[len(merged)-1].end)
		} else {
			merged = append(merged, w)
		}
	}
	return merged
}

// commonWindows includes every fixed session member, including absent/unanswered members.
func commonWindows(s coord.Snapshot, day time.Time) []minuteWindow {
	if len(s.Members) == 0 {
		return nil
	}
	common := []minuteWindow{{0, 1440}}
	for _, m := range s.Members {
		p, d := preparationData(s, m.ID)
		if p == nil || p.Attendance != coord.AttendanceAttending {
			return nil
		}
		var next []minuteWindow
		for _, a := range common {
			for _, b := range availableWindows(d, day, s.DurationMinutes) {
				w := minuteWindow{max(a.start, b.start), min(a.end, b.end)}
				if w.end-w.start >= s.DurationMinutes {
					next = append(next, w)
				}
			}
		}
		common = next
	}
	return common
}

func scheduleAllows(s coord.Snapshot, at time.Time) bool {
	at = at.In(jst)
	if at.Second() != 0 || at.Nanosecond() != 0 {
		return false
	}
	start := at.Hour()*60 + at.Minute()
	for _, w := range commonWindows(s, at) {
		if start >= w.start && start+s.DurationMinutes <= w.end {
			return true
		}
	}
	return false
}

func scheduleLines(a *ScheduleAvailability) []string {
	if a == nil || a.Status == "unknown" {
		return []string{"参加できる時間帯：未定"}
	}
	if a.Status == "unavailable" {
		return []string{"参加できる時間帯：期間内は参加できません"}
	}
	out := []string{fmt.Sprintf("最大参加時間：%d分（JST）", a.MaxDurationMinutes)}
	for _, w := range a.WeeklyWindows {
		out = append(out, fmt.Sprintf("毎週%s曜 %s〜%s", weekdayJA[w.Weekday], w.Start, w.End))
	}
	for _, w := range a.DateWindows {
		out = append(out, fmt.Sprintf("%s %s〜%s（この日の週間設定を置換）", w.Date, w.Start, w.End))
	}
	return out
}

// Fake mode accepts an explicit, documented notation; arbitrary prose stays unclear.
func parseScheduleText(text string) (*ScheduleAvailability, bool) {
	t := strings.TrimSpace(text)
	if t == "未定" {
		return &ScheduleAvailability{Status: "unknown"}, true
	}
	if t == "期間内は参加不可" {
		return &ScheduleAvailability{Status: "unavailable"}, true
	}
	// Example: 水 20:00-22:00 60分 / 2026-10-07 21:00-22:00 60分
	parts := strings.Fields(t)
	if len(parts) != 3 {
		return nil, false
	}
	span := strings.Split(parts[1], "-")
	if len(span) != 2 {
		return nil, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(parts[2], "分"))
	if err != nil {
		return nil, false
	}
	a := &ScheduleAvailability{Status: "provided", MaxDurationMinutes: n}
	for day, label := range weekdayJA {
		if parts[0] == label || parts[0] == label+"曜" || parts[0] == label+"曜日" {
			a.WeeklyWindows = []WeeklyWindow{{day, span[0], span[1]}}
		}
	}
	if len(a.WeeklyWindows) == 0 {
		a.DateWindows = []DateWindow{{parts[0], span[0], span[1]}}
	}
	v := &coord.ValidationError{}
	validateAvailability(v, a)
	return a, v.Err() == nil
}
