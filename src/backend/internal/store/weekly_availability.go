package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// WeeklyAvailability は本人が登録した普段の空き時間。Windows は coord が検証済みの JSON。
type WeeklyAvailability struct {
	UserID    string
	Timezone  string
	Windows   []byte
	UpdatedAt time.Time
}

// WeeklyAvailability は本人の登録を返す。未登録なら ErrNotFound。
func (t *Tx) WeeklyAvailability(ctx context.Context, userID string) (WeeklyAvailability, error) {
	var a WeeklyAvailability
	var windows, updated string
	err := t.row(ctx, "SELECT user_id, timezone, windows, updated_at FROM user_weekly_availability WHERE user_id = ?", userID).
		Scan(&a.UserID, &a.Timezone, &windows, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Windows, a.UpdatedAt = []byte(windows), parseTS(updated)
	return a, nil
}

// PutWeeklyAvailability は本人の登録を全置換する。
func (t *Tx) PutWeeklyAvailability(ctx context.Context, a WeeklyAvailability) error {
	return t.exec(ctx, `INSERT INTO user_weekly_availability (user_id, timezone, windows, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET timezone = excluded.timezone, windows = excluded.windows, updated_at = excluded.updated_at`,
		a.UserID, a.Timezone, string(a.Windows), ts(a.UpdatedAt))
}

// WeeklyAvailabilityByUsers は指定した利用者のうち登録済みの人の空き時間を返す。
func (t *Tx) WeeklyAvailabilityByUsers(ctx context.Context, userIDs []string) (map[string]WeeklyAvailability, error) {
	out := map[string]WeeklyAvailability{}
	for _, id := range userIDs {
		a, err := t.WeeklyAvailability(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, nil
}
