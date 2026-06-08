package httpstore

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"
)

// Cache stores Backend response bodies plus the validators needed
// to issue a conditional GET on the next read. Implementations MUST
// be safe for concurrent use — Backend reads and writes from any
// goroutine the caller spawns.
//
// A nil Cache field on NewOptions disables caching entirely (the
// v0.0.5 behavior). When non-nil, Backend routes Read* methods
// through it transparently: cache hit triggers an If-None-Match
// request; 304 returns the stored body; 200 refreshes the entry.
//
// Implementations MUST defensive-copy Body across the boundary so
// callers can mutate without poisoning the store.
type Cache interface {
	// Get returns the cached entry for key. The bool reports whether
	// an entry exists; on false the Entry is the zero value.
	//
	// The ctx is passed through for implementations backed by remote
	// stores (Redis, BoltDB, etc.) — MemoryCache ignores it.
	Get(ctx context.Context, key string) (Entry, bool)

	// Put stores entry under key, overwriting any prior entry.
	// Implementations stamp StoredAt at Put time.
	//
	// The ctx is passed through for implementations that perform IO.
	Put(ctx context.Context, key string, entry Entry)

	// Delete removes the entry at key, if present. Idempotent —
	// deleting an absent key is a no-op, not an error.
	//
	// Backend.Write* methods invoke Delete on success so the next
	// read re-fetches the freshly-written body from upstream
	// (write-invalidate semantics). Implementations of Cache that
	// can't delete (read-only / append-only stores) MUST document
	// that limitation and operators MUST NOT pair them with a
	// Backend used for writes.
	Delete(ctx context.Context, key string)
}

// Entry is one cached response body plus the validators an upstream
// returned. The body is defensive-copied across the Cache boundary
// — mutating the Body slice the caller passed to Put or received
// from Get does not affect the stored entry.
type Entry struct {
	// Body is the response body. Zero-length slice when the upstream
	// returned an empty body (not nil — empty != absent).
	Body []byte

	// ETag is the validator from the upstream's ETag header, exactly
	// as received — wrapping quotes preserved, weak `W/` prefix
	// preserved. Empty when the upstream didn't send one (caching
	// still works, but every read re-fetches the full body since
	// conditional GET isn't possible).
	ETag string

	// LastModified is the parsed Last-Modified header. Zero when
	// absent. Reserved for cache implementations that want to
	// honour If-Modified-Since semantics; the library itself uses
	// ETag in preference.
	LastModified time.Time

	// StoredAt is the wall-clock instant the Cache stored this
	// entry. Set by Put; callers don't populate. Useful for
	// diagnostics and for cache impls that want to enforce a
	// max-age policy on top of ETag validation.
	StoredAt time.Time
}

// MemoryCache is an in-process LRU cache keyed by relative path.
// Safe for concurrent use. Construct via NewMemoryCache.
//
// MemoryCache is paired one-to-one with a Backend. Two Backends
// pointing at different upstreams MUST NOT share a MemoryCache —
// the cache key is just the relative path, with no BaseURL
// namespacing. Callers that need to share a cache across upstreams
// should wrap MemoryCache with their own key-prefixing layer.
type MemoryCache struct {
	mu         sync.Mutex
	maxEntries int
	order      *list.List               // front = most recently used
	index      map[string]*list.Element // key -> element holding *memEntry
}

// memEntry is the internal element value inside the list. We pair
// key with entry so eviction (which pops from the back of the list)
// can find the right index entry to delete.
type memEntry struct {
	key   string
	entry Entry
}

// MemoryCacheOptions configures NewMemoryCache.
type MemoryCacheOptions struct {
	// MaxEntries is the LRU cap. Must be > 0. When a Put would
	// exceed this, the least-recently-used entry is evicted.
	//
	// Sized by entry count rather than byte budget — body sizes
	// in BCR are bimodal (small JSONs vs large tarballs), and
	// large tarballs already bypass the cache via ReadBlob's
	// streaming path. If byte-cap semantics matter, wrap with a
	// custom Cache implementation.
	MaxEntries int
}

// NewMemoryCache constructs a MemoryCache. Returns ErrInvalidOptions
// when MaxEntries <= 0.
func NewMemoryCache(opts MemoryCacheOptions) (*MemoryCache, error) {
	if opts.MaxEntries <= 0 {
		return nil, fmt.Errorf("%w: MemoryCacheOptions.MaxEntries must be > 0 (got %d)",
			ErrInvalidOptions, opts.MaxEntries)
	}
	return &MemoryCache{
		maxEntries: opts.MaxEntries,
		order:      list.New(),
		index:      make(map[string]*list.Element, opts.MaxEntries),
	}, nil
}

// Get implements Cache.Get. Returns a defensive copy of the stored
// Body — mutating the returned slice does not affect the cache.
func (c *MemoryCache) Get(_ context.Context, key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[key]
	if !ok {
		return Entry{}, false
	}
	c.order.MoveToFront(el)
	stored := el.Value.(*memEntry).entry
	return cloneEntry(stored), true
}

// Delete implements Cache.Delete. Idempotent — deleting an absent
// key is a no-op.
func (c *MemoryCache) Delete(_ context.Context, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[key]; ok {
		delete(c.index, key)
		c.order.Remove(el)
	}
}

// Put implements Cache.Put. Defensive-copies entry.Body so later
// mutation by the caller does not poison the store. Stamps
// StoredAt at the time of the Put call.
func (c *MemoryCache) Put(_ context.Context, key string, entry Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry = cloneEntry(entry)
	entry.StoredAt = time.Now()
	if el, ok := c.index[key]; ok {
		el.Value.(*memEntry).entry = entry
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&memEntry{key: key, entry: entry})
	c.index[key] = el
	for c.order.Len() > c.maxEntries {
		back := c.order.Back()
		if back == nil {
			return
		}
		delete(c.index, back.Value.(*memEntry).key)
		c.order.Remove(back)
	}
}

// Len returns the current number of stored entries. Useful for
// tests and diagnostics; not part of the Cache interface.
func (c *MemoryCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// cloneEntry deep-copies Entry.Body so the cache boundary holds.
// ETag, LastModified, and StoredAt are value types — no copy needed.
func cloneEntry(in Entry) Entry {
	out := in
	if in.Body != nil {
		out.Body = make([]byte, len(in.Body))
		copy(out.Body, in.Body)
	}
	return out
}
