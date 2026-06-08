# Changelog

All notable changes to `go-cdn-purge` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-06-08

### Changed

- `validateURLs` + `dedupeURLs` moved from `cloudflare.go` to a
  sibling `urls.go` (unexported, no API change). Same package,
  same callers, same semantics — relocation preserves first-
  occurrence-order dedup and whole-batch-reject validation. Future
  CloudFront / Bunny adapters will share these helpers without
  duplicating the contract.

### Added

- `urls_test.go` — direct unit-test coverage for the relocated
  helpers (HTTP/HTTPS accept, whitespace/scheme/host rejects,
  whole-batch reject contract, RFC-3986 case-sensitive dedup,
  single/empty pass-through). The helpers were previously tested
  indirectly via Cloudflare + Fastly Purge tests.

## [0.1.0] - 2026-06-02

**Second CDN provider — proves the `Provider` interface generalises.**

Adds `FastlyProvider` implementing the existing `Provider`
interface for Fastly's purge API. Validates the abstraction
designed in v0.0.1: a different vendor API shape (one URL per
request, path-encoded URL, `Fastly-Key` header) fits the same
interface without changes.

### Added

- **`FastlyProvider`** + **`NewFastly(FastlyConfig{APIToken, ServiceID, HTTPClient, BaseURL})`** — Fastly REST API URL purge:
  - `POST /service/<ServiceID>/purge/<url-encoded-url>`
  - `Fastly-Key: <APIToken>` header
  - Response: `{"status": "ok", "id": "..."}` on success
- **`FastlyProvider.ServiceID()`** accessor for operator diagnostics

### Behavior

