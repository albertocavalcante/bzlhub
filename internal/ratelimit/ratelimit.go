// Package ratelimit composes a per-IP token-bucket limiter with a
// global concurrency semaphore.
//
// Two layers, two concerns:
//
//   - Per-IP token bucket: bounds *request* rate from a single client.
//     A configurable allowlist bypasses this gate entirely.
//
//   - Global semaphore: bounds *concurrent* in-flight work. Bypass
//     does NOT skip this — a privileged client can submit faster, but
//     cannot exhaust server capacity. The semaphore is the actual
//     resource defense; the per-IP limit is anti-noise.
//
// Note: the bypass list is keyed by IP. The broader safety story for
// direct-exposure deployments lives in featureflags.CheckSafeStartup,
// which refuses to boot bzlhub serve when IngestWriteEnabled=true
// without a configured BZLHUB_TRUSTED_PROXY_CIDR. Future evolution:
// once an AuthN front-proxy is mandatory (Plan 71 §C3 + Plan 75
// header-auth scaffold), swap this keying function to use user-id so
// the bypass list becomes "trusted operators" rather than "trusted
// addresses."
package ratelimit

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// IngestLimiter is the bound rate+concurrency gate for ingest writes.
// Construct via New, then call Acquire per request. Acquire returns a
// release func that callers MUST invoke once their work is done,
// regardless of outcome (use defer).
type IngestLimiter struct {
	// perMin is the per-IP cap, in requests per 60s. 0 disables
	// per-IP limiting (every request bypasses the token bucket).
	perMin int

	// bypass holds remote addresses exempt from the per-IP limiter.
	// Set membership is read-only after construction.
	bypass map[string]struct{}

	// sem is the global concurrency cap. When nil, concurrency is
	// unbounded (operator chose 0 — not recommended).
	sem chan struct{}

	mu      sync.Mutex
	buckets map[string]*bucket
}

// Options is the construction surface — kept narrow so callers don't
// build half-configured limiters.
type Options struct {
	PerMin        int
	BypassIPs     []string
	MaxConcurrent int
}

// New builds an IngestLimiter from the given options. opts.PerMin=0
// disables the per-IP gate; opts.MaxConcurrent=0 disables the global
// semaphore (in-process unbounded — fine for tests, dangerous in prod).
func New(opts Options) *IngestLimiter {
	l := &IngestLimiter{
		perMin:  opts.PerMin,
		buckets: make(map[string]*bucket),
		bypass:  make(map[string]struct{}, len(opts.BypassIPs)),
	}
	for _, ip := range opts.BypassIPs {
		l.bypass[ip] = struct{}{}
	}
	if opts.MaxConcurrent > 0 {
		l.sem = make(chan struct{}, opts.MaxConcurrent)
	}
	return l
}

// Errors returned by Acquire. They map cleanly to HTTP statuses:
// ErrRateLimited → 429, ErrCapacityExhausted → 503.
var (
	ErrRateLimited       = errors.New("ratelimit: per-IP request rate exceeded")
	ErrCapacityExhausted = errors.New("ratelimit: server at capacity, try again shortly")
)

// Acquire applies both layers. Returns (release, nil) on success;
// callers must invoke release exactly once when work completes.
//
// The semaphore is non-blocking: when full, Acquire returns
// ErrCapacityExhausted immediately rather than queueing. This is a
// deliberate choice — ingest jobs can run for tens of seconds, and a
// silently-queued request would mislead the user about progress. A
// fast 503 lets the UI surface "try again shortly" deterministically.
func (l *IngestLimiter) Acquire(ctx context.Context, remoteAddr string) (release func(), err error) {
	// Per-IP bucket first — cheaper to reject early.
	if l.perMin > 0 {
		if _, bypass := l.bypass[remoteAddr]; !bypass {
			if !l.bucketFor(remoteAddr).allow(time.Now()) {
				return nil, ErrRateLimited
			}
		}
	}

	// Global concurrency: non-blocking try-acquire.
	if l.sem != nil {
		select {
		case l.sem <- struct{}{}:
			// acquired
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, ErrCapacityExhausted
		}
		return func() { <-l.sem }, nil
	}
	return func() {}, nil
}

func (l *IngestLimiter) bucketFor(ip string) *bucket {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		b = newBucket(l.perMin)
		l.buckets[ip] = b
	}
	return b
}

// bucket is a minimal fixed-window token bucket. Capacity == refill
// rate (perMin tokens per 60s, refilling linearly). Concurrent allow
// calls are serialized via mu — contention is per-IP, not global, so
// even a flood from one client doesn't impact others.
type bucket struct {
	mu       sync.Mutex
	capacity int
	tokens   float64
	lastFill time.Time
}

func newBucket(perMin int) *bucket {
	return &bucket{
		capacity: perMin,
		tokens:   float64(perMin),
		lastFill: time.Now(),
	}
}

func (b *bucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Linear refill: perMin tokens per 60s = perMin/60 per second.
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * float64(b.capacity) / 60.0
	if b.tokens > float64(b.capacity) {
		b.tokens = float64(b.capacity)
	}
	b.lastFill = now

	if b.tokens < 1.0 {
		return false
	}
	b.tokens -= 1.0
	return true
}

// RemoteIP extracts the client IP from an http.Request.
//
// canopy runs behind cloudflared in production; the tunnel SETS
// (replaces, not appends) Cf-Connecting-IP to the real client IP, so
// that header is safe to trust when present. X-Forwarded-For is NOT
// trusted: cloudflared APPENDS to it, so its leftmost entry is whatever
// the client typed — a free spoof. Falling back to RemoteAddr keeps
// dev/test sane.
//
// If a non-Cloudflare proxy is ever added in front, swap Cf-Connecting-IP
// for that proxy's set-replace header, or implement an explicit trusted-
// proxy chain — do NOT re-enable bare XFF parsing.
func RemoteIP(r *http.Request) string {
	if cf := trim(r.Header.Get("Cf-Connecting-IP")); cf != "" {
		return cf
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trim(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
