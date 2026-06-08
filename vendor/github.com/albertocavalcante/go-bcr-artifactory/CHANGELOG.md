# Changelog

All notable changes to `go-bcr-artifactory` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-06-02

Build Promotion API. Adds the third documented Artifactory surface
canopy's release-management workflow needs: move (or copy) a
build's artifacts from one repo to another, stamping status +
properties at promotion time.

### Added

- **`PromoteOptions`** struct with `BuildName` / `BuildNumber` /
  `SourceRepo` / `TargetRepo` (required) plus `Status` /
  `Comment` / `Copy` / `DryRun` / `Properties` / `Timestamp`
  (optional).

- **`PromoteBuild(ctx, backend *httpstore.Backend, opts PromoteOptions) error`** —
  free function (rather than struct method) because promotion
  doesn't carry per-instance state. POSTs JSON to
  `/api/build/promote/<buildName>/<buildNumber>`.

### Behavior

- Required-field validation fires before the upstream call —
  empty `BuildName` / `BuildNumber` / `SourceRepo` / `TargetRepo` →
  `httpstore.ErrInvalidOptions`. No wasted round-trip on
  misconfiguration.
- Optional fields are omitted from the JSON body when empty
  (Artifactory defaults them).
- `Timestamp` (when non-zero) serialised as RFC3339 UTC.
- `Properties` (when non-empty) serialised as Artifactory's
  native `map[string][]string` shape — same multi-value semantic
  as the Properties API.
- `Content-Type: application/json` sent automatically.

### Status mapping

| Status | Sentinel |
|---|---|
| 200 / 201 / 204 / 202 | nil (committed or DryRun validated) |
| 400 | `httpstore.ErrUpstreamStatus` (validation failure) |
| 401 | `httpstore.ErrUnauthorized` |
| 403 | `httpstore.ErrForbidden` |
| 404 | `httpstore.ErrUpstream404` (build name/number not found) |
| other non-2xx | `httpstore.ErrUpstreamStatus` |

### Why a free function (not a struct)

Promote spans two repos by definition — `SourceRepo` and `TargetRepo`
are per-call, not per-instance. Compare with `Properties` (which
carries a single `repo` on the struct since every operation is
scoped to one repo). A struct-with-no-state pattern would be less
honest than a free function.

### Implementation note

Uses `httpstore.Backend.Do` for the request, same pattern as
Properties — the `/api/build/promote/...` path doesn't fit the
BCR-typed Write* methods and is the canonical vendor-specific
endpoint case `Backend.Do` was designed for.

### Tests

7 new top-level test functions (with subtests pushing actual
assertions to ~19) pushed total package to 38:

- Constructor / validation: nil backend → ErrInvalidOptions; all
  four required-field-empty cases each → ErrInvalidOptions; no
  upstream call made when validation fails.
- Success: POST method, correct `/api/build/promote/<name>/<num>`
  path.
- Body shape: minimal-fields body omits all optionals; all-fields
  body includes them (status, comment, copy, dryRun, timestamp
  RFC3339, properties multi-value).
- Status mapping: 200/201/204 → nil; 401 → ErrUnauthorized;
  403 → ErrForbidden; 404 → ErrUpstream404; 400 + 500 →
  ErrUpstreamStatus.
- Content-Type: `application/json` sent automatically.

### Closes v0.x BCR-relevant Artifactory surface

With Properties (v0.2.0) and Build Promotion (v0.3.0) shipped,
the three documented surfaces canopy's plan called out
(`canopy/docs/plans/28-build-plan-and-maturity.md` Plan 23:
"listing + properties + promotion") are complete. Next library-
local feature work is consumer-driven — the next slice would
respond to canopy's first Artifactory-backed deployment surfacing
a real need.

### Compatibility

| `go-bcr-artifactory` | `go-bcr-httpstore` |
|---|---|
| v0.3.0 | ≥ v0.2.2 |

## [0.2.0] - 2026-06-02

Properties API. Adds read / write / delete for Artifactory's
sidecar key-value metadata system — useful for tagging modules
with build IDs, release status, source provenance, etc.

### Added

- **`Properties` type** — `NewProperties(backend *httpstore.Backend, repo string) (Properties, error)`. Nil backend or empty repo returns `httpstore.ErrInvalidOptions`.

- **`Properties.Get(ctx, relPath) (map[string][]string, error)`** —
  reads all properties at `relPath` under the configured repo.
  Multi-value semantic preserved (Artifactory's native shape: each
  key maps to `[]string`). Empty `properties` field in the response
  returns an empty (non-nil) map so callers can range without
  nil-check.

- **`Properties.Set(ctx, relPath, props map[string][]string) error`** —
  upserts properties. Keys present in `props` replace their existing
  values; keys absent are preserved upstream. To remove keys
  explicitly, use `Delete`. Empty `props` is a no-op (no upstream
  call). Encodes as `?properties=k1=v1|k2=v2a,v2b&recursive=0`
  matching JFrog's documented format.

- **`Properties.Delete(ctx, relPath, keys ...string) error`** —
  removes the named keys. Properties at `relPath` whose keys aren't
  in `keys` are preserved. Empty `keys` is a no-op. Encodes as
  `?properties=k1,k2,k3`.

### Error mapping

All three methods map status to typed sentinels:

