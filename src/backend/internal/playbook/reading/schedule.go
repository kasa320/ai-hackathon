package reading

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// 日付は利用者の生活時間で扱う。API の日時は UTC だが、「何月何日か」は JST で判断する。
const dateLayout = "2006-01-02"

var jst = time.FixedZone("JST", 9*60*60)

var _ coord.PlanScheduler = Playbook{}

// PlannedStart は案が決めた開催日時を返す。日時を含まない案では ok=false。
func (Playbook) PlannedStart(raw json.RawMessage) (time.Time, bool, error) {
	p, err := decodePlan(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	if p.StartsAt == "" {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339, p.StartsAt)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading: 案の開催日時を読めません: %w", err)
	}
	return at.UTC(), true, nil
}

// validateDates は「出られない日」の形式を検証し、重複を除いて日付順に整える。
func validateDates(v *coord.ValidationError, path string, dates []string) []string {
	if len(dates) > maxUnavailableDays {
		v.Add(path, "出られない日は%d件までにしてください", maxUnavailableDays)
		return dates
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(dates))
	for i, d := range dates {
		if _, err := time.ParseInLocation(dateLayout, d, jst); err != nil {
			v.Add(fmt.Sprintf("%s[%d]", path, i), "YYYY-MM-DD で指定してください")
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// validateSchedule は案が決める開催日時を検証する。
//
// 日時がまだ決まっていない回（ScheduleStatus=proposed）では、案は期間内の日時を1つ決める。
// 開催回の全員が明示した時間帯・最大参加時間を満たす必要がある。
// 未回答・欠席を対象から除くことはできず、日時の確定には別途全員の同意が必要。
func validateSchedule(v *coord.ValidationError, s coord.Snapshot, p PlanData) {
	if len([]rune(p.Frequency)) > maxFrequencyLen {
		v.Add("frequency", "進め方の説明は%d文字までにしてください", maxFrequencyLen)
	}
	if s.ScheduleStatus != coord.ScheduleProposed {
		// 日時が決まっている回では動かせない。同じ日時をそのまま書くのは許す。
		if p.StartsAt != "" {
			at, err := time.Parse(time.RFC3339, p.StartsAt)
			if err != nil || !at.Equal(s.StartsAt) {
				v.Add("starts_at", "この回の開催日時は決まっています。変更するには登録し直してください")
			}
		}
		// A period-based session retains its all-member policy after confirmation.
		if s.PeriodStart != "" && !scheduleAllows(s, s.StartsAt) {
			v.Add("starts_at", "確定日時で全員の参加条件を満たせなくなりました。日時の再調整が必要です")
		}
		return
	}

	if p.StartsAt == "" {
		v.Add("starts_at", "この回は日時が決まっていません。期間内の開催日時を決めてください")
		return
	}
	at, err := time.Parse(time.RFC3339, p.StartsAt)
	if err != nil {
		v.Add("starts_at", "タイムゾーン付きの日時（RFC 3339）で指定してください")
		return
	}
	if !at.After(s.Now.Add(coord.SessionMinLead)) {
		v.Add("starts_at", "回答を集める時間が残るよう、いまから1時間より先の日時にしてください")
	}
	from, errFrom := time.ParseInLocation(dateLayout, s.PeriodStart, jst)
	to, errTo := time.ParseInLocation(dateLayout, s.PeriodEnd, jst)
	if errFrom == nil && errTo == nil {
		end := to.Add(24*time.Hour - time.Second)
		if at.Before(from) || at.After(end) {
			v.Add("starts_at", "%s〜%s の範囲で決めてください", s.PeriodStart, s.PeriodEnd)
		}
	}
	day := at.In(jst).Format(dateLayout)
	if busy := unavailableOn(s, day); len(busy) > 0 {
		v.Add("starts_at", "%s は出られないと答えた人がいます（%s）", day, memberNames(s, busy))
	}
	if !scheduleAllows(s, at) {
		v.Add("starts_at", "開催回の全員が回答した参加可能時間と最大参加時間を満たしていません")
	}
	if rejectedStart(s, at) {
		v.Add("starts_at", "この案件で否決された日時です。別の候補を選んでください")
	}
}

// unavailableOn はその日に出られないと答えた出席予定者を返す。
func unavailableOn(s coord.Snapshot, day string) []string {
	var out []string
	for _, id := range s.Attending() {
		_, pd := preparationData(s, id)
		for _, d := range pd.UnavailableDates {
			if d == day {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// memberNames は表示名を「、」でつないで返す。分からない ID はそのまま出す。
func memberNames(s coord.Snapshot, ids []string) string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		for _, m := range s.Members {
			if m.ID == id {
				name = m.DisplayName
				break
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}

// candidateDays は全員の共通時間から、開始候補を30分刻みで近い順に返す。
// 仮の判断処理（AGENT_MODE=fake）と、AI へ渡す候補の提示に使う。
func candidateDays(s coord.Snapshot, limit int) []time.Time {
	from, err := time.ParseInLocation(dateLayout, s.PeriodStart, jst)
	if err != nil {
		return nil
	}
	to, err := time.ParseInLocation(dateLayout, s.PeriodEnd, jst)
	if err != nil {
		return nil
	}
	var out []time.Time
	for day := from; !day.After(to) && len(out) < limit; day = day.AddDate(0, 0, 1) {
		for _, w := range commonWindows(s, day) {
			for minute := w.start; minute+s.DurationMinutes <= w.end && len(out) < limit; minute += 30 {
				at := day.Add(time.Duration(minute) * time.Minute)
				if at.After(s.Now.Add(coord.SessionMinLead)) && !rejectedStart(s, at) {
					out = append(out, at)
				}
			}
		}
	}
	return out
}

func rejectedStart(s coord.Snapshot, at time.Time) bool {
	for _, rejected := range s.Case.RejectedStartsAt {
		if rejected.Equal(at) {
			return true
		}
	}
	return false
}

// buildSchedule は AI へ渡す日程の判断材料を組み立てる。
// 全員が明示した条件を満たす候補だけを渡す。
func buildSchedule(s coord.Snapshot) aiSchedule {
	out := aiSchedule{Status: s.ScheduleStatus}
	if out.Status == "" {
		out.Status = coord.ScheduleConfirmed
	}
	if out.Status != coord.ScheduleProposed {
		return out
	}
	out.PeriodStart, out.PeriodEnd = s.PeriodStart, s.PeriodEnd
	out.Today = s.Now.In(jst).Format(dateLayout)
	for _, at := range candidateDays(s, 10) {
		out.Candidates = append(out.Candidates, at.Format(time.RFC3339))
	}
	return out
}

// describeFrequency は期間と1回の長さから、進め方の1行を作る（仮の判断処理用）。
// 週1回を前提にした概算で、確定するのはこの1回ぶんだけ。
func describeFrequency(s coord.Snapshot) string {
	from, errFrom := time.ParseInLocation(dateLayout, s.PeriodStart, jst)
	to, errTo := time.ParseInLocation(dateLayout, s.PeriodEnd, jst)
	if errFrom != nil || errTo != nil {
		return ""
	}
	weeks := int(to.Sub(from).Hours()/(24*7)) + 1
	if weeks < 1 {
		weeks = 1
	}
	return fmt.Sprintf("週1回%d分・全%d回の見込み（確定するのは次の1回だけ）", s.DurationMinutes, weeks)
}

// weekdayJA は曜日の日本語表記。Go の書式に曜日の和名がないため自前で持つ。
var weekdayJA = [...]string{"日", "月", "火", "水", "木", "金", "土"}

// formatDay は「9/27(日) 19:00」の形にする。表示は JST。
func formatDay(at time.Time) string {
	t := at.In(jst)
	return fmt.Sprintf("%d/%d(%s) %02d:%02d", int(t.Month()), t.Day(), weekdayJA[int(t.Weekday())], t.Hour(), t.Minute())
}
