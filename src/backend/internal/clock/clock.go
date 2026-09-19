// Package clock は現在時刻の取得を抽象化する。
// 回答期限などの判定はすべてこの Clock を通し、デモでは時計を進め、テストでは時刻を固定できるようにする。
package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

// Real は実際の時刻を返す。
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Offset は実時刻に任意のずれを足した時刻を返す。デモで「24時間後」に進めるために使う。
type Offset struct {
	mu     sync.RWMutex
	base   Clock
	offset time.Duration
}

func NewOffset(base Clock) *Offset {
	return &Offset{base: base}
}

func (o *Offset) Now() time.Time {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.base.Now().Add(o.offset)
}

// Advance は時計を d だけ進める。負の値は無視する（時刻を巻き戻さない）。
func (o *Offset) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.offset += d
}

// Fixed はテスト用に固定した時刻を返す。
type Fixed struct {
	T time.Time
}

func (f Fixed) Now() time.Time { return f.T }
