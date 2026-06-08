package ratelimit

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UserLimiter is a per-user token-bucket limiter. Each user gets one
// bucket; tokens refill linearly at the rate the caller supplies on
// each Allow call. This lets the rate be hot-swapped (e.g., via the
// SIGHUP-driven policy reload) without rebuilding the limiter — the
// next Allow call uses the new rate immediately.
//
// Stale buckets accumulate in memory; for v0.1 the growth is bounded
// by the active reviewer pool (a few hundred at most). When the user
// population grows large enough to matter, add a periodic GC sweep.
//
// Safe for concurrent use after construction.
type UserLimiter struct {
	mu      sync.Mutex
	buckets map[string]*userBucket
}

// NewUserLimiter constructs an empty UserLimiter. The rate is passed
// per-call to Allow rather than at construction, so SIGHUP reloads
// flow through immediately.
func NewUserLimiter() *UserLimiter {
	return &UserLimiter{buckets: make(map[string]*userBucket)}
}

// Allow consumes one token for user at rate count/per. Returns
// (true, 0) when permitted; (false, retryAfter) when the bucket is
// empty, where retryAfter is the time to wait before a token will
// be available again.
//
// A zero or negative rate (count<=0 OR per<=0) means "no limit" —
// returns (true, 0) unconditionally without touching the bucket.
func (l *UserLimiter) Allow(user string, count int, per time.Duration) (bool, time.Duration) {
	return l.allowAt(user, count, per, time.Now())
}

// allowAt is the time-injected variant for tests.
func (l *UserLimiter) allowAt(user string, count int, per time.Duration, now time.Time) (bool, time.Duration) {
	if count <= 0 || per <= 0 {
		return true, 0
	}
	l.mu.Lock()
	b, ok := l.buckets[user]
	if !ok {
		b = &userBucket{tokens: float64(count), lastFill: now}
		l.buckets[user] = b
	}
	l.mu.Unlock()
	return b.consume(now, count, per)
}

// userBucket holds one user's token-bucket state. Locked with its
// own mutex so two requests from the same user serialize but
// different users don't contend.
type userBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastFill time.Time
}

func (b *userBucket) consume(now time.Time, count int, per time.Duration) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Linear refill at count tokens per `per` duration.
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * float64(count) / per.Seconds()
		if b.tokens > float64(count) {
			b.tokens = float64(count)
		}
		b.lastFill = now
	}
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}
	// Time until tokens hits 1.0.
	deficit := 1.0 - b.tokens
	secondsToOne := deficit * per.Seconds() / float64(count)
	return false, time.Duration(secondsToOne * float64(time.Second))
}

// ParseRate decodes the policy.yml rate-limit syntax "N/unit" where
// unit is "second", "minute", or "hour" (singular only; bare numbers
// reject). Returns (count, per, nil) on success or an explanatory
// error.
//
// Examples:
//
//	"10/hour"    → (10, time.Hour, nil)
//	"5/minute"   → (5, time.Minute, nil)
//	"100/second" → (100, time.Second, nil)
//	""           → (0, 0, ErrRateUnset)  -- distinct so handlers can treat as no-op
func ParseRate(s string) (int, time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, 0, ErrRateUnset
	}
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("ratelimit: %q: want N/unit form", s)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("ratelimit: %q: count not an integer: %w", s, err)
	}
	if n <= 0 {
		return 0, 0, fmt.Errorf("ratelimit: %q: count must be positive", s)
	}
	var per time.Duration
	switch strings.ToLower(strings.TrimSpace(parts[1])) {
	case "second":
		per = time.Second
	case "minute":
		per = time.Minute
	case "hour":
		per = time.Hour
	default:
		return 0, 0, fmt.Errorf("ratelimit: %q: unit must be second|minute|hour", s)
	}
	return n, per, nil
}

// ErrRateUnset signals an empty rate string. Handlers treat this as
// "no rate limit configured" and skip the limiter — distinct from a
// parse error, which should be logged.
var ErrRateUnset = fmt.Errorf("ratelimit: rate unset")
