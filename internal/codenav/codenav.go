package codenav

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/albertocavalcante/understory/pkg/understory"
)

// defaultMaxCachedSources bounds the number of (module, version) source
// roots the resolver keeps warm by default. Each cached entry holds one
// *understory.Index (parsed SCIP, typically <10 MB) plus one *os.Root
// file handle. 32 is generous for canopy's expected single-process
// serving workload — the hot set for an active site is usually <10
// distinct modules.
//
// Not a flag in the binary — operators have no reason to tune it. Tests
// override via the unexported maxEntries field on Resolver so eviction
// behavior is exercisable without unpacking 33 fixtures.
const defaultMaxCachedSources = 32

// Resolver lazily materializes (understory.Index, *os.Root) pairs for
// canopy module-version coordinates, caching them under a small LRU.
//
// Concurrency: per-(module, version) sync.Once guarantees the unpack
// runs exactly once even under concurrent Resolve calls.
//
// Eviction semantics: on overflow the oldest entry is removed from the
// cache map+list, but the underlying *os.Root is intentionally NOT
// closed — an in-flight HTTP request may still hold the handle, and
// closing under it would crash the read mid-stream. The OS reclaims the
// fd when the last reference goes out of scope. Re-resolving an evicted
// coordinate re-unpacks (cheap: idempotent via the .complete sentinel).
type Resolver struct {
	store     BlobReader
	mirrorDir string
	cacheDir  string

	mu         sync.Mutex
	entries    map[string]*list.Element // key → entry's list node
	order      *list.List               // front = newest, back = oldest
	maxEntries int                      // 0 → defaultMaxCachedSources
}

// BlobReader is the slice of the store interface this package needs.
// Kept narrow so tests can fake it without spinning up SQLite.
type BlobReader interface {
	GetScipBlob(ctx context.Context, module, version string) ([]byte, error)
}

// NewResolver wires a Resolver against a SCIP blob source, a mirror
// directory layout (modules/<m>/<v>/source.json + blobs/<hex>), and a
// cache directory for unpacked source trees. The cache directory is
// created on demand.
func NewResolver(store BlobReader, mirrorDir, cacheDir string) *Resolver {
	return &Resolver{
		store:     store,
		mirrorDir: mirrorDir,
		cacheDir:  cacheDir,
		entries:   make(map[string]*list.Element),
		order:     list.New(),
	}
}

// entry holds the once-per-key lazy state. The once.Do call sets idx,
// root, and err exactly once; subsequent Resolve calls observe the
// same values.
type entry struct {
	once sync.Once
	idx  *understory.Index
	root *os.Root
	err  error

	// key is stored on the entry so the LRU can identify which map row
	// to delete when the list element is evicted from the back.
	key string
}

// Resolve returns the cached (or newly built) (Index, *os.Root) for a
// (module, version). The returned *os.Root is owned by the Resolver
// and must NOT be closed by the caller — closing would invalidate the
// cache entry for the lifetime of the process. The caller may freely
// pass it into understory.ui.NewServerWithUI.
//
// On error, returns (nil, nil, err) with the cached failure preserved.
// Future Resolve calls for the same coordinate will keep returning the
// same error until eviction; the alternative — retry-on-each-request
// — would amplify a permanently-broken module into per-request
// disk-thrash.
func (r *Resolver) Resolve(ctx context.Context, module, version string) (*understory.Index, *os.Root, error) {
	if module == "" || version == "" {
		return nil, nil, errors.New("codenav: module and version are required")
	}
	key := module + "@" + version
	e := r.entryFor(key)
	e.once.Do(func() {
		e.idx, e.root, e.err = r.build(ctx, module, version)
	})
	return e.idx, e.root, e.err
}

// entryFor returns the LRU entry for key, allocating it on miss and
// touching it on hit. Eviction (if any) happens under the same lock.
func (r *Resolver) entryFor(key string) *entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.entries[key]; ok {
		r.order.MoveToFront(el)
		return el.Value.(*entry)
	}
	e := &entry{key: key}
	el := r.order.PushFront(e)
	r.entries[key] = el
	r.evictIfFull()
	return e
}

// evictIfFull drops the LRU tail when the cache is over budget. Caller
// must hold r.mu. The evicted entry's *os.Root is deliberately left
// open — see the Resolver doc for the in-flight-request rationale.
// Letting the OS reclaim the fd on GC is the safe tradeoff vs. risking
// a crash inside http.FileServer when a request still holds the handle.
func (r *Resolver) evictIfFull() {
	limit := r.maxEntries
	if limit <= 0 {
		limit = defaultMaxCachedSources
	}
	for r.order.Len() > limit {
		el := r.order.Back()
		if el == nil {
			return
		}
		ev := el.Value.(*entry)
		r.order.Remove(el)
		delete(r.entries, ev.key)
	}
}

// build performs the actual work the once.Do guards: fetch the SCIP
// blob, parse it, locate source.json, unpack the tarball into
// cacheDir/<m>/<v>/, and open an os.Root over the result.
func (r *Resolver) build(ctx context.Context, module, version string) (*understory.Index, *os.Root, error) {
	blob, err := r.store.GetScipBlob(ctx, module, version)
	if err != nil {
		return nil, nil, fmt.Errorf("codenav: load scip blob %s@%s: %w", module, version, err)
	}
	idx, err := understory.OpenBytes(blob)
	if err != nil {
		return nil, nil, fmt.Errorf("codenav: parse scip blob %s@%s: %w", module, version, err)
	}

	sourceJSON := filepath.Join(r.mirrorDir, "modules", module, version, "source.json")
	blobsDir := filepath.Join(r.mirrorDir, "blobs")
	destDir := filepath.Join(r.cacheDir, module, version)

	if err := unpackSource(blobsDir, sourceJSON, destDir); err != nil {
		return nil, nil, fmt.Errorf("codenav: unpack %s@%s: %w", module, version, err)
	}

	root, err := os.OpenRoot(destDir)
	if err != nil {
		return nil, nil, fmt.Errorf("codenav: open root %s: %w", destDir, err)
	}
	return idx, root, nil
}