| Status | Sentinel |
|---|---|
| 200 / 201 / 204 / 202 | nil |
| 404 | `httpstore.ErrUpstream404` (Get only; Set/Delete on a missing path is operator-meaningful — they may want to error or create the path first) |
| 401 | `httpstore.ErrUnauthorized` |
| 403 | `httpstore.ErrForbidden` |
| other non-2xx | `httpstore.ErrUpstreamStatus` |

### Implementation note

Uses `httpstore.Backend.Do(...)` (added in `httpstore` v0.2.2)
internally to issue requests with the configured auth + HTTP
client. Properties endpoints have non-BCR path shapes
(`/api/storage/<repo>/<path>?properties=...`) and so don't fit
through the Read* / Write* methods directly — `Backend.Do` is the
designed escape hatch for vendor-specific endpoints.

### Tests

17 new tests pushed total to 31:

- Constructor: nil backend rejected, empty repo rejected.
- Get: success + multi-value preservation, `?properties` query
  sent verbatim, 404 → ErrUpstream404, malformed JSON →
  ErrIndexUnreadable, nil properties field → empty map, 401 →
  ErrUnauthorized.
- Set: success + method/path verification, query encoded as
  `k=v|k=v` with sorted keys for determinism, empty props is
  no-op, 403 → ErrForbidden, 5xx → ErrUpstreamStatus.
- Delete: success + method/path verification, keys encoded as
  comma-separated list, empty keys is no-op, 401 →
  ErrUnauthorized.

### Compatibility

| `go-bcr-artifactory` | `go-bcr-httpstore` |
|---|---|
| v0.2.0 | ≥ v0.2.2 |

The library uses `httpstore.Backend.Do` (added in `httpstore`
v0.2.2) for vendor-specific request shapes.

## [0.1.0] - 2026-06-01

First release. Implements `httpstore.Layout` against JFrog Artifactory's
storage API so a `httpstore.Backend` pointed at an Artifactory generic
repo can enumerate Bazel Central Registry modules + versions.

### Added

- **`Layout` type** — implements `httpstore.Layout`. Reads
  `/api/storage/<repo>/<path>` and parses Artifactory's JSON
  `{repo, path, children: [{uri, folder}]}` shape.
- **`New(repo string) (Layout, error)`** — constructor. Empty repo
  returns `httpstore.ErrInvalidOptions` (no useful default; silent
  acceptance would produce confusing 404s at first call).
- **`Layout.Repo() string`** — accessor for diagnostics.

### Behavior

- `Layout.ListModules` reads `/api/storage/<repo>/modules`. Returns
  folder-typed children's names, sorted. **Soft-fail on 404**:
  returns `(nil, nil)` so consumers render "no modules yet" instead
  of an error banner. Mirrors `httpstore.HTMLAutoindex.ListModules`.
- `Layout.ListVersions` reads `/api/storage/<repo>/modules/<module>`.
  Returns folder-typed children's names, sorted. Non-folder entries
  (notably `metadata.json`) are filtered. 404 →
  `httpstore.ErrModuleNotFound`.
- Malformed JSON → `httpstore.ErrIndexUnreadable`.
- URI leading slashes (Artifactory's convention) are stripped before
  returning module / version names.

### Auth pairing

Pair with `httpstore.CustomHeaderAuth{HeaderName: "X-JFrog-Art-Api"}`
for Artifactory API-key auth, or `BearerAuth` / `BasicAuth` for the
other supported schemes.

### Tests

14 tests under `-race -count=1`, all green. Coverage:

- Constructor: empty repo rejected, Repo() accessor.
- ListModules: success+sorted, non-folder filtering, 404 soft-fail,
  malformed JSON hard-fail, URI leading-slash stripping, empty
  children returns empty list.
- ListVersions: success+sorted, metadata.json filter, 404 →
  ErrModuleNotFound, empty children.
- URL construction: actual request path is
  `/api/storage/<repo>/modules`, different repo names produce
  different paths.

### Why a separate library (not a sub-package of httpstore)

Documented in detail at
`~/dev/md/2026-06-01-go-bcr-httpstore-architecture/02-spinoff-analysis.md`
(retrospective decision section). Short version: JFrog Artifactory's
APIs (storage / properties / promotion) are vendor-specific surface
that doesn't translate to other substrates. Keeping this code in
its own library — rather than as `go-bcr-httpstore/artifactoryext`
— preserves `httpstore`'s substrate-agnostic posture and lets
Artifactory-specific releases ship on their own cadence.

Same pattern applies to any future vendor adapter (Nexus → `go-bcr-nexus`,
etc.) — vendor extensions live in vendor libraries, not as sub-packages
of the core.

### Compatibility

| `go-bcr-artifactory` | `go-bcr-httpstore` |
|---|---|
| v0.1.0 | ≥ v0.2.1 |

The library uses `httpstore.ErrUpstream404` (added in `httpstore`
v0.2.1) for 404 detection — earlier `httpstore` versions don't
export this sentinel.

### Scope (deferred)

- **Properties API** (`?properties=k=v`) — v0.2.x, when canopy's
  tagging workflow consumes it. Needs the `httpstore` write surface
  (v0.2.0) to be production-tested first.
- **Build promotion** (`/api/build/promote/...`) — v0.2.x or
  v0.3.x, when canopy's release-management workflow consumes it.
