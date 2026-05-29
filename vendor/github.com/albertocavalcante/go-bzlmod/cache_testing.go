package gobzlmod

import (
	"context"
	"errors"
	"sync"
)

// Compile-time interface compliance checks
var _ ModuleCache = NoopCache{}
var _ ModuleCache = (*MemoryCache)(nil)
var _ ModuleCache = (*FailingCache)(nil)

// NoopCache is a cache that discards all writes and always returns cache misses.
// Useful for testing without caching overhead.
type NoopCache struct{}

// Get always returns a cache miss.
func (NoopCache) Get(ctx context.Context, name, version string) ([]byte, bool, error) {
	return nil, false, nil
}

// Put discards the content and returns success.
func (NoopCache) Put(ctx context.Context, name, version string, content []byte) error {
	return nil
}

// MemoryCache is a thread-safe in-memory cache for testing.
type MemoryCache struct {
	mu    sync.RWMutex
	items map[string][]byte
}

// NewMemoryCache creates a new in-memory cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		items: make(map[string][]byte),
	}
}

// Get retrieves a cached MODULE.bazel file.
func (c *MemoryCache) Get(ctx context.Context, name, version string) ([]byte, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := name + "@" + version
	content, ok := c.items[key]
	if !ok {
		return nil, false, nil
	}
	// Return a copy to prevent mutation
	result := make([]byte, len(content))
	copy(result, content)
	return result, true, nil
}

// Put stores a MODULE.bazel file in the cache.
func (c *MemoryCache) Put(ctx context.Context, name, version string, content []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := name + "@" + version
	// Store a copy to prevent mutation
	stored := make([]byte, len(content))
	copy(stored, content)
	c.items[key] = stored
	return nil
}

// Clear removes all entries from the cache.
func (c *MemoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string][]byte)
}

// Len returns the number of cached entries.
func (c *MemoryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// FailingCache is a cache that always returns errors.
// Useful for testing error handling paths.
type FailingCache struct {
	GetErr error
	PutErr error
}

// NewFailingCache creates a cache that fails with the given errors.
func NewFailingCache(getErr, putErr error) *FailingCache {
	if getErr == nil {
		getErr = errors.New("cache get failed")
	}
	if putErr == nil {
		putErr = errors.New("cache put failed")
	}
	return &FailingCache{GetErr: getErr, PutErr: putErr}
}

// Get always returns an error.
func (c *FailingCache) Get(ctx context.Context, name, version string) ([]byte, bool, error) {
	return nil, false, c.GetErr
}

// Put always returns an error.
func (c *FailingCache) Put(ctx context.Context, name, version string, content []byte) error {
	return c.PutErr
}
