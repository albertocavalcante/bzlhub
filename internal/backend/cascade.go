package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/egress"
)

// Cascade is a Backend that walks: primary first, then HTTP upstreams in
// parallel, first-200-wins. Returns ErrNotFound if every source said
// "no", ErrUpstreamUnavailable if every upstream failed for non-404
// reasons. Used for federation under Plan 16.
//
// Blob requests bypass cascading and go straight to the primary —
// upstreams don't serve /blobs/<key> in the BCR-shape protocol; tarballs
// are at the URLs embedded in source.json instead.
type Cascade struct {
	primary                Backend
	upstreams              []*Upstream
	http                   *http.Client
	logger                 *slog.Logger
	cache                  *responseCache
	disableShadowDetection bool

	// promote is the deferred-bound cache-promotion hook (Plan 16
	// Layer F). atomic.Pointer so probeOne can read concurrently
	// while cmd/bzlhub boot wires it after Service construction.
	// nil pointer + nil func both = no-op (hook never fires).
	promote atomic.Pointer[func(module, version string)]

	// collisionLogger is the deferred-bound Plan 16 Layer D hook.
	// Called once per (m, v, source_url, kind) tuple to record
	// provenance + collisions. atomic.Pointer for the same
	// concurrent-read pattern as promote.
	collisionLogger atomic.Pointer[func(module, version, sourceURL, kind string)]

	// collisionCoalesce is Plan 16's "5-minute write-coalesce":
	// skip re-logging a (m, v, source_url, kind) tuple that was
	// already recorded recently. Bounded by the small number of
	// distinct (m, v) the federation serves; map grows but doesn't
	// turn over fast enough to warrant LRU eviction at v1.
	collisionCoalesceMu sync.Mutex
	collisionCoalesce   map[string]time.Time
}

// collisionCoalesceWindow controls how often the cascade re-logs a
// given (m, v, source, kind). Plan 16 spec says 5 minutes; same
// here.
const collisionCoalesceWindow = 5 * time.Minute

// CollisionKind values mirror store.ModuleSourceKind without
// importing store (which would close a cycle). The cmd-side wiring
// just passes these strings through to LogModuleSource.
const (
	CollisionKindLocal     = "local"
	CollisionKindUpstream  = "http-upstream"
	CollisionKindShadowed  = "collision-shadowed"
)

// Upstream is one federation upstream. Reachability is updated lazily —
// readers can inspect this via /api/v1/upstreams (Plan 16 F3).
//
// Authenticated upstreams: when the configured URL includes userinfo
// (e.g., `https://oauth2:glpat-xxx@git.example.com/...`), NewCascade
// strips the userinfo from URL and stores the rendered Basic-auth
// header on authHeader. probeOne / ProbeUpstream inject the header on
// every request. The public URL field always holds the sanitized form
// so logs + /api/v1/upstreams never expose the credential.
type Upstream struct {
	URL string

	// authHeader is the pre-rendered Authorization value (e.g.,
	// "Basic <base64>") derived from URL userinfo at construction.
	// Empty when the configured URL had no userinfo. Set once in
	// NewCascade; read-only after.
	authHeader string

	// Reachable is the last observed state. Written by the boot probe
	// (Layer A) and the future background probe loop. Reads from
	// /api/v1/upstreams in F3.
	mu                sync.RWMutex
	reachable         bool
	lastProbe         time.Time
	lastProbeLatency  time.Duration
	lastProbeErrorMsg string
}

// Reachable returns the upstream's last-observed reachability status
// along with the time of the last probe.
func (u *Upstream) Reachable() (reachable bool, lastProbe time.Time, latency time.Duration, errMsg string) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.reachable, u.lastProbe, u.lastProbeLatency, u.lastProbeErrorMsg
}

func (u *Upstream) setReachable(reachable bool, latency time.Duration, errMsg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.reachable = reachable
	u.lastProbe = time.Now().UTC()
	u.lastProbeLatency = latency
	u.lastProbeErrorMsg = errMsg
}

