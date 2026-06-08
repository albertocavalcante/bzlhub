package ratelimit

import (
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
