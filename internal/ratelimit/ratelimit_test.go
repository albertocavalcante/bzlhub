package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestAcquire_PerIPLimitTriggers(t *testing.T) {
	l := New(Options{PerMin: 3, MaxConcurrent: 0})
	for i := range 3 {
		release, err := l.Acquire(context.Background(), "10.0.0.1")
		if err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
		release()
	}
	_, err := l.Acquire(context.Background(), "10.0.0.1")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("4th Acquire err = %v, want ErrRateLimited", err)
	}
}

func TestAcquire_PerIPLimitIsPerKey(t *testing.T) {
	l := New(Options{PerMin: 1, MaxConcurrent: 0})
	r1, err := l.Acquire(context.Background(), "10.0.0.1")
	if err != nil {
		t.Fatalf("ip1 first: %v", err)
	}
	r1()
	// Different IP gets its own bucket — should still succeed.
	r2, err := l.Acquire(context.Background(), "10.0.0.2")
	if err != nil {
		t.Fatalf("ip2 first: %v", err)
	}
	r2()
	// ip1 second call hits its own depleted bucket.
	if _, err := l.Acquire(context.Background(), "10.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("ip1 second err = %v, want ErrRateLimited", err)
	}
}

func TestAcquire_BypassSkipsPerIPLimit(t *testing.T) {
	l := New(Options{PerMin: 1, BypassIPs: []string{"10.0.0.1"}})
	// 50 calls from bypass IP should all succeed.
	for i := range 50 {
		release, err := l.Acquire(context.Background(), "10.0.0.1")
		if err != nil {
			t.Fatalf("bypass call #%d: %v", i, err)
		}
		release()
	}
	// Non-bypass IP at the same limit gets cut off after 1.
	r, err := l.Acquire(context.Background(), "10.0.0.2")
	if err != nil {
		t.Fatalf("non-bypass first: %v", err)
	}
	r()
	if _, err := l.Acquire(context.Background(), "10.0.0.2"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("non-bypass second err = %v, want ErrRateLimited", err)
	}
}

func TestAcquire_SemaphoreBoundsConcurrency(t *testing.T) {
	l := New(Options{PerMin: 0, MaxConcurrent: 2})
	r1, err := l.Acquire(context.Background(), "10.0.0.1")
	if err != nil {
		t.Fatalf("#1: %v", err)
	}
	r2, err := l.Acquire(context.Background(), "10.0.0.2")
	if err != nil {
		t.Fatalf("#2: %v", err)
	}
	// Third call (any IP) must hit ErrCapacityExhausted.
	if _, err := l.Acquire(context.Background(), "10.0.0.3"); !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("#3 err = %v, want ErrCapacityExhausted", err)
	}
	// Releasing one slot frees capacity for the next caller.
	r1()
	r3, err := l.Acquire(context.Background(), "10.0.0.3")
	if err != nil {
		t.Fatalf("#3 after release: %v", err)
	}
	r3()
	r2()
}

func TestAcquire_BypassDoesNotSkipSemaphore(t *testing.T) {
	// Critical safety property: a bypassed IP must still respect the
	// global concurrency cap. Otherwise "trusted" becomes "can DoS."
	l := New(Options{PerMin: 0, BypassIPs: []string{"10.0.0.1"}, MaxConcurrent: 1})
	r1, err := l.Acquire(context.Background(), "10.0.0.1")
	if err != nil {
		t.Fatalf("#1: %v", err)
	}
	if _, err := l.Acquire(context.Background(), "10.0.0.1"); !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("#2 err = %v, want ErrCapacityExhausted even for bypass IP", err)
	}
	r1()
}

func TestAcquire_RaceSafe(t *testing.T) {
	// Smoke test for race detector: concurrent Acquire/release across
	// many IPs should never panic or trip the race detector.
	l := New(Options{PerMin: 10000, MaxConcurrent: 50})
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip := "10.0.0." + itoa(i%20)
			release, err := l.Acquire(context.Background(), ip)
			if err == nil {
				release()
			}
		}(i)
	}
	wg.Wait()
}

func TestRemoteIP_PrefersCfConnectingIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:55555"
	r.Header.Set("Cf-Connecting-IP", "203.0.113.5")
	if got := RemoteIP(r); got != "203.0.113.5" {
		t.Errorf("RemoteIP = %q, want 203.0.113.5 (Cf-Connecting-IP)", got)
	}
}

func TestRemoteIP_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:55555"
	if got := RemoteIP(r); got != "127.0.0.1" {
		t.Errorf("RemoteIP = %q, want 127.0.0.1 (host portion)", got)
	}
}

// XFF is deliberately not trusted — cloudflared appends to it, so the
// leftmost entry is attacker-controlled. Setting XFF alone must fall
// through to RemoteAddr.
func TestRemoteIP_IgnoresXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:55555"
	r.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := RemoteIP(r); got != "127.0.0.1" {
		t.Errorf("RemoteIP = %q, want 127.0.0.1 (XFF must be ignored)", got)
	}
}

// Cf-Connecting-IP wins over both XFF and RemoteAddr — Cloudflare sets
// (replaces) this header, so it's the trustworthy source.
func TestRemoteIP_CfBeatsXFF(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "127.0.0.1:55555"
	r.Header.Set("Cf-Connecting-IP", "203.0.113.5")
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
	if got := RemoteIP(r); got != "203.0.113.5" {
		t.Errorf("RemoteIP = %q, want 203.0.113.5 (Cf-Connecting-IP wins)", got)
	}
}

// itoa is a tiny strconv.Itoa replacement so the test file pulls no
// extra deps. Keeps the race-test self-contained.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b strings.Builder
	for n > 0 {
		d := n % 10
		n /= 10
		b.WriteByte(byte('0' + d))
	}
	// reverse
	s := b.String()
	out := make([]byte, len(s))
	for i := range s {
		out[len(s)-1-i] = s[i]
	}
	return string(out)
}