// CascadeConfig configures a Cascade.
type CascadeConfig struct {
	// Primary is the always-tried-first Backend. Required.
	Primary Backend

	// Upstreams is the ordered list of HTTP upstreams probed in
	// parallel when Primary returns ErrNotFound. Each URL must be the
	// root of a BCR-shape registry (i.e., the directory containing
	// bazel_registry.json).
	Upstreams []*Upstream

	// HTTPClient is the client used for upstream requests. Tests inject
	// a captured-server client; production uses a default with a 30s
	// transport timeout. Per-request timeout is enforced separately via
	// context.
	HTTPClient *http.Client

	// Logger receives structured warnings (upstream errors, collisions
	// when F3 lands). Default: slog.Default().
	Logger *slog.Logger

	// CacheCapacity bounds the in-process response cache (Plan 16
	// Layer C). Default 1000 entries; ≤ 0 disables caching entirely
	// (useful for tests that need fresh upstream fetches every call).
	// Configurable via BZLHUB_UPSTREAM_CACHE_SIZE at the cmd layer.
	CacheCapacity int

	// DisableShadowDetection skips the drainShadowed goroutine after
	// a winner is found. With detection on (default), every cascade
	// fetch lets all upstreams complete so collision-shadowed events
	// fire reliably — Plan 16 Layer D's audit table reflects reality.
	// The cost is ~len(upstreams) requests per resolve instead of
	// effectively one (sibling-cancel saved the rest in the old
	// behavior). Operators serving high-volume cascades against
	// rate-limited or expensive upstreams flip this on; the trade-off
	// is the audit table never sees a shadowed-200 row. Configurable
	// via BZLHUB_DISABLE_SHADOW_DETECTION at the cmd layer.
	DisableShadowDetection bool
}

