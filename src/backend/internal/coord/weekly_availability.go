package coord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // 実行環境に tz データがなくても週間空き時間のタイムゾーンを検証できるようにする

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
	"github.com/kasa320/ai-hackathon/src/backend/internal/apperr"
	"github.com/kasa320/ai-hackathon/src/backend/internal/store"
)

// 週間空き時間の未登録時の既定と上限。
const (
	DefaultAvailabilityZone = "Asia/Tokyo"
	maxWeeklyWindows        = 70
)

// parseClock は "HH:MM" を分に直す。end のときだけ "24:00" を許す。
func parseClock(s string, end bool) (int, bool) {
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

// validateWeeklyAvailability は入力を検証し、曜日・開始時刻順に整えた区間を返す。
func validateWeeklyAvailability(in apitypes.PutWeeklyAvailabilityInput) (string, []apitypes.WeeklyWindow, error) {
	var fields []apperr.Field
	zone := strings.TrimSpace(in.Timezone)
	// "Local" などの環境依存の名前は受け付けず、IANA 名（Area/City）または UTC だけを許す。
	if zone == "" || (zone != "UTC" && !strings.Contains(zone, "/")) {
		fields = append(fields, apperr.Field{Path: "timezone", Message: "Asia/Tokyo のような IANA タイムゾーン名を指定してください"})
	} else if _, err := time.LoadLocation(zone); err != nil {
		fields = append(fields, apperr.Field{Path: "timezone", Message: "未知のタイムゾーンです"})
	}
	if in.Windows == nil {
		fields = append(fields, apperr.Field{Path: "windows", Message: "必須です（空にする場合は空の配列を指定してください）"})
		return zone, nil, apperr.Validation(fields...)
	}
	windows := append([]apitypes.WeeklyWindow{}, *in.Windows...)
	if len(windows) > maxWeeklyWindows {
		fields = append(fields, apperr.Field{Path: "windows", Message: fmt.Sprintf("区間は%d件までです", maxWeeklyWindows)})
		return zone, nil, apperr.Validation(fields...)
	}
	type span struct{ start, end, index int }
	byDay := map[int][]span{}
	for i, w := range windows {
		p := fmt.Sprintf("windows[%d]", i)
		start, okS := parseClock(w.Start, false)
		end, okE := parseClock(w.End, true)
		if w.Weekday < 1 || w.Weekday > 7 {
			fields = append(fields, apperr.Field{Path: p + ".weekday", Message: "1（月曜）〜7（日曜）で指定してください"})
		}
		if !okS {
			fields = append(fields, apperr.Field{Path: p + ".start", Message: "00:00〜23:59 の HH:MM で指定してください"})
		}
		if !okE {
			fields = append(fields, apperr.Field{Path: p + ".end", Message: "00:01〜24:00 の HH:MM で指定してください"})
		}
		if okS && okE {
			if start >= end {
				fields = append(fields, apperr.Field{Path: p, Message: "終了は開始より後にしてください"})
			} else if w.Weekday >= 1 && w.Weekday <= 7 {
				byDay[w.Weekday] = append(byDay[w.Weekday], span{start, end, i})
			}
		}
	}
	for _, spans := range byDay {
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		for i := 1; i < len(spans); i++ {
			if spans[i].start < spans[i-1].end {
				fields = append(fields, apperr.Field{Path: fmt.Sprintf("windows[%d]", spans[i].index), Message: "同じ曜日の別の区間と重なっています"})
			}
		}
	}
	if len(fields) > 0 {
		return zone, nil, apperr.Validation(fields...)
	}
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].Weekday != windows[j].Weekday {
			return windows[i].Weekday < windows[j].Weekday
		}
		return windows[i].Start < windows[j].Start
	})
	return zone, windows, nil
}

func weeklyView(a store.WeeklyAvailability) (apitypes.WeeklyAvailability, error) {
	out := apitypes.WeeklyAvailability{Timezone: a.Timezone, Windows: []apitypes.WeeklyWindow{}}
	if err := json.Unmarshal(a.Windows, &out.Windows); err != nil {
		return out, err
	}
	if out.Windows == nil {
		out.Windows = []apitypes.WeeklyWindow{}
	}
	t := a.UpdatedAt
	out.UpdatedAt = &t
	return out, nil
}

// WeeklyAvailability は本人の普段の空き時間を返す。未登録なら空の区間と updated_at=null を返す。
func (c *Coordinator) WeeklyAvailability(ctx context.Context, userID string) (apitypes.WeeklyAvailability, error) {
	out := apitypes.WeeklyAvailability{Timezone: DefaultAvailabilityZone, Windows: []apitypes.WeeklyWindow{}}
	err := c.st.Tx(ctx, func(tx *store.Tx) error {
		a, err := tx.WeeklyAvailability(ctx, userID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = weeklyView(a)
		return err
	})
	return out, err
}

// PutWeeklyAvailability は本人の普段の空き時間を置き換える。対象は常に認証済みの本人で、
// 何度送っても同じ結果になる（更新日時だけが新しくなる）。
func (c *Coordinator) PutWeeklyAvailability(ctx context.Context, userID string, in apitypes.PutWeeklyAvailabilityInput) (apitypes.WeeklyAvailability, error) {
	zone, windows, err := validateWeeklyAvailability(in)
	if err != nil {
		return apitypes.WeeklyAvailability{}, err
	}
	raw, _ := json.Marshal(windows)
	a := store.WeeklyAvailability{UserID: userID, Timezone: zone, Windows: raw, UpdatedAt: c.now()}
	err = c.st.Tx(ctx, func(tx *store.Tx) error {
		if _, err := tx.User(ctx, userID); err != nil {
			return err
		}
		return tx.PutWeeklyAvailability(ctx, a)
	})
	if err != nil {
		return apitypes.WeeklyAvailability{}, err
	}
	return weeklyView(a)
}
