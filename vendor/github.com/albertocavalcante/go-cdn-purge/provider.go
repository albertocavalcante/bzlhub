package cdnpurge

import "context"

// Provider is the CDN-vendor-specific purge backend. Each
// implementation translates the canonical URL-purge operation
// into its vendor's API.
//
// Implementations in v0.0.1: NoOpProvider, CloudflareProvider.
// Future v0.x: FastlyProvider, CloudFrontProvider, BunnyProvider
// (added as canopy operators ask).
//
// Implementations MUST be safe for concurrent use after
// construction.
type Provider interface {
	// Purge invalidates the given URLs at the CDN edge. Returns
	// a structured result with per-URL successes/failures + the
	// number of vendor API requests made (after the
	// implementation's internal batching).
	//
	// urls is deduplicated by the implementation before sending
	// to the vendor (saves API quota). Empty urls slice is a
	// no-op — returns success with Requests=0.
	//
	// The function-level error is reserved for transport-level
	// failures (network unavailable, vendor outage, auth
	// failure, rate limit). Per-URL failures land in
	// PurgeResult.Failures.
	//
	// Per re-review Δ4: on partial failure (some batches
	// succeeded, then a call-level error fired), Submitted
	// contains URLs from successful batches; the unmade URLs
	// are NOT listed anywhere. Caller detects partial purge by
	// comparing len(Submitted) + len(Failures) against
	// len(input).
	Purge(ctx context.Context, urls []string) (PurgeResult, error)

	// Name returns a stable identifier for audit log lines:
	// "noop", "cloudflare", "fastly", etc. Never includes
	// credentials.
	Name() string
}

// PurgeResult is the return value of Purge. Per-URL successes
// land in Submitted; per-URL failures in Failures. Requests is
// the count of vendor API calls the implementation made (after
// batching) — useful for operator rate-limit budgeting.
//
// Per re-review Δ4: on partial failure (some batches succeeded
// before a call-level error fired), Submitted contains the
// successfully-submitted URLs; unmade URLs are NOT listed.
// Caller compares len(Submitted)+len(Failures) vs len(input)
// to detect partial purge.
type PurgeResult struct {
	// Submitted lists URLs successfully submitted to the vendor
	// for purging. Vendor APIs typically return success
	// synchronously (the URL is queued for purge); actual edge
	// invalidation happens asynchronously thereafter, usually
	// within ~30 seconds for Cloudflare.
	Submitted []string

	// Failures maps each failed URL to the per-URL error the
	// vendor reported. Empty if every URL succeeded. Distinct
	// from the function-level error, which surfaces transport
	// + auth + rate-limit failures.
	Failures map[string]error

	// Requests is the count of vendor API calls the
	// implementation made. For Cloudflare with the default
	// 30-URL batch limit, a 100-URL Purge call produces
	// Requests=4 (30+30+30+10). NoOpProvider always reports 0.
	//
	// Use for: rate-limit budgeting; operator dashboards;
	// post-publish "purged N URLs in M requests" log lines.
	Requests int
}

// NoOpProvider is a Provider that does nothing — used in
// deployments without a CDN (local dev, on-prem with no CDN,
// test envs). All Purge calls return success with empty Failures
// and Requests=0.
//
// Exported as a value type so callers can use it as a sentinel
// without construction: provider := cdnpurge.NoOpProvider{}.
//
// Explicit NoOp{} over nil Provider per re-review §3 + canopy
// portfolio convention (loud-fail on misconfiguration; nil
// interface = ambiguous "did the operator mean to disable CDN
// or just forget to configure?").
type NoOpProvider struct{}

// Compile-time guard.
var _ Provider = NoOpProvider{}

// Purge always returns success. Submitted echoes the input URLs;
// Failures is empty; Requests is 0.
func (NoOpProvider) Purge(_ context.Context, urls []string) (PurgeResult, error) {
	return PurgeResult{
		Submitted: append([]string(nil), urls...),
		Failures:  map[string]error{},
		Requests:  0,
	}, nil
}

// Name returns "noop".
func (NoOpProvider) Name() string { return "noop" }