// NewCascade constructs a Cascade. Returns an error if Primary is nil.
// Upstreams may be empty — the result is a no-op wrapper that just
// delegates to Primary, useful for testing the wrap layer in isolation.
func NewCascade(cfg CascadeConfig) (*Cascade, error) {
	if cfg.Primary == nil {
		return nil, errors.New("backend: Cascade requires Primary")
	}
	// Default capacity = 1000 entries (Plan 16 Layer C). Negative
	// explicit value disables; zero implicit → use the default.
	capacity := cfg.CacheCapacity
	if capacity == 0 {
		capacity = 1000
	}
	c := &Cascade{
		primary:                cfg.Primary,
		upstreams:              cfg.Upstreams,
		http:                   cfg.HTTPClient,
		disableShadowDetection: cfg.DisableShadowDetection,
		logger:                 cfg.Logger,
		cache:                  newResponseCache(capacity),
		collisionCoalesce:      make(map[string]time.Time),
	}
	if c.http == nil {
		// Egress wraps the default transport so the cascade's
		// fall-through to upstream BCRs participates in the
		// process-wide policy (mirror-only profile denies; sync-runner
		// audits; default permits). Profile binding happens at
		// bzlhub serve startup; here we accept whatever policy is in
		// effect, with a permissive zero-value if none is bound.
		c.http = egress.NewHTTPClient(egress.Policy{})
		c.http.Timeout = 30 * time.Second
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	// Normalize each upstream URL once:
	//   1. Strip trailing slash so path composition is predictable.
	//   2. Extract userinfo (if any) into the pre-rendered Basic-auth
	//      authHeader, then rewrite URL to the sanitized form so logs
	//      + /api/v1/upstreams never expose the credential.
	for _, u := range c.upstreams {
		stripped, header, err := splitURLUserinfo(u.URL)
		if err != nil {
			return nil, fmt.Errorf("backend: upstream %q: %w", u.URL, err)
		}
		u.URL = strings.TrimRight(stripped, "/")
		u.authHeader = header
	}
	return c, nil
}

// Upstreams returns the configured upstreams. The slice is shared (not
// copied); callers must not mutate it. Used by /api/v1/upstreams in F3.
func (c *Cascade) Upstreams() []*Upstream { return c.upstreams }

// Primary returns the wrapped local-first Backend. Used by the
// /api/v1/upstreams introspection handler to report what the primary
// is (kind: "local" + root path when the primary is a *File). Kept as
// the interface type so a future S3/Postgres primary doesn't break
// the accessor signature.
func (c *Cascade) Primary() Backend { return c.primary }

// CacheStats returns a snapshot of the response cache state
// (Plan 16 F3 spec). Zero values when the cache is disabled
// (CacheCapacity ≤ 0).
func (c *Cascade) CacheStats() CacheStats { return c.cache.Stats() }

// SetPromoteHook installs an async callback that fires whenever an
// upstream wins a fetch for a `modules/<m>/<v>/...` path (Plan 16
// Layer F: cache promotion on serve). The hook is fired in a
// detached goroutine with no context propagation — the caller is
// responsible for managing its own timeouts / cancellation.
//
// The hook is called at most once per (m, v, path) within a probe;
// duplicate calls across paths for the same (m, v) are possible and
// expected (the hook implementation should de-duplicate / use its
// own idempotence — Bump is idempotent by design, so naive wiring
// is fine).
//
// Deferred binding (not part of CascadeConfig) so cmd/bzlhub can
// wire the hook AFTER constructing the Service, while the Cascade
// is constructed earlier in the boot sequence.
//
// Passing nil clears the hook.
func (c *Cascade) SetPromoteHook(hook func(module, version string)) {
	if hook == nil {
		c.promote.Store(nil)
		return
	}
	c.promote.Store(&hook)
}

// SetCollisionLogger installs the Plan 16 Layer D hook. Called
// async once per (module, version, source_url, kind) seen during
// a cascade probe. The kind argument is one of CollisionKindLocal,
// CollisionKindUpstream, or CollisionKindShadowed.
//
// Same deferred-binding pattern as SetPromoteHook so cmd/bzlhub can
// wire the hook to the store-layer LogModuleSource call after
// constructing the Service. Detached goroutine; the hook is
// responsible for its own context + idempotence (LogModuleSource
// uses INSERT OR IGNORE so duplicate writes are cheap).
//
// Passing nil clears the hook.
func (c *Cascade) SetCollisionLogger(hook func(module, version, sourceURL, kind string)) {
	if hook == nil {
		c.collisionLogger.Store(nil)
		return
	}
	c.collisionLogger.Store(&hook)
}

// extractModuleVersion parses a BCR path of the form
// `modules/<m>/<v>/...` and returns (module, version, true). Other
// path shapes (bazel_registry.json, modules/<m>/metadata.json,
// blobs/<key>) return ("", "", false).
//
// Used by the upstream-win path to decide whether to fire the
// promote hook. Promoting only triggers on full (m, v) paths
// because Bump operates on a specific version — metadata-only
// fetches don't have a version to ingest.
func extractModuleVersion(relPath string) (module, version string, ok bool) {
	const prefix = "modules/"
	if !strings.HasPrefix(relPath, prefix) {
		return "", "", false
	}
	rest := relPath[len(prefix):]
	// Need at least two segments after "modules/": module + version.
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return "", "", false
	}
	module = rest[:i]
	rest = rest[i+1:]
	j := strings.IndexByte(rest, '/')
	if j <= 0 {
		// Two-segment forms: modules/<m>/metadata.json (no version).
		return "", "", false
	}
	version = rest[:j]
	// Reject paths whose "version" segment is reserved
	// non-version content. metadata.json sits at modules/<m>/
	// metadata.json — caught by the no-trailing-segment guard
	// above. But "patches" / "overlay" appear at modules/<m>/<v>/
	// patches/<file> — those are LEGITIMATE downstream paths
	// where (m, v) is still meaningful. So no extra filter here.
	if module == "" || version == "" {
		return "", "", false
	}
	return module, version, true
}

// ---- Backend impl ----

func (c *Cascade) GetBazelRegistryJSON(ctx context.Context) (io.ReadCloser, error) {
	return c.cascadeGet(ctx, c.primary.GetBazelRegistryJSON, "bazel_registry.json")
}

func (c *Cascade) GetMetadata(ctx context.Context, module string) (io.ReadCloser, error) {
	return c.cascadeGet(ctx, func(ctx context.Context) (io.ReadCloser, error) {
		return c.primary.GetMetadata(ctx, module)
	}, path.Join("modules", module, "metadata.json"))
}

func (c *Cascade) GetModuleBazel(ctx context.Context, module, version string) (io.ReadCloser, error) {
	return c.cascadeGet(ctx, func(ctx context.Context) (io.ReadCloser, error) {
		return c.primary.GetModuleBazel(ctx, module, version)
	}, path.Join("modules", module, version, "MODULE.bazel"))
}

func (c *Cascade) GetSourceJSON(ctx context.Context, module, version string) (io.ReadCloser, error) {
	return c.cascadeGet(ctx, func(ctx context.Context) (io.ReadCloser, error) {
		return c.primary.GetSourceJSON(ctx, module, version)
	}, path.Join("modules", module, version, "source.json"))
}

