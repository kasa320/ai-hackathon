package reading

import (
	"encoding/json"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

var _ coord.WeeklyAvailabilityExtractor = Playbook{}

// sessionWeekdayToWeekly converts this playbook's session-day weekday numbering
// (0=日曜〜6=土曜) to the weekly-availability contract's numbering (1=月曜〜7=日曜).
func sessionWeekdayToWeekly(day int) int {
	if day == 0 {
		return 7
	}
	return day
}

// ExtractWeeklyAvailability reads the explicit weekly windows out of a validated
// Preparation.Data payload. It returns ok=false when the person did not explicitly state a
// weekly schedule in this submission (schedule omitted, unknown/unavailable, or only
// this-session date exceptions were given) — in those cases nothing should replace the
// user's standing weekly availability.
func (Playbook) ExtractWeeklyAvailability(data json.RawMessage) ([]apitypes.WeeklyWindow, bool, error) {
	var d PreparationData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, false, err
	}
	if d.Schedule == nil || d.Schedule.Status != "provided" || len(d.Schedule.WeeklyWindows) == 0 {
		return nil, false, nil
	}
	out := make([]apitypes.WeeklyWindow, 0, len(d.Schedule.WeeklyWindows))
	for _, w := range d.Schedule.WeeklyWindows {
		out = append(out, apitypes.WeeklyWindow{
			Weekday: sessionWeekdayToWeekly(w.Weekday),
			Start:   w.Start,
			End:     w.End,
		})
	}
	return out, true, nil
}
