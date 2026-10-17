// Package clock is the injected time source. The engine and the daemon never
// call time.Now directly, so tests drive time by hand.
package clock

import (
	"sync"
	"time"
)

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake is a clock that only moves when told to.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake starts at t (or a fixed instant if t is zero).
func NewFake(t time.Time) *Fake {
	if t.IsZero() {
		t = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	}
	return &Fake{t: t}
}

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Advance moves the clock forward.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}
