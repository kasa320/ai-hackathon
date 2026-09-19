package clock

import (
	"testing"
	"time"
)

func TestOffsetAdvance(t *testing.T) {
	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	c := NewOffset(Fixed{T: base})

	c.Advance(24 * time.Hour)
	if got, want := c.Now(), base.Add(24*time.Hour); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}

	c.Advance(-time.Hour)
	if got, want := c.Now(), base.Add(24*time.Hour); !got.Equal(want) {
		t.Fatalf("負の Advance で時刻が変わった: Now() = %v, want %v", got, want)
	}
}
