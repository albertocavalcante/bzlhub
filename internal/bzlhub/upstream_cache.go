package bzlhub

import (
	"context"
	"encoding/json"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/bzlhub/internal/fetch"
)

// upstreamCache memoises bcrmirror.MetadataAt + JSON decode per
// module across one drift pass. The cache short-circuits both the
// "module exists upstream" and "module is local-only" outcomes so a
// repeat module name (every version of it) costs one map lookup.
//
// Not goroutine-safe — callers (Backfill, Refresh) iterate rows
// serially. Parallelising the loop is a tempting perf knob but
// would require either guarding the map with a mutex here or
// switching to sync.Map; pick one before parallelising.
type upstreamCache struct {
	mirror *bcrmirror.Mirror
	hits   map[string]upstreamCacheEntry
}

type upstreamCacheEntry struct {
	meta *fetch.MetadataJSON
	err  error
}

func newUpstreamCache(m *bcrmirror.Mirror) *upstreamCache {
	return &upstreamCache{mirror: m, hits: map[string]upstreamCacheEntry{}}
}

func (c *upstreamCache) lookup(ctx context.Context, module string) (*fetch.MetadataJSON, error) {
	if hit, ok := c.hits[module]; ok {
		return hit.meta, hit.err
	}
	raw, err := c.mirror.MetadataAt(ctx, module, "HEAD")
	if err != nil {
		c.hits[module] = upstreamCacheEntry{err: err}
		return nil, err
	}
	var meta fetch.MetadataJSON
	if jerr := json.Unmarshal(raw, &meta); jerr != nil {
		c.hits[module] = upstreamCacheEntry{err: jerr}
		return nil, jerr
	}
	c.hits[module] = upstreamCacheEntry{meta: &meta}
	return &meta, nil
}
