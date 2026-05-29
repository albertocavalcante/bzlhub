package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ---- responseCache primitive ----

func TestResponseCache_HitAndExpiry(t *testing.T) {
	c := newResponseCache(10)
	c.Put("a", []byte("hello"), 50*time.Millisecond)

	if got, ok := c.Get("a"); !ok || string(got) != "hello" {
		t.Errorf("fresh hit: got=%q ok=%v", got, ok)
	}

	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Errorf("stale entry should be evicted")
	}
	if c.Len() != 0 {
		t.Errorf("expired Get should remove the entry, len=%d", c.Len())
	}
}

func TestResponseCache_LRUEviction(t *testing.T) {
	c := newResponseCache(3)
	c.Put("a", []byte("1"), time.Minute)
	c.Put("b", []byte("2"), time.Minute)
	c.Put("c", []byte("3"), time.Minute)

	// Touching "a" promotes it to most-recently-used; "b" is now oldest.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected hit on a")
	}

	// Inserting "d" must evict "b" (oldest after the touch above).
	c.Put("d", []byte("4"), time.Minute)
	if _, ok := c.Get("b"); ok {
		t.Errorf("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Errorf("a should still be present")
	}
	if _, ok := c.Get("d"); !ok {
		t.Errorf("d should be present")
	}
}

func TestResponseCache_DisabledWhenCapacityZero(t *testing.T) {
	c := newResponseCache(0)
	if c.Enabled() {
		t.Error("capacity=0 should disable")
	}
	c.Put("a", []byte("x"), time.Minute)
	if _, ok := c.Get("a"); ok {
		t.Error("Put should be a no-op when disabled")
	}
}

func TestResponseCache_DisabledWhenCapacityNegative(t *testing.T) {
	c := newResponseCache(-1)
	if c.Enabled() {
		t.Error("negative capacity should disable")
	}
}

func TestResponseCache_ReplaceUpdatesBody(t *testing.T) {
	c := newResponseCache(10)
	c.Put("a", []byte("v1"), time.Minute)
	c.Put("a", []byte("v2"), time.Minute)
	if got, _ := c.Get("a"); string(got) != "v2" {
		t.Errorf("expected v2, got %q", got)
	}
	if c.Len() != 1 {
		t.Errorf("replace shouldn't add an entry, len=%d", c.Len())
	}
}

// Stats() must reflect the hit/miss tallies — the values surface
// through /api/v1/upstreams (Plan 16 F3 spec).
func TestResponseCache_StatsCounters(t *testing.T) {
	c := newResponseCache(10)
	c.Put("a", []byte("x"), time.Minute)

	// Hit, hit, miss
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected hit")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected hit")
	}
	if _, ok := c.Get("nope"); ok {
		t.Fatal("expected miss")
	}

	s := c.Stats()
	if s.Hits != 2 || s.Misses != 1 || s.Entries != 1 {
		t.Errorf("stats = %+v, want {Hits:2 Misses:1 Entries:1}", s)
	}
}

// A stale entry returned as miss must increment the miss counter
// (the cache served nothing useful; the operator's view is "miss").
func TestResponseCache_StaleCountsAsMiss(t *testing.T) {
	c := newResponseCache(10)
	c.Put("a", []byte("x"), 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected miss on stale entry")
	}
	s := c.Stats()
	if s.Hits != 0 || s.Misses != 1 {
		t.Errorf("stats = %+v, want stale-miss tallied", s)
	}
}

func TestCacheTTLFor(t *testing.T) {
	cases := []struct {
		path string
		want time.Duration
	}{
		{"modules/rules_go/0.50.0/source.json", 60 * time.Second},
		{"modules/rules_go/0.50.0/MODULE.bazel", 60 * time.Second},
		{"modules/rules_go/metadata.json", 30 * time.Second}, // versions grow
		{"bazel_registry.json", 60 * time.Second},
		{"modules/rules_go/0.50.0/patches/foo.patch", 60 * time.Second},
		{"modules/rules_go/0.50.0/overlay/BUILD", 60 * time.Second},
		// Unknown paths get the default 60s.
		{"weird/future/endpoint", 60 * time.Second},
	}
	for _, c := range cases {
		if got := cacheTTLFor(c.path); got != c.want {
			t.Errorf("%s: got %v, want %v", c.path, got, c.want)
		}
	}
}

// ---- Integration: cache short-circuits the HTTP path ----

// Second request for the same path within the TTL hits the cache and
// makes no upstream HTTP call. We verify by counting requests reaching
// the upstream stub.
func TestCascade_CachedHitSkipsUpstreamRequest(t *testing.T) {
	primary := newStub()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{"cached":true}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})

	// First request: primary 404 → upstream hit → 1 upstream call.
	rc, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != `{"cached":true}` {
		t.Errorf("first call body = %q", body)
	}

	// Second request: same path, same upstream → should hit the cache.
	rc2, err := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(rc2)
	rc2.Close()
	if string(body2) != `{"cached":true}` {
		t.Errorf("second call body = %q (cache should round-trip identical bytes)", body2)
	}

	got := atomic.LoadInt64(&hits)
	if got != 1 {
		t.Errorf("upstream HTTP hits = %d, want 1 (second call should have been served from cache)", got)
	}
}

// 404 from upstream is NOT cached — a transient miss shouldn't poison
// the cache for the TTL window. Future calls re-probe.
func TestCascade_NotFoundIsNotCached(t *testing.T) {
	primary := newStub()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:   primary,
		Upstreams: []*Upstream{{URL: srv.URL}},
	})
	for i := 0; i < 3; i++ {
		_, _ = c.GetSourceJSON(context.Background(), "foo", "1.0.0")
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("hits = %d, want 3 (404 must not be cached — every call re-probes)", got)
	}
}

// CacheCapacity=-1 disables the cache; every request re-fetches.
func TestCascade_DisabledCacheRefetchesEveryCall(t *testing.T) {
	primary := newStub()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewCascade(CascadeConfig{
		Primary:       primary,
		Upstreams:     []*Upstream{{URL: srv.URL}},
		CacheCapacity: -1,
	})
	for i := 0; i < 3; i++ {
		rc, _ := c.GetSourceJSON(context.Background(), "foo", "1.0.0")
		if rc != nil {
			_, _ = io.Copy(io.Discard, rc)
			rc.Close()
		}
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("hits = %d, want 3 (cache disabled)", got)
	}
}
