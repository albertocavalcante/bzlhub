// Package purge wires canopy's publish path to a CDN cache-purge
// provider. The Provider interface is satisfied by NoOp (the
// default, for deployments with no CDN) and by Adapter (which wraps
// any github.com/albertocavalcante/go-cdn-purge cdnpurge.Provider —
// today: Cloudflare, Fastly, NoOp).
//
// Why a thin layer instead of importing cdnpurge directly into admit:
//   - Different audit-log shape (canopy logs cdn-egress per call).
//   - Different error tolerance (purge failures are logged, NOT
//     fatal to the admit success path — the module is still indexed;
//     CDN edge is just stale until the next TTL expires).
//   - Lets us swap vendors / mock cleanly at the canopy boundary.
package purge

import (
	"context"
	"log/slog"
	"strings"

	cdnpurge "github.com/albertocavalcante/go-cdn-purge"
)

// Provider is canopy's narrowed view of a CDN purge backend. The
// shape mirrors cdnpurge.Provider but doesn't expose PurgeResult
// to callers — admit cares about "did we try" and "did anything
// hard-fail", not per-URL receipts.
type Provider interface {
	// Purge invalidates the listed URLs at the CDN edge. Returns
	// an error only for call-level failures (rate limit, transport,
	// 5xx). Per-URL failures (vendor returned 200 with status:"error")
	// are NOT errors here — they're logged by the implementation
	// and otherwise tolerated. Caller continues regardless.
	Purge(ctx context.Context, urls []string) error

	// Name reports the underlying vendor for operator diagnostics
	// + audit log enrichment. "noop" for the default; "cloudflare"
	// or "fastly" for real backends.
	Name() string
}

// NoOp is the sentinel "no CDN" provider. Construct via NewNoOp() or
// take its zero value. Returns nil from Purge regardless of input;
// useful for local dev, on-prem, or tests.
type NoOp struct{}

// NewNoOp returns the NoOp provider as the Provider interface for
// callers that want to inject it explicitly.
func NewNoOp() Provider { return NoOp{} }

// Purge accepts any URL list and reports success. URLs are NOT
// validated — they would be at the real vendor's call site, and
// validating here would couple NoOp to URL shape rules.
func (NoOp) Purge(context.Context, []string) error { return nil }

// Name returns "noop".
func (NoOp) Name() string { return "noop" }

// Adapter wraps a cdnpurge.Provider (the library's interface) into
// canopy's narrower Provider. The adapter swallows per-URL Failures
// (logged at Warn) and surfaces only function-level errors.
type Adapter struct {
	upstream cdnpurge.Provider
	log      *slog.Logger
}

// NewAdapter wraps an existing cdnpurge.Provider. log is required
// (use slog.Default() if no scoped logger is available).
func NewAdapter(p cdnpurge.Provider, log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{upstream: p, log: log}
}

// Name returns the underlying vendor's name (delegates to upstream).
func (a *Adapter) Name() string { return a.upstream.Name() }

// Purge delegates to the wrapped Provider. Per-URL failures land in
// a Warn log; function-level errors return to the caller. The
// returned PurgeResult is consumed for the structured log only —
// callers don't need the per-URL receipt to act.
func (a *Adapter) Purge(ctx context.Context, urls []string) error {
	if len(urls) == 0 {
		return nil
	}
	result, err := a.upstream.Purge(ctx, urls)
	if err != nil {
		a.log.Warn("cdn purge failed",
			"vendor", a.upstream.Name(),
			"requested", len(urls),
			"submitted", len(result.Submitted),
			"err", err)
		return err
	}
	if len(result.Failures) > 0 {
		// Log per-URL failures as a single line — operators don't
		// need every URL spelled out, just the count + a sample.
		var sample []string
		for u := range result.Failures {
			sample = append(sample, u)
			if len(sample) >= 3 {
				break
			}
		}
		a.log.Warn("cdn purge had per-URL failures (continuing)",
			"vendor", a.upstream.Name(),
			"requested", len(urls),
			"submitted", len(result.Submitted),
			"failed", len(result.Failures),
			"sample", strings.Join(sample, ","))
	}
	a.log.Debug("cdn purge submitted",
		"vendor", a.upstream.Name(),
		"requested", len(urls),
		"submitted", len(result.Submitted),
		"vendor_requests", result.Requests)
	return nil
}
