# go-cdn-purge

[![go.mod](https://img.shields.io/badge/go-1.26-blue)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/albertocavalcante/go-cdn-purge/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/go-cdn-purge/actions)

CDN cache-purge library for the canopy portfolio. When canopy publishes new content, CDN-cached URLs must be invalidated so consumers see fresh content instead of stale cached responses.

**Mode-A library #6 — the final one in canopy's Plan 27 portfolio.**

## What it does

| URL shape | Goes stale when | Purge urgency |
|---|---|---|
| `/modules/<m>/metadata.json` | Every new version of `<m>` ships | **HIGH** |
| `/_canopy_index.json` | Every publish event | **HIGH** |
| `/modules/<m>/<v>/source.json` etc | Only on republish | MEDIUM |
| `/blobs/sha256-<hash>` | Almost never (content-addressable) | LOW |

`go-cdn-purge` calls the CDN's purge API to evict stale URLs.

## Install

```bash
go get github.com/albertocavalcante/go-cdn-purge@v0.0.1
```

**Zero non-stdlib direct deps.** Lightest library in the canopy portfolio.

## Quickstart

```go
// Cloudflare:
p, err := cdnpurge.NewCloudflare(cdnpurge.CloudflareConfig{
    APIToken:   os.Getenv("CLOUDFLARE_API_TOKEN"),
    ZoneID:     "your-zone-id",
    HTTPClient: &http.Client{Timeout: 30 * time.Second},
})
if err != nil {
    panic(err)
}

res, err := p.Purge(ctx, []string{
    "https://canopy.example.com/modules/bazel_skylib/metadata.json",
    "https://canopy.example.com/_canopy_index.json",
})
// res.Submitted: URLs the vendor accepted
// res.Failures:  per-URL errors (rare)
// res.Requests:  number of API calls (after batching)

// No CDN configured (local dev, tests, on-prem):
p := cdnpurge.NoOpProvider{}
```

## v0.0.1 surface

| Type | Status |
|---|---|
| `Provider` interface (`Purge`, `Name`) | ✅ |
| `PurgeResult{Submitted, Failures, Requests}` | ✅ |
| `NoOpProvider` — sentinel "no CDN" value type | ✅ |
| `CloudflareProvider` + `NewCloudflare(CloudflareConfig)` | ✅ |
| Auto-batching at `MaxURLsPerRequest` (default 30) | ✅ |
| URL pre-validation (parse + scheme check) | ✅ |
| URL deduplication before send | ✅ |
| 6 sentinel errors | ✅ |
| ByteStream / fastly / cloudfront / bunny | ⏳ v0.1.x+ |
| Tag-based purging (Cache-Tag) | ⏳ v0.1.x |
| Built-in retry policy | ⏳ v0.1.x |
| Purge-everything panic button | ⏳ v0.1.x |

## Behavior

**Auto-batching:** Cloudflare's free tier accepts 30 URLs per request; paid tiers go up to 500. Library batches transparently.

**URL pre-validation:** Empty, whitespace-only, malformed, or non-HTTP(S) URLs are rejected at function-level (`ErrInvalidOptions`) BEFORE any API call. Saves rate-limit quota on caller-side bugs.

**Fail-fast on call-level errors:** If batch 1 of 4 succeeds and batch 2 returns 429, the library returns `(PurgeResult{Submitted: <batch1 URLs>}, ErrRateLimited)` without attempting batches 3-4. Caller compares `len(Submitted)+len(Failures)` against input length to detect partial purge.

**Per-URL Cloudflare errors** (HTTP 200 with `success: false`) land in `PurgeResult.Failures` while the function-level error stays nil.

## Error sentinels (6)

| Sentinel | Maps to |
|---|---|
| `ErrInvalidOptions` | Config validation OR malformed URL in list |
| `ErrUnauthorized` | HTTP 401 (rotate API token) |
| `ErrForbidden` | HTTP 403 (token lacks "Zone: Cache Purge: Purge" scope) |
| `ErrRateLimited` | HTTP 429 (back off + retry) |
| `ErrUpstreamStatus` | Other non-2xx (wraps response body) |
| `ErrServerUnavailable` | Transport / network failure |

HTTP idiom: `ErrUnauthorized` / `ErrForbidden` (matches `go-bcr-httpstore`). gRPC libraries (`go-reapi-client`) use `ErrUnauthenticated` / `ErrPermissionDenied` instead — naming follows each protocol's vocabulary.

## Cloudflare API token

Required permission: **"Zone: Cache Purge: Purge"** scoped to a single Zone.

Operators should:
- Scope tokens narrowly (one zone, one permission)
- Restrict client IPs if known
- Prefer short-lived + rotation
- Never log the token value (the library exposes `Provider.Name()` for audit logs)

## Status

**v0.0.1.** Pre-1.0 — public API will shake out as canopy drives it. Every break recorded in [`CHANGELOG.md`](CHANGELOG.md). Post-v1.0 follows semver.

## Roadmap

| Version | Scope |
|---|---|
| v0.0.1 | NoOp + Cloudflare URL purge + auto-batching + 6 sentinels ✅ |
| v0.0.2 | Corrections from first canopy use (Plan 24 integration) |
| v0.1.x | Tag-based purging (Cache-Tag); Fastly provider; built-in retry; strict-zone validation |
| v0.2.x | CloudFront + Bunny providers |
| v0.3.x | Purge-everything panic button with `Confirm`-type guard |
| v1.0 | API stability lockstep with canopy v1.0 (M6 close, week 28) |

## Design dossier

Full architectural rationale at `~/dev/md/2026-06-02-go-cdn-purge-design/`:

- `README.md` — index + open questions + portfolio-completion note
- `01-purpose-and-scope.md` — published-then-stale problem, URL inventory
- `02-public-api.md` — types + methods
- `03-cloudflare-mapping.md` — Cloudflare API mapping
- `04-implementation-plan.md` — slice plan + risk register
- `05-rereview-2026-06-02.md` — 9 deltas applied **before** code

## License

[MIT](LICENSE) © 2026 Alberto Cavalcante