func (c *Cascade) GetPatch(ctx context.Context, module, version, filename string) (io.ReadCloser, error) {
	if strings.ContainsAny(filename, "/\\") {
		return nil, ErrNotFound
	}
	return c.cascadeGet(ctx, func(ctx context.Context) (io.ReadCloser, error) {
		return c.primary.GetPatch(ctx, module, version, filename)
	}, path.Join("modules", module, version, "patches", filename))
}

func (c *Cascade) GetOverlay(ctx context.Context, module, version, overlay string) (io.ReadCloser, error) {
	clean := path.Clean(overlay)
	if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
		return nil, ErrNotFound
	}
	return c.cascadeGet(ctx, func(ctx context.Context) (io.ReadCloser, error) {
		return c.primary.GetOverlay(ctx, module, version, overlay)
	}, path.Join("modules", module, version, "overlay", clean))
}

// GetBlob does NOT cascade. Upstreams don't serve /blobs/<key> in the
// BCR-shape protocol — tarballs are at the URLs embedded in source.json.
// Cascading here would just generate 404s from every upstream.
func (c *Cascade) GetBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	return c.primary.GetBlob(ctx, key)
}

// ---- Cascade core ----

// cascadeGet tries the primary first. On ErrNotFound, fires every
// upstream in parallel for upstreamPath, returns the first 200 body, and
// cancels the rest. Returns ErrNotFound if all upstreams confirmed 404;
// ErrUpstreamUnavailable if all upstreams errored without an
// authoritative 404.
func (c *Cascade) cascadeGet(
	ctx context.Context,
	primaryGet func(context.Context) (io.ReadCloser, error),
	upstreamPath string,
) (io.ReadCloser, error) {
	rc, err := primaryGet(ctx)
	if err == nil {
		// Plan 16 Layer D: log the local serve. Only for paths that
		// have a (module, version) anchor — bazel_registry.json +
		// metadata.json have nothing useful to record in
		// module_sources.
		if m, v, ok := extractModuleVersion(upstreamPath); ok {
			c.logCollision(m, v, "local", CollisionKindLocal)
		}
		return rc, nil
	}
	if !errors.Is(err, ErrNotFound) {
		// Primary failed for a reason that isn't "not found" — surface
		// it. Don't paper over filesystem errors etc. with an upstream
		// hit; the operator needs to see the local fault.
		return nil, err
	}
	if len(c.upstreams) == 0 {
		return nil, ErrNotFound
	}
	return c.probeUpstreams(ctx, upstreamPath)
}

// logCollision fires the collision-logger hook async if it's set
// AND the coalesce window has elapsed for this (m, v, source, kind)
// tuple. The hook runs in a detached goroutine — Plan 16 explicitly
// calls collision tracking best-effort, never blocking the serve
// path.
func (c *Cascade) logCollision(module, version, sourceURL, kind string) {
	hookPtr := c.collisionLogger.Load()
	if hookPtr == nil {
		return
	}
	key := module + "@" + version + ":" + sourceURL + ":" + kind
	now := time.Now()
	c.collisionCoalesceMu.Lock()
	last, ok := c.collisionCoalesce[key]
	if ok && now.Sub(last) < collisionCoalesceWindow {
		c.collisionCoalesceMu.Unlock()
		return
	}
	c.collisionCoalesce[key] = now
	c.collisionCoalesceMu.Unlock()
	hook := *hookPtr
	go hook(module, version, sourceURL, kind)
}

// splitURLUserinfo extracts userinfo from a URL and renders it as a
// pre-formatted Basic-auth Authorization value. Returns:
//   - sanitized URL (userinfo stripped) for use in logs + API responses
//   - "Basic <base64>" header value (empty if the URL had no userinfo)
//   - error if the URL is malformed
//
// Example: `https://oauth2:glpat-xxx@git.example.com/r` →
//
//	("https://git.example.com/r", "Basic b2F1dGgyOmdscGF0LXh4eA==", nil)
//
// Forgejo + GitLab + GitHub all accept Basic auth with "oauth2" (or
// "x-access-token", or the literal username) as the user and the PAT
// as the password. Git uses this same pattern over HTTPS, so the
// embedded-credential URL syntax is familiar territory.
func splitURLUserinfo(raw string) (cleanedURL string, authHeader string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if u.User == nil {
		return raw, "", nil
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	creds := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	u.User = nil
	return u.String(), "Basic " + creds, nil
}
