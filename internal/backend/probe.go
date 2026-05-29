package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// upstreamProbeTimeout bounds an individual upstream request. Parallel
// probe in cascadeGet means total wall time is roughly this value
// regardless of how many upstreams are configured.
const upstreamProbeTimeout = 5 * time.Second

// MaxCascadeBodyBytes caps the in-memory buffer probeOne builds when
// caching a successful upstream response. A compromised upstream
// serving a multi-GB body would otherwise OOM the canopy serve
// process — the cache fill would buffer the entire response before
// putting it. 16MB matches fetch.MaxJSONResponseBytes; BCR's largest
// real metadata.json sits under 100KB so the cap is two orders of
// magnitude above legitimate use.
const MaxCascadeBodyBytes = 16 * 1024 * 1024

// ErrCascadeBodyTooLarge is the sentinel returned when an upstream
// body exceeds MaxCascadeBodyBytes. Wrapped by the cascade result;
// callers can errors.Is to distinguish "bomb" from generic upstream
// errors.
var ErrCascadeBodyTooLarge = errors.New("cascade: upstream body exceeded cap")

// upstreamResult is sent over a buffered channel by each parallel probe.
type upstreamResult struct {
	upstream *Upstream
	body     io.ReadCloser
	status   int
	err      error
}

// probeUpstreams fires every upstream concurrently with a 5s per-call
// timeout. First 200 wins; the remaining bodies are drained and closed.
// Returns ErrNotFound only if every upstream returned 404 (authoritative
// "no" from all sources). Returns ErrUpstreamUnavailable if every
// upstream failed for non-404 reasons (timeout, 5xx, network error).
func (c *Cascade) probeUpstreams(ctx context.Context, relPath string) (io.ReadCloser, error) {
	n := len(c.upstreams)
	results := make(chan upstreamResult, n)

	// Two regimes:
	//
	//   Collision-detect ON (default, plan 16 promise): probes run on
	//   context.WithoutCancel(ctx) so runner-ups complete after the
	//   winner is handed back to the caller. Each probeOne still
	//   enforces its own upstreamProbeTimeout (5s). On winner-detect
	//   the cascade returns immediately and detaches the remaining
	//   tally into drainShadowed so collision-shadowed events fire.
	//
	//   Collision-detect OFF (operator opt-out via env): probes run
	//   on a cancellable child ctx. On winner-detect we cancel
	//   siblings. Runner-ups die with context.Canceled instead of
	//   completing — saves upstream traffic but the collision audit
	//   never sees shadowed-200s. Useful for high-volume / expensive
	//   upstream deployments.
	var probeCtx context.Context
	var cancelSiblings context.CancelFunc
	if c.disableShadowDetection {
		probeCtx, cancelSiblings = context.WithCancel(ctx)
		defer cancelSiblings()
	} else {
		probeCtx = context.WithoutCancel(ctx)
	}
	for _, up := range c.upstreams {
		go c.probeOne(probeCtx, up, relPath, results)
	}

	// First pass: pop until we see a 200 winner OR exhaust the list
	// of upstreams. On winner-detect we hand the body to the caller
	// immediately and either cancel siblings (opt-out) or detach the
	// remaining tally into a drainShadowed goroutine (default).
	module, version, hasMV := extractModuleVersion(relPath)
	var sawNotFound int
	var sawError int
	consumed := 0
	for consumed < n {
		res := <-results
		consumed++
		switch {
		case res.err == nil && res.status == http.StatusOK:
			if hasMV {
				c.logCollision(module, version, res.upstream.URL, CollisionKindUpstream)
			}
			remaining := n - consumed
			if remaining > 0 {
				if c.disableShadowDetection {
					// Cancel siblings + start a tiny drain goroutine
					// to consume their canceled results so the channel
					// doesn't leak the probeOne goroutines stuck on
					// a buffered send. No collision logging happens
					// in this drain — that's the whole point of the
					// opt-out.
					cancelSiblings()
					go drainSilent(results, remaining)
				} else {
					go c.drainShadowed(results, remaining, module, version, hasMV, relPath)
				}
			}
			return res.body, nil
		case res.err == nil && res.status == http.StatusNotFound:
			sawNotFound++
		default:
			sawError++
			// Suppress context.Canceled WARNs when collision-detect
			// is off — those are our own sibling-cancel signals
			// arriving from the cancelSiblings() above.
			if c.disableShadowDetection && res.err != nil && errors.Is(res.err, context.Canceled) {
				break
			}
			if res.err != nil {
				c.logger.Warn("federation upstream error",
					"upstream", res.upstream.URL, "path", relPath, "err", res.err)
			} else {
				c.logger.Warn("federation upstream non-200",
					"upstream", res.upstream.URL, "path", relPath, "status", res.status)
			}
		}
	}

	if sawNotFound > 0 {
		// At least one upstream authoritatively said no. Honor that
		// even if others errored — Bazel's resolution path treats 404
		// as "this version doesn't exist anywhere", which matches.
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf("%w (%d upstream errors)", ErrUpstreamUnavailable, sawError)
}

// drainSilent pops remaining results and discards them. Used by the
// shadow-detection-off path so probeOne goroutines blocked on a
// buffered channel send don't leak. No collision logging happens
// here — that's the whole point of the opt-out.
func drainSilent(results <-chan upstreamResult, remaining int) {
	for i := 0; i < remaining; i++ {
		res := <-results
		if res.body != nil {
			drainAndClose(res.body)
		}
	}
}

// drainShadowed runs in a detached goroutine after the cascade has
// handed a winner body to the caller. It pops the remaining
// upstream results so shadowed-200s fire the collision-logger hook
// AND so the channel doesn't leak goroutines stuck on the send. The
// loop is bounded by upstreamProbeTimeout on each probe (no need to
// honor parent ctx here — by the time we run, the caller already has
// what it wanted).
func (c *Cascade) drainShadowed(results <-chan upstreamResult, remaining int, module, version string, hasMV bool, relPath string) {
	for i := 0; i < remaining; i++ {
		res := <-results
		switch {
		case res.err == nil && res.status == http.StatusOK:
			if hasMV {
				c.logCollision(module, version, res.upstream.URL, CollisionKindShadowed)
			}
			drainAndClose(res.body)
		case res.err == nil && res.status == http.StatusNotFound:
			// Authoritative no from this upstream; no audit row needed.
		default:
			// Real upstream error (timeout, 5xx, DNS, etc.) — log so
			// operators see flaky upstreams even when the cascade as
			// a whole succeeded. context.Canceled isn't an issue here
			// because we don't cancel anymore.
			if res.err != nil {
				c.logger.Warn("federation upstream error (shadow-drain)",
					"upstream", res.upstream.URL, "path", relPath, "err", res.err)
			} else {
				c.logger.Warn("federation upstream non-200 (shadow-drain)",
					"upstream", res.upstream.URL, "path", relPath, "status", res.status)
			}
		}
	}
}

// probeOne sends one GET to upstream/relPath with a per-call 5s timeout.
// Result is sent on results regardless of outcome — the caller's tally
// loop pulls exactly len(upstreams) results.
//
// Plan 16 Layer C: a cache lookup short-circuits the HTTP call when a
// fresh entry exists. Cache fill happens on the 200 path; non-200
// responses (404, 5xx) are NOT cached so a transient failure doesn't
// poison the cache for the TTL window.
func (c *Cascade) probeOne(parentCtx context.Context, up *Upstream, relPath string, results chan<- upstreamResult) {
	// Cache hit: synthesize a 200 result without any HTTP traffic.
	// Each consumer gets an independent ReadCloser over the shared
	// bytes via io.NopCloser. The cache entry's reachability state
	// isn't refreshed on a hit — that's the cost of cache parallelism
	// (the background probe loop F3 docs is the right place to keep
	// the snapshot warm).
	cacheKey := up.URL + ":" + relPath
	if body, ok := c.cache.Get(cacheKey); ok {
		results <- upstreamResult{
			upstream: up,
			body:     io.NopCloser(bytes.NewReader(body)),
			status:   http.StatusOK,
		}
		return
	}

	ctx, cancel := context.WithTimeout(parentCtx, upstreamProbeTimeout)
	defer cancel()

	reqURL := up.URL + "/" + strings.TrimLeft(relPath, "/")
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		results <- upstreamResult{upstream: up, err: err}
		return
	}
	req.Header.Set("Accept", "application/json")
	if up.authHeader != "" {
		req.Header.Set("Authorization", up.authHeader)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Don't touch the upstream's probe state here — a cascade
		// fetch can fail for caller-side reasons (request ctx canceled
		// when the bazel client gives up, drainShadowed killing
		// runner-up probes) that have nothing to do with the upstream's
		// actual reachability. Probe state is owned exclusively by
		// ProbeUpstream (boot + background loop) so /api/v1/upstreams
		// reflects deliberate health checks, not opportunistic fetch
		// noise.
		results <- upstreamResult{upstream: up, err: err}
		return
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		// Cache fill: buffer the body into memory + put + hand out a
		// fresh reader. Caller owns closing the new reader; the
		// original resp.Body is closed once we've drained it. Cap
		// the read via io.LimitReader sized to MaxCascadeBodyBytes+1
		// so we can detect overrun without trusting Content-Length.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxCascadeBodyBytes+1))
		resp.Body.Close()
		if readErr != nil {
			// Partial read → don't cache; surface as upstream error so
			// the tally treats it as a failure rather than a 200.
			results <- upstreamResult{upstream: up, err: readErr}
			return
		}
		if int64(len(body)) > MaxCascadeBodyBytes {
			results <- upstreamResult{
				upstream: up,
				err:      fmt.Errorf("%w from %s: %d > %d", ErrCascadeBodyTooLarge, up.URL, len(body), MaxCascadeBodyBytes),
			}
			return
		}
		c.cache.Put(cacheKey, body, cacheTTLFor(relPath))
		// Plan 16 Layer F: cache promotion on serve. Fire the hook
		// async when an upstream wins a `modules/<m>/<v>/...` path
		// AND a hook is wired. Bump is idempotent so duplicate calls
		// across the closure are safe (e.g., metadata.json fetch
		// followed by source.json fetch for the same module).
		if module, version, ok := extractModuleVersion(relPath); ok {
			if hookPtr := c.promote.Load(); hookPtr != nil {
				hook := *hookPtr
				go hook(module, version)
			}
		}
		results <- upstreamResult{
			upstream: up,
			body:     io.NopCloser(bytes.NewReader(body)),
			status:   http.StatusOK,
		}
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		results <- upstreamResult{upstream: up, status: resp.StatusCode}
	default:
		drainAndClose(resp.Body)
		results <- upstreamResult{upstream: up, status: resp.StatusCode,
			err: fmt.Errorf("upstream %s: HTTP %d", up.URL, resp.StatusCode)}
	}
}

// drainAndClose reads up to 64KB from rc then closes it. Used for
// losing-race upstream responses so the underlying TCP connection can
// be reused by the http.Transport pool.
func drainAndClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 64*1024))
	_ = rc.Close()
}