- **One URL per request** (Fastly has no batching equivalent to Cloudflare's 30-URL bulk endpoint). Result: `Requests == len(deduped urls)`. Operators with high publish volume should consider Fastly's surrogate-key purge mechanism, which lands in v0.1.x's tag-based-purge slice.
- **Same URL pre-validation** as `CloudflareProvider` — empty / malformed / non-http(s) → `ErrInvalidOptions` at function level (whole-batch reject).
- **Same dedup** before send (saves API quota).
- **Same fail-fast** on call-level errors (rate limit, transport, 5xx) — remaining URLs NOT attempted.
- **Status mapping** identical to Cloudflare (401/403/429/5xx).
- **Per-URL Fastly errors** (200 with `status != "ok"`) land in `PurgeResult.Failures` (function-level error stays nil).

### Why this exists

The v0.0.1 design dossier claimed the `Provider` interface
would generalise to other CDNs. Shipping a second provider
**validates that claim** with code:

- Fastly's API shape is genuinely different from Cloudflare's:
  per-URL endpoint, Fastly-Key header (not Bearer), URL in path
  (not JSON body), no batching, simpler response shape.
- Same `Provider` interface accommodates it without changes.
- Same `validateURLs` + `dedupeURLs` helpers reused.
- Same status-mapping pattern, same `ErrUnauthorized` /
  `ErrForbidden` / `ErrRateLimited` sentinels.

If a future Provider (CloudFront, Bunny, KeyCDN) needs an API
shape the interface CAN'T accommodate, v0.2.x revisits the
abstraction. v0.1.0 confirms the abstraction holds for at least
two real vendors.

### Tests

19 new tests pushed total to 45 under `-race -count=1`. Same
`httptest.Server` mock pattern as Cloudflare:

- `NewFastly` validation (3 required fields)
- Successful single-URL purge with `Fastly-Key` header
- Path contains `/service/<ServiceID>/purge/<url-encoded-url>`
- One URL per request (5 URLs → 5 requests)
- URL pre-validation (empty + non-HTTP scheme)
- Status mapping: 401 → `ErrUnauthorized`, 403 → `ErrForbidden`, 429 → `ErrRateLimited`, 5xx → `ErrUpstreamStatus`
- `status: "error"` response → per-URL failure in `Failures`
- Dedup before send
- Partial-failure on mid-batch rate-limit (Submitted carries successful URLs)
- `Name()` returns "fastly"
- `ServiceID()` accessor
- Empty URL list → no-op success

### Dependencies

Unchanged from v0.0.1 — **zero non-stdlib direct deps**. Fastly's
API is plain HTTPS + JSON, same as Cloudflare.

### What v0.1.0 deliberately does NOT add

- **Surrogate-key purge** (Fastly's Cache-Tag equivalent) — comes with the v0.1.x tag-based-purge slice, alongside Cloudflare Enterprise's tag purge.
- **Soft purge** (`Fastly-Soft-Purge: 1` header for stale-while-revalidate behaviour) — v0.1.x config flag.
- **`purge_all` panic button** — deferred to v0.3.x like Cloudflare's equivalent.

### Roadmap

- v0.1.x: tag-based purging (Cloudflare Cache-Tag + Fastly surrogate-key); built-in retry policy; strict-zone validation
- v0.2.x: CloudFront + Bunny providers
- v0.3.x: purge-everything panic button (per-vendor confirm-type guard)
- v1.0: API stability lockstep with canopy v1.0 (M6 close, week 28)

## [0.0.1] - 2026-06-02

First release. CDN cache-purge library for the canopy portfolio.
**Mode-A library #6 — the final one in canopy's Plan 27 portfolio.**

### Added

#### Provider abstraction

- **`Provider` interface** — `Purge(ctx, urls) (PurgeResult, error)` + `Name() string`. Vendor-specific implementations plug in.
- **`PurgeResult{Submitted, Failures, Requests}`** — structured per-URL successes + failures + count of vendor API calls (after batching).

#### Implementations

- **`NoOpProvider`** — sentinel "no CDN" value type for deployments without a CDN (local dev, on-prem, tests). Returns success with `Requests=0`.
- **`CloudflareProvider`** — Cloudflare REST API URL purge:
  - `NewCloudflare(CloudflareConfig{APIToken, ZoneID, HTTPClient, MaxURLsPerRequest, BaseURL})`
  - POSTs `{"files": [...]}` to `/zones/<ZoneID>/purge_cache` with `Authorization: Bearer <APIToken>` header
  - Auto-batches at `MaxURLsPerRequest` (default 30, free-tier safe; paid tiers can set 500)
  - URL deduplication before send (saves API quota)

#### URL pre-validation (re-review Δ2)

Pre-validates URLs via `net/url.Parse`:
- Empty / whitespace-only → `ErrInvalidOptions`
- Malformed → `ErrInvalidOptions`
- Non-HTTP(S) scheme → `ErrInvalidOptions`
- Empty host → `ErrInvalidOptions`

Whole-batch reject — don't burn rate-limit quota on caller-side bugs.

#### Errors — 6 sentinels

- `ErrInvalidOptions` — config validation OR malformed URL
- `ErrUnauthorized` — HTTP 401 (rotate API token)
- `ErrForbidden` — HTTP 403 (re-review Δ1: HTTP idiom matches httpstore, not gRPC's `ErrPermissionDenied`)
- `ErrRateLimited` — HTTP 429 (back off + retry)
- `ErrUpstreamStatus` — other non-2xx
- `ErrServerUnavailable` — transport / network failure

`ErrZoneIDMismatch` originally planned but dropped at v0.0.1 per re-review Δ3 (reserved for v0.1.x StrictZone work; phantom sentinels are API clutter).

### Behavior

- **Fail-fast on call-level errors** (re-review Δ4): batches that succeed before a 429/5xx/transport-error contribute URLs to `PurgeResult.Submitted`; remaining batches NOT attempted. Caller detects partial purge via `len(Submitted)+len(Failures) < len(input)`.
- **Per-URL Cloudflare errors** (HTTP 200 with `success: false`): land in `PurgeResult.Failures` while function-level error stays nil.
- **Concurrent-safe** (re-review Δ5): `Purge` is safe for concurrent use after construction. Per-token rate limits apply across goroutines — operators wanting coordination wrap a single Provider in a serialised queue.
- **URL dedup is case-sensitive** (re-review Δ8): byte equality across the full URL. Operators wanting hostname normalization do it before passing to Purge.
- **`BaseURL` override is test-only / private-proxy-only** (re-review Δ9): don't override casually.

### Tests

26 tests under `-race -count=1`, all green. In-process `httptest.Server` mock impersonates Cloudflare API; records every request + body for assertion.

Coverage:

- Provider interface conformance + NoOpProvider behavior
- `NewCloudflare` validation (required APIToken / ZoneID / HTTPClient)
- Default `MaxURLsPerRequest = 30`
- Purge round-trip success
- ZoneID in request path
- Bearer auth header
- Auto-batching at 30 (100 URLs → 4 requests)
- Paid-tier batch size 500 (100 URLs → 1 request)
- URL pre-validation: empty, malformed, non-HTTP scheme, missing host
- Status mapping: 401 → ErrUnauthorized, 403 → ErrForbidden, 429 → ErrRateLimited, 5xx → ErrUpstreamStatus
- Cloudflare `success: false` per-URL failures land in `PurgeResult.Failures`
- URL deduplication before send (4 input URLs with 2 duplicates → 1 batch of 2)
- Partial-failure semantics (batch 1 succeeds, batch 2 → 429 → `Submitted` carries batch 1's 30)
- Empty URL list → no-op success
- `Name()` returns `"cloudflare"`
- `ZoneID()` accessor

### Dependencies

**Zero non-stdlib direct deps.** Lightest library in the canopy portfolio. stdlib only: `net/http`, `encoding/json`, `net/url`, `bytes`, `context`, `errors`, `fmt`, `io`, `strings`.

### Re-review applied before code

Per `~/dev/md/2026-06-02-go-cdn-purge-design/05-rereview-2026-06-02.md`, 9 deltas applied pre-code:

1. `ErrPermissionDenied` → `ErrForbidden` (HTTP idiom matches httpstore)
2. URL pre-validation in Purge (avoid burning rate-limit on bad input)
3. `ErrZoneIDMismatch` dropped from v0.0.1 (phantom sentinel)
4. `PurgeResult.Submitted` partial-failure semantics documented
5. Concurrent-Purge contract + shared-rate-limit-budget documented
6. Cloudflare success-response shape uncertainty noted (verify in production)
7. `dedupe.go` separate file → inlined in `cloudflare.go`
8. Dedupe case-sensitivity contract documented
9. `BaseURL` override is test-only / private-proxy-only

### Compatibility

| `go-cdn-purge` | dependencies |
|---|---|
| v0.0.1 | stdlib only |

### Roadmap

- v0.0.2: Corrections from first canopy use (Plan 24 integration)
- v0.1.x: Tag-based purging (Cache-Tag); Fastly provider; built-in retry; strict-zone validation
- v0.2.x: CloudFront + Bunny providers
- v0.3.x: Purge-everything panic button with `Confirm`-type guard
- v1.0: API stability lockstep with canopy v1.0 (M6 close, week 28)

### Portfolio completion

Shipping `go-cdn-purge` v0.0.1 completes the canopy Mode-A portfolio:

| # | Library | Status |
|---|---|---|
| 1 | `go-bcr-mirror` | v0.1.3 |
| 2 | `go-bcr-httpstore` | v0.2.2 |
| 3 | `go-bcr-artifactory` | v0.3.0 |
| 4 | `go-bcr-bundle` | v0.2.0 |
| 5 | `go-reapi-client` | v0.0.2 |
| 6 | `go-cdn-purge` | **v0.0.1 (this)** |

All 6 Mode-A libraries shipped to working v0.x. canopy's Plan 27 (library extraction map) obligation met.

### Design dossier

Full architectural rationale at `~/dev/md/2026-06-02-go-cdn-purge-design/` — 5 design files + 1 re-review addendum (~1,700 lines total).
