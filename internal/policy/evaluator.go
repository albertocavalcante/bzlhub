package policy

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/auth"
)

// MaintainerSource is the read side of module_maintainers the
// Evaluator consults for the per-target `maintainer` gate. Real
// impl: *store.Store. Tests use a stub.
//
// Errors from IsMaintainer cause AllowFor to DENY (no fail-open).
// The error is NOT cached so transient failures recover on the
// next call.
type MaintainerSource interface {
	IsMaintainer(ctx context.Context, module, userEmail string) (bool, error)
}

// EvaluatorOptions controls the cache. Zero values pick sensible
// defaults — 60s TTL, 256 entries.
type EvaluatorOptions struct {
	CacheTTL time.Duration
	CacheMax int
}

const (
	defaultEvaluatorCacheTTL = 60 * time.Second
	defaultEvaluatorCacheMax = 256
)

// Evaluator wraps a *Policy with the per-target lookup machinery
// the `maintainer` gate needs. Handlers needing target-aware
// gates use Evaluator.AllowFor; for global gates Policy.Allow (or
// the Evaluator.Allow shortcut) is sufficient.
//
// Cache: bounded LRU per (user_email, module) tuple with TTL.
// Caches both positive AND negative verdicts (denying repeated
// non-maintainer probes is the common case). Error verdicts are
// NOT cached.
//
// Cache invalidation: grant/revoke flows call InvalidateMaintainer
// to purge the tuple. Policy hot-reload doesn't invalidate (the
// per-tuple verdict is independent of policy.yml — what changes is
// which actions consult the cache).
type Evaluator struct {
	policy *Policy
	source MaintainerSource

	cacheTTL time.Duration
	cacheMax int

	mu    sync.Mutex
	list  *list.List               // *cacheEntry (front=newest)
	index map[cacheKey]*list.Element
}

type cacheKey struct {
	email  string
	module string
}

type cacheEntry struct {
	key     cacheKey
	verdict bool
	expires time.Time
}

// NewEvaluator constructs an Evaluator. p must be non-nil; source
// may be nil (then AllowFor for maintainer gates always denies).
func NewEvaluator(p *Policy, source MaintainerSource, opts EvaluatorOptions) *Evaluator {
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = defaultEvaluatorCacheTTL
	}
	if opts.CacheMax <= 0 {
		opts.CacheMax = defaultEvaluatorCacheMax
	}
	return &Evaluator{
		policy:   p,
		source:   source,
		cacheTTL: opts.CacheTTL,
		cacheMax: opts.CacheMax,
		list:     list.New(),
		index:    make(map[cacheKey]*list.Element, opts.CacheMax),
	}
}

// AllowFor evaluates action against id with the target as context.
// For non-maintainer gates the target is ignored and the result
// matches Policy.Allow. For the maintainer gate, the result is
// (id.Email is in source.IsMaintainer(ctx, target, id.Email)).
//
// Anonymous identity always denies a maintainer gate. Source
// errors deny safely.
func (e *Evaluator) AllowFor(ctx context.Context, id auth.Identity, action, target string) bool {
	gate, ok := e.policy.Auth.Actions[action]
	if !ok {
		return false
	}
	if gate != GateMaintainer {
		// Non-target gate — same answer as Policy.Allow.
		return e.policy.Allow(id, action)
	}
	// maintainer gate from here.
	if !id.IsAuthenticated() || id.Email == "" || target == "" {
		return false
	}
	if e.source == nil {
		return false
	}

	key := cacheKey{email: id.Email, module: target}
	if v, ok := e.lookupCache(key); ok {
		return v
	}
	got, err := e.source.IsMaintainer(ctx, target, id.Email)
	if err != nil {
		// Don't cache errors — let the next call retry. Caching a
		// transient DB failure would freeze the verdict for a full
		// TTL after recovery.
		return false
	}
	e.storeCache(key, got)
	return got
}

// InvalidateMaintainer purges the cache entry for (email, module).
// Call from grant/revoke handlers so the next AllowFor reflects
// the new state without waiting for TTL.
func (e *Evaluator) InvalidateMaintainer(email, module string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if el, ok := e.index[cacheKey{email: email, module: module}]; ok {
		e.list.Remove(el)
		delete(e.index, el.Value.(*cacheEntry).key)
	}
}

func (e *Evaluator) lookupCache(key cacheKey) (bool, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	el, ok := e.index[key]
	if !ok {
		return false, false
	}
	entry := el.Value.(*cacheEntry)
	if time.Now().After(entry.expires) {
		e.list.Remove(el)
		delete(e.index, key)
		return false, false
	}
	// LRU bump.
	e.list.MoveToFront(el)
	return entry.verdict, true
}

func (e *Evaluator) storeCache(key cacheKey, verdict bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if el, ok := e.index[key]; ok {
		entry := el.Value.(*cacheEntry)
		entry.verdict = verdict
		entry.expires = time.Now().Add(e.cacheTTL)
		e.list.MoveToFront(el)
		return
	}
	entry := &cacheEntry{key: key, verdict: verdict, expires: time.Now().Add(e.cacheTTL)}
	el := e.list.PushFront(entry)
	e.index[key] = el
	// Evict oldest if over cap.
	for e.list.Len() > e.cacheMax {
		oldest := e.list.Back()
		if oldest == nil {
			break
		}
		e.list.Remove(oldest)
		delete(e.index, oldest.Value.(*cacheEntry).key)
	}
}
