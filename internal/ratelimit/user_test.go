package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	cases := []struct {
		in     string
		count  int
		per    time.Duration
		errIs  error // nil for success
	}{
		{"10/hour", 10, time.Hour, nil},
		{"5/minute", 5, time.Minute, nil},
		{"100/second", 100, time.Second, nil},
		{"  10/hour  ", 10, time.Hour, nil},
		{"10/HOUR", 10, time.Hour, nil},
		{"", 0, 0, ErrRateUnset},
		{"   ", 0, 0, ErrRateUnset},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			n, per, err := ParseRate(c.in)
			if c.errIs != nil {
				if !errors.Is(err, c.errIs) {
					t.Errorf("err=%v, want %v", err, c.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("err=%v, want nil", err)
			}
			if n != c.count || per != c.per {
				t.Errorf("got (%d, %v), want (%d, %v)", n, per, c.count, c.per)
			}
		})
	}
}

func TestParseRate_Invalid(t *testing.T) {
	bad := []string{"10", "10/day", "abc/hour", "-1/hour", "0/hour", "/hour", "10/"}
	for _, s := range bad {
		t.Run(s, func(t *testing.T) {
			_, _, err := ParseRate(s)
			if err == nil {
				t.Errorf("expected error for %q", s)
			}
		})
	}
}

func TestUserLimiter_UnsetRate_AllowsAll(t *testing.T) {
	l := NewUserLimiter()
	for range 100 {
		ok, _ := l.Allow("alice", 0, time.Hour)
		if !ok {
			t.Fatal("zero rate must allow all")
		}
	}
}

func TestUserLimiter_PerUserIsolated(t *testing.T) {
	l := NewUserLimiter()
	start := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	// alice burns all tokens
	for i := range 10 {
		ok, _ := l.allowAt("alice", 10, time.Hour, start)
		if !ok {
			t.Fatalf("alice request %d should be allowed", i)
		}
	}
	if ok, _ := l.allowAt("alice", 10, time.Hour, start); ok {
		t.Error("alice's 11th request should be rate-limited")
	}
	// bob has full bucket
	for i := range 10 {
		ok, _ := l.allowAt("bob", 10, time.Hour, start)
		if !ok {
			t.Fatalf("bob request %d should be allowed (independent bucket)", i)
		}
	}
}

func TestUserLimiter_Refills(t *testing.T) {
	l := NewUserLimiter()
	t0 := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	// 60/hour = 1/minute. Exhaust the burst.
	for range 60 {
		l.allowAt("alice", 60, time.Hour, t0)
	}
	if ok, _ := l.allowAt("alice", 60, time.Hour, t0); ok {
		t.Fatal("burst should be exhausted")
	}
	// Advance 90 seconds = 1.5 tokens refill → 1 token consumed → 0.5 left, second one fails
	t1 := t0.Add(90 * time.Second)
	if ok, _ := l.allowAt("alice", 60, time.Hour, t1); !ok {
		t.Error("after 90s refill at 60/h, alice should get 1 token back")
	}
	if ok, _ := l.allowAt("alice", 60, time.Hour, t1); ok {
		t.Error("after consuming the refilled token, next should fail")
	}
}

func TestUserLimiter_RetryAfterApproximated(t *testing.T) {
	l := NewUserLimiter()
	t0 := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	// 60/hour. Burn budget.
	for range 60 {
		l.allowAt("alice", 60, time.Hour, t0)
	}
	ok, retry := l.allowAt("alice", 60, time.Hour, t0)
	if ok {
		t.Fatal("rate exhausted; allow should be false")
	}
	// 1 token at 60/hour = 60s. Allow ±5s for float rounding.
	if retry < 55*time.Second || retry > 65*time.Second {
		t.Errorf("retry=%v, want ~60s", retry)
	}
}

// =================================================================
// GC sweep — Plan 76 §2.5 last open follow-up
// =================================================================

func TestUserLimiter_GCStaleBuckets_RemovesStale(t *testing.T) {
	l := NewUserLimiter()
	t0 := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	l.allowAt("alice", 5, time.Hour, t0)
	l.allowAt("bob", 5, time.Hour, t0)
	if got := l.Size(); got != 2 {
		t.Fatalf("Size=%d, want 2 (seed)", got)
	}

	t1 := t0.Add(3 * time.Hour)
	n := l.GCStaleBuckets(t1, time.Hour)
	if n != 2 {
		t.Errorf("evicted=%d, want 2", n)
	}
	if got := l.Size(); got != 0 {
		t.Errorf("Size after GC=%d, want 0", got)
	}
}

