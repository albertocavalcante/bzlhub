// Package cdnpurge implements CDN cache-purge for the canopy
// portfolio. When canopy publishes new content (a new module
// version, an updated _canopy_index.json, etc.), CDN-cached
// URLs must be invalidated so consumers see fresh content
// instead of stale cached responses.
//
// # Provider abstraction
//
// `Provider` is the CDN-vendor-specific interface. v0.0.1 ships
// two implementations:
//
//   - `NoOpProvider` — does nothing; for deployments without a
//     CDN (local dev, on-prem, tests).
//   - `CloudflareProvider` — Cloudflare REST API purge by URL.
//
// Future implementations (Fastly, CloudFront, Bunny, etc.) land
// at v0.1.x+ as canopy operators ask.
//
// # Quickstart
//
//	// Cloudflare:
//	p, err := cdnpurge.NewCloudflare(cdnpurge.CloudflareConfig{
//	    APIToken:   os.Getenv("CLOUDFLARE_API_TOKEN"),
//	    ZoneID:     "your-zone-id",
//	    HTTPClient: &http.Client{Timeout: 30 * time.Second},
//	})
//	if err != nil { /* handle */ }
//
//	res, err := p.Purge(ctx, []string{
//	    "https://canopy.example.com/modules/bazel_skylib/metadata.json",
//	    "https://canopy.example.com/_canopy_index.json",
//	})
//	// res.Submitted: URLs the vendor accepted
//	// res.Failures: URLs the vendor rejected (per-URL errors)
//	// res.Requests: number of API calls (after batching)
//
// # No CDN
//
//	p := cdnpurge.NoOpProvider{}  // explicit; no nil-Provider sentinel
//
// # Behavior
//
// Cloudflare's purge API accepts up to 30 URLs per request on
// the free tier (500 on paid tiers). The library auto-batches.
//
// URL pre-validation rejects malformed / empty / non-HTTP(S)
// URLs at the function level (whole-batch reject) to avoid
// burning rate-limit quota on caller-side bugs.
//
// On call-level errors (rate-limit, transport, 5xx), the library
// fails fast — remaining batches NOT attempted. Returns partial
// PurgeResult alongside the error so caller can detect "some
// URLs made it, some didn't."
//
// # Cloudflare API token scoping
//
// Operators MUST scope the API token narrowly:
//   - Resources: specific Zone only (NOT "All zones from an account")
//   - Permissions: only "Zone: Cache Purge: Purge"
//   - Client IP filtering: restrict to canopy's egress IPs if known
//   - Token TTL: prefer short-lived + rotation
//
// # Design dossier
//
// Full design rationale at
// ~/dev/md/2026-06-02-go-cdn-purge-design/ including the
// re-review addendum (05-rereview-2026-06-02.md) with 9 deltas
// applied before any code lands.
package cdnpurge
