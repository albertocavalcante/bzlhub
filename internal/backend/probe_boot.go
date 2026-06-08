package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProbeUpstream sends a single GET <url>/bazel_registry.json to test
// reachability + BCR-shape. Used by bzlhub serve at boot to populate
// initial reachability state.
//
// Returns:
//   - nil, nil       → 200 OK, upstream confirmed reachable + BCR-shape
//   - nil, ProbeNotBCR        → reached upstream but it returned 4xx
//   - nil, ProbeTransientErr  → 5xx, timeout, network error
//
// The boot wiring in cmd/bzlhub treats ProbeNotBCR as hard-fail (config
// error: typo'd URL, wrong host), ProbeTransientErr as soft-fail (start
// degraded, background probe loop will retry).
func (c *Cascade) ProbeUpstream(ctx context.Context, up *Upstream) error {
	probeCtx, cancel := context.WithTimeout(ctx, upstreamProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, "GET", up.URL+"/bazel_registry.json", nil)
	if err != nil {
		up.setReachable(false, 0, err.Error())
		return &ProbeError{Kind: ProbeNotBCR, Err: err}
	}
	if up.authHeader != "" {
		req.Header.Set("Authorization", up.authHeader)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	latency := time.Since(start)
	if err != nil {
		up.setReachable(false, latency, err.Error())
		return &ProbeError{Kind: ProbeTransientErr, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	switch {
	case resp.StatusCode == http.StatusOK:
		up.setReachable(true, latency, "")
		return nil
	case resp.StatusCode >= 500 && resp.StatusCode < 600:
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		up.setReachable(false, latency, msg)
		return &ProbeError{Kind: ProbeTransientErr, Err: errors.New(msg)}
	default:
		// 4xx → config error: misspelled host, wrong URL shape, auth
		// required, etc. Hard-fail at boot is the right behavior.
		msg := fmt.Sprintf("HTTP %d (not a BCR-shape registry)", resp.StatusCode)
		up.setReachable(false, latency, msg)
		return &ProbeError{Kind: ProbeNotBCR, Err: errors.New(msg)}
	}
}

// ProbeErrorKind discriminates between hard-fail and soft-fail probe
// outcomes. See ProbeUpstream.
type ProbeErrorKind int

const (
	// ProbeNotBCR signals the upstream is reachable but doesn't look
	// like a BCR-shape registry (4xx response). Boot should hard-fail.
	ProbeNotBCR ProbeErrorKind = iota
	// ProbeTransientErr signals a network failure, timeout, or 5xx.
	// Boot should soft-fail (log warning, start degraded).
	ProbeTransientErr
)

// ProbeError carries the discriminator + underlying error from
// ProbeUpstream so the caller can decide hard-fail vs soft-fail.
type ProbeError struct {
	Kind ProbeErrorKind
	Err  error
}

func (e *ProbeError) Error() string { return e.Err.Error() }
func (e *ProbeError) Unwrap() error { return e.Err }

// IsProbeTransient reports whether err came from ProbeUpstream and
// represents a soft-fail (transient) condition.
func IsProbeTransient(err error) bool {
	var pe *ProbeError
	if errors.As(err, &pe) {
		return pe.Kind == ProbeTransientErr
	}
	return false
}

// RunProbeLoop refreshes every upstream's reachability state on the
// given interval, returning when ctx is canceled. Intended to be
// called in a detached goroutine from cmd/bzlhub after boot.
//
// Each tick probes every upstream sequentially. ProbeUpstream's
// per-call timeout (upstreamProbeTimeout = 5s) bounds the work per
// tick at roughly len(upstreams) × 5s in the worst case, which is
// well under the default 60s interval even with a dozen upstreams.
//
// interval ≤ 0 returns immediately — caller is opting out (useful for
// tests that don't want a background goroutine, or for one-shot
// bzlhub serve invocations).
func (c *Cascade) RunProbeLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, up := range c.upstreams {
				// ProbeUpstream updates the upstream's Reachable
				// state regardless of outcome — error paths set
				// reachable=false with the error message; success
				// sets reachable=true with latency. We don't act
				// on the return value here; the next /api/v1/upstreams
				// request will surface whatever the latest state is.
				_ = c.ProbeUpstream(ctx, up)
			}
		}
	}
}