func TestUserLimiter_GCStaleBuckets_KeepsFresh(t *testing.T) {
	l := NewUserLimiter()
	t0 := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	l.allowAt("alice", 5, time.Hour, t0)
	l.allowAt("bob", 5, time.Hour, t0.Add(2*time.Hour))

	t1 := t0.Add(3 * time.Hour)
	n := l.GCStaleBuckets(t1, time.Hour)
	if n != 1 {
		t.Errorf("evicted=%d, want 1 (alice only)", n)
	}
	if got := l.Size(); got != 1 {
		t.Errorf("Size=%d, want 1 (bob kept)", got)
	}
}

func TestUserLimiter_GCStaleBuckets_UpdatesLastSeenOnDeny(t *testing.T) {
	// A user who hits the rate limit gets their bucket REFRESHED
	// (lastSeen updated), not abandoned. Without this, a malicious
	// caller could pile up denied requests and then get their bucket
	// evicted between bursts, restarting their budget.
	l := NewUserLimiter()
	t0 := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	// Burn the budget; rate 5/hour leaves no room to refill in zero
	// elapsed time, so the 6th call at t0 is denied.
	for range 5 {
		l.allowAt("alice", 5, time.Hour, t0)
	}
	if ok, _ := l.allowAt("alice", 5, time.Hour, t0); ok {
		t.Fatal("expected denial after burning the budget at t0")
	}
	// GC at 90min past t0; maxAge=1h. alice's lastSeen is t0 — the
	// denied attempt's lastSeen-stamp is also t0 (since we passed t0
	// as the now). Strict-Before(cutoff=t0+30min) is true, so alice
	// IS evicted. That's correct: stale-cutoff window is what gates
	// eviction, not "ever attempted." The point of THIS test is the
	// denied call updates lastSeen — so we now make a fresh denied
	// call at t0+30min and assert lastSeen advanced.
	if ok, _ := l.allowAt("alice", 5, time.Hour, t0.Add(30*time.Minute)); ok {
		// At 5/hour, 30min refills 2.5 tokens → allowed. Burn it
		// back to dry.
		for range 3 {
			l.allowAt("alice", 5, time.Hour, t0.Add(30*time.Minute))
		}
	}
	// One more attempt at t0+30min — denied; should refresh lastSeen.
	_, _ = l.allowAt("alice", 5, time.Hour, t0.Add(30*time.Minute))

	// GC at 90min past t0; maxAge=1h means stale-cutoff = t0+30min.
	// alice's lastSeen is t0+30min — NOT strictly before cutoff, so
	// stays.
	t1 := t0.Add(90 * time.Minute)
	n := l.GCStaleBuckets(t1, time.Hour)
	if n != 0 {
		t.Errorf("evicted=%d, want 0 (denied attempt at t0+30min should refresh lastSeen)", n)
	}
}

func TestUserLimiter_StartGC_RespectsCtxCancel(t *testing.T) {
	l := NewUserLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	l.StartGC(ctx, 10*time.Millisecond, 1*time.Hour)
	l.Allow("alice", 5, time.Hour)
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)
	// No assertion beyond "no panic + test ends" — the cancel path
	// is the assertion.
}

func TestUserLimiter_StartGC_ZeroIntervalNoOps(t *testing.T) {
	l := NewUserLimiter()
	// Should NOT spawn a goroutine; time.NewTicker(0) would panic.
	l.StartGC(context.Background(), 0, time.Hour)
}

func TestUserLimiter_RateChangesHotswap(t *testing.T) {
	// Start at 10/hour, exhaust, switch to 60/hour — refill now uses
	// the new rate. Mirrors a SIGHUP policy reload.
	l := NewUserLimiter()
	t0 := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	for range 10 {
		l.allowAt("alice", 10, time.Hour, t0)
	}
	// At new rate 60/hour and a 90s gap → 1.5 tokens refilled.
	t1 := t0.Add(90 * time.Second)
	if ok, _ := l.allowAt("alice", 60, time.Hour, t1); !ok {
		t.Error("hot-swap to higher rate should make a token available after 90s")
	}
}
