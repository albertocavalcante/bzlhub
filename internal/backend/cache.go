package backend

import (
	"container/list"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// responseCache is the federation response cache (Plan 16 Layer C).
// In-process LRU keyed by "<upstream-url>:<path>" — multi-upstream
// setups don't share cache lines, important for metadata.json where
// two upstreams may legitimately have different versions lists.
//
// Cache stores only successful (HTTP 200) responses. Bodies are
// buffered as []byte at insertion; readers are issued via
// io.NopCloser(bytes.NewReader(...)) at lookup so each consumer gets
// an independent ReadCloser over the shared bytes.
//
// Concurrency: a single mutex protects both the map and the LRU
// list. The list operations (MoveToFront / Remove / PushFront) are
// O(1) so contention stays bounded even at 1000+ entries.
type responseCache struct {
	capacity int

	mu      sync.Mutex
	entries map[string]*list.Element // key → element holding *cacheEntry
	order   *list.List               // front = newest, back = oldest

	// Counters surface via /api/v1/upstreams' cache_stats field
	// (Plan 16 F3 spec). Atomic so reads don't contend with the
	// mu-held Get/Put critical sections. "hit" = fresh entry found;
	// "miss" = either no entry or stale entry. Stale evictions are
	// counted as misses (the operator's view: "was it served from
	// cache?" → no, even if there was an entry once).
	hits   atomic.Int64
	misses atomic.Int64
}

// CacheStats is the read-only snapshot of a responseCache's state
// surfaced via /api/v1/upstreams. Exported so the api package can
// embed it without importing internal/backend's unexported types.
type CacheStats struct {
	Entries int   `json:"entries"`
	Hits    int64 `json:"hits"`
	Misses  int64 `json:"misses"`
}

// cacheEntry is one LRU node. Expires is the strict TTL deadline;
// a hit AFTER the deadline returns miss and the entry is evicted.
type cacheEntry struct {
	key     string
	body    []byte
	expires time.Time
}

// newResponseCache constructs an empty cache. capacity ≤ 0 disables
// caching (every Get returns miss; every Put is a no-op).
func newResponseCache(capacity int) *responseCache {
	return &responseCache{
		capacity: capacity,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Enabled reports whether the cache will store anything. False when
// capacity ≤ 0 — useful for skipping the body-buffering work on the
// hot path.
func (c *responseCache) Enabled() bool { return c.capacity > 0 }

// Get returns the cached body for key if present + unexpired.
// Returns (nil, false) on miss. On a stale hit, the entry is evicted
// before returning false so the next Put doesn't pay the eviction
// cost.
//
// Touching a hit moves it to the front of the LRU list. TTL is NOT
// extended (strict expiry, per Plan 16 design).
func (c *responseCache) Get(key string) ([]byte, bool) {
	if c.capacity <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	ent := el.Value.(*cacheEntry)
	if time.Now().After(ent.expires) {
		// Stale → evict. Counts as a miss from the operator's view —
		// the request didn't get served from cache.
		c.order.Remove(el)
		delete(c.entries, key)
		c.misses.Add(1)
		return nil, false
	}
	c.order.MoveToFront(el)
	c.hits.Add(1)
	return ent.body, true
}

// Put inserts or replaces an entry. If insertion would push the
// cache past capacity, the least-recently-used entry is evicted
// first.
func (c *responseCache) Put(key string, body []byte, ttl time.Duration) {
	if c.capacity <= 0 || ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		// Replace existing entry. Move-to-front + overwrite body.
		ent := el.Value.(*cacheEntry)
		ent.body = body
		ent.expires = time.Now().Add(ttl)
		c.order.MoveToFront(el)
		return
	}
	// Evict LRU if at capacity.
	for c.order.Len() >= c.capacity {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.order.Remove(back)
		delete(c.entries, back.Value.(*cacheEntry).key)
	}
	ent := &cacheEntry{
		key:     key,
		body:    body,
		expires: time.Now().Add(ttl),
	}
	c.entries[key] = c.order.PushFront(ent)
}

// Len returns the current entry count. Useful for tests + future
// metrics endpoint exposure.
func (c *responseCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Stats returns a point-in-time snapshot. Entries is read under the
// mutex; hits + misses are atomic loads. The triple is not
// instantaneously consistent (entries could have been added/removed
// between the mutex release and the atomic loads), but the skew is
// tiny and the operator-facing use case ("approximately how busy is
// this cache?") tolerates it.
func (c *responseCache) Stats() CacheStats {
	c.mu.Lock()
	entries := c.order.Len()
	c.mu.Unlock()
	return CacheStats{
		Entries: entries,
		Hits:    c.hits.Load(),
		Misses:  c.misses.Load(),
	}
}

// cacheTTLFor returns the cache TTL for a given path. Plan 16 spec:
// source.json + MODULE.bazel + bazel_registry.json + patches/* +
// overlay/* are 60s (immutable in practice). metadata.json is 30s
// (versions list grows over time so a slightly fresher view is
// worth the modest cache-hit rate hit).
//
// Defaults to 60s for unrecognized paths so future BCR-shape
// endpoints aren't silently uncached.
func cacheTTLFor(relPath string) time.Duration {
	if strings.HasSuffix(relPath, "/metadata.json") {
		return 30 * time.Second
	}
	return 60 * time.Second
}
