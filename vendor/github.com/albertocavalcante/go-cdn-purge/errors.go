// Package cdnpurge implements CDN cache-purge for the canopy
// portfolio. See doc.go for the package overview; design dossier
// at ~/dev/md/2026-06-02-go-cdn-purge-design/.
package cdnpurge

import "errors"

// Sentinel errors. Callers compare with errors.Is. The library
// wraps these with operator-meaningful context (URL, vendor
// error message, HTTP status) at the call site, so the unwrapped
// sentinel is the stable predicate even when the wrapped message
// changes.
//
// 6 sentinels as of v0.0.1 — see
// ~/dev/md/2026-06-02-go-cdn-purge-design/05-rereview-2026-06-02.md
// for the rationale on ErrForbidden naming (re-review Δ1: HTTP-
// shaped library matches httpstore, not gRPC's ErrPermissionDenied)
// and the dropped ErrZoneIDMismatch (re-review Δ3: reserved for
// v0.1.x StrictZone work).
var (
	// ErrInvalidOptions — CloudflareConfig validation failed at
	// NewCloudflare; OR a per-URL Purge call has an empty /
	// whitespace-only / malformed / non-http(s)-scheme URL.
	//
	// Pre-validation per re-review Δ2: invalid URLs fail the
	// whole Purge at function level (don't burn rate-limit quota
	// on malformed input).
	ErrInvalidOptions = errors.New("cdnpurge: invalid options")

	// ErrUnauthorized — vendor rejected our credentials (HTTP 401
	// for Cloudflare). Rotate the API token.
	//
	// Naming follows HTTP idiom (matches httpstore's
	// ErrUnauthorized); reapi-client uses ErrUnauthenticated
	// because gRPC's status code is codes.Unauthenticated.
	ErrUnauthorized = errors.New("cdnpurge: unauthorized")

	// ErrForbidden — credential accepted but lacks permission
	// (HTTP 403 for Cloudflare; usually a token without "Zone:
	// Cache Purge: Purge" scope).
	//
	// Naming follows HTTP idiom (matches httpstore's
	// ErrForbidden); reapi-client uses ErrPermissionDenied
	// because gRPC's status code is codes.PermissionDenied.
	ErrForbidden = errors.New("cdnpurge: forbidden")

	// ErrRateLimited — vendor rate limit hit (HTTP 429 for
	// Cloudflare). Caller should back off + retry; library
	// doesn't implement built-in rate limiting at v0.0.1.
	//
	// Cloudflare's documented purge rate limit: 1000 requests
	// per 5 minutes per token.
	ErrRateLimited = errors.New("cdnpurge: rate limited")

	// ErrUpstreamStatus — vendor returned a non-2xx status that
	// isn't 401/403/429. Body content (the vendor's error
	// message) is wrapped in the returned error for operator
	// diagnostics.
	ErrUpstreamStatus = errors.New("cdnpurge: upstream non-ok status")

	// ErrServerUnavailable — transport failure (DNS, TCP, TLS,
	// timeout). Wraps the underlying net error.
	ErrServerUnavailable = errors.New("cdnpurge: server unavailable")
)
