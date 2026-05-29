// Package forgetest provides shared test helpers for forge consumers
// — most importantly a manual Clock that lets retry/backoff code be
// exercised in microseconds instead of real seconds.
//
// This package is intended for use in _test.go files; nothing here
// should land in a production binary.
package bigornatest

import (
	"context"
	"sync"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// Compile-time check that ManualClock satisfies bigorna.Clock.
var _ bigorna.Clock = (*ManualClock)(nil)

// ManualClock is a bigorna.Clock implementation backed by an explicit
// "now" value the test can advance. Sleep returns immediately if the
// scheduled wake time has already passed; otherwise it blocks on
// Advance / SkipSleeps / ctx cancellation.
//
// Typical usage:
//
//	clk := bigornatest.NewManualClock(time.Unix(0, 0))
//	// inject clk into the forge Client...
//	// trigger an operation that sleeps...
//	clk.Advance(time.Hour) // unblock any pending Sleep
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []sleeper
	skipAll bool
}

type sleeper struct {
	until  time.Time
	signal chan struct{}
}

// NewManualClock returns a ManualClock initialized to the given time.
func NewManualClock(start time.Time) *ManualClock {
	return &ManualClock{now: start}
}

// Now reports the current manual time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep blocks until c.Now() >= start+d, ctx is canceled, or
// SkipSleeps is enabled.
func (c *ManualClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	if c.skipAll || d <= 0 {
		c.mu.Unlock()
		return ctx.Err()
	}
	target := c.now.Add(d)
	s := sleeper{until: target, signal: make(chan struct{})}
	c.sleeps = append(c.sleeps, s)
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.signal:
		return nil
	}
}

// Advance moves time forward by d and wakes any sleeper whose target
// is now reached.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	remaining := c.sleeps[:0]
	for _, s := range c.sleeps {
		if !c.now.Before(s.until) {
			close(s.signal)
			continue
		}
		remaining = append(remaining, s)
	}
	c.sleeps = remaining
	c.mu.Unlock()
}

// SkipSleeps configures the clock to return from Sleep immediately
// (with nil error if ctx is live). Useful for retry tests that don't
// care about timing, only about iteration count.
func (c *ManualClock) SkipSleeps() {
	c.mu.Lock()
	c.skipAll = true
	for _, s := range c.sleeps {
		close(s.signal)
	}
	c.sleeps = nil
	c.mu.Unlock()
}
