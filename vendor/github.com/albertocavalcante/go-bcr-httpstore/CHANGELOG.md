# Changelog

All notable changes to `go-bcr-httpstore` are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.2] - 2026-06-02

Tiny additive release. Exports `Backend.Do` so extension libraries
(notably `go-bcr-artifactory`'s upcoming Properties API) can issue
vendor-specific requests through the Backend's configured auth +
HTTP client without re-implementing transport.

### Added

- **`Backend.Do(ctx, method, relPath, query, headers, body)
  (*http.Response, error)`** — escape hatch for vendor adapters
  with non-BCR path shapes or vendor-specific endpoints
  (Artifactory `?properties=k=v`, Nexus `/service/rest/...`, etc.).
  Auth is applied; Cache is NOT consulted (cache routing stays
  reserved for the BCR-typed Read* methods, which understand which
  path shapes are safe to cache).

### Behavior

- Status-neutral. Non-2xx responses are returned as-is, not
  translated into typed sentinels. Callers map status to their
  own error vocabulary (e.g. `go-bcr-artifactory`'s Properties
  maps 404 → `httpstore.ErrModuleNotFound` for path-not-found,
  401 → `ErrUnauthorized`, etc.).
- `query` is nil-safe — empty Values produces no `?...` suffix.
- `headers` is nil-safe.
- `body` is nil for GET/HEAD/DELETE.
- Caller MUST close `Response.Body` when `err == nil`.

### Why now

The v0.2.0 `Reader` interface let external Layout implementations
read arbitrary paths via `ReadIndex`. But Layout impls aren't the
only kind of extension — vendor adapters often need vendor-
specific endpoints (Artifactory Properties at
`?properties=k=v|...`, Nexus REST API at `/service/rest/v1/...`)
that aren't reachable through `Read*` or `Write*` methods.

`Backend.Do` fills that gap. The internal `do()` helper has done
the work since v0.1.1; this release simply exposes it via a
cleaner public API that drops the internal `*url.URL` return
value (used only for httpstore-internal error wrapping).

Caught while designing `go-bcr-artifactory`'s Properties API:
without `Do`, the package would either re-implement transport
(losing the configured auth + HTTP client) or hardcode a sub-
optimal workaround. Exporting `Do` is the clean fix.

### Tests

5 new tests pushed total to 91 across the package:
- `TestDo_AppliesAuthAndHeaders` — auth + per-call headers
- `TestDo_QueryStringEncoded` — query.Values encoded to URL
- `TestDo_NilQueryProducesNoQueryString` — nil query = no ?
- `TestDo_StatusNeutral_PassesThroughNon2xx` — 404 → response, not error
- `TestDo_BodyPassedThrough` — request body streamed

## [0.2.1] - 2026-06-01

Tiny additive release. Exports the path-generic 404 sentinel so
external `Layout` implementations (notably the new sibling library
`go-bcr-artifactory`) can `errors.Is` 404s uniformly without
string-matching error messages.

### Added

- **`ErrUpstream404`** — was the internal `errHTTP404` sentinel.
  Returned by `Backend.getBytes`, `Backend.ReadIndex`, `Backend.Stat`
  on 404 responses. BCR-typed methods (`ReadMetadataJSON` etc.)
  continue to translate it to the operator-meaningful
  `ErrModuleNotFound` / `ErrVersionNotFound` / `ErrPatchNotFound`.
  External `Layout` implementations can now detect path-generic
  404s via `errors.Is(err, httpstore.ErrUpstream404)`.

### Why now

The v0.2.0 `Reader` interface (designed to let external Layout
implementations live outside httpstore) was incomplete — it
returned errors that callers couldn't pattern-match. The
`go-bcr-artifactory` library's `Layout.ListModules` needs to
soft-fail on 404 of the modules listing, which requires
detecting the 404. Exporting the sentinel is the minimal fix.

This omission was caught while implementing `go-bcr-artifactory`
v0.1.0; it would have been a v0.3.0 breaking change later. v0.2.1
is purely additive — the variable was private before, no caller
broke.

## [0.2.0] - 2026-06-01

Breaking release. Ships the Write surface alongside two interface
cleanups that benefit from grouping with the breaking changes —
`CanopyIndex` relocation + Layout signature narrowing + `Cache.Delete`
for write invalidation.

### BREAKING CHANGES

#### 1. `CanopyIndex` removed from httpstore

It was canopy-specific contract (`apiVersion: "canopy-index/v1"`)
leaking into a substrate-agnostic library. Canopy now declares its
own equivalent Layout in its `internal/backend/canopyindex/` package.
External consumers wanting to read canopy-shape indexes can vendor
the same implementation or write their own.

**Migration:** if you used `httpstore.CanopyIndex{}` as your Layout,
either vendor canopy's `canopyindex` package or write a small Layout
implementing the new interface (see #2). For nginx/Caddy-fronted
stores, `httpstore.HTMLAutoindex{}` is the substrate-generic
alternative.

#### 2. `Layout` signature narrowed to take `Reader` not `*Backend`

Previously Layout implementations received the whole `*Backend` —
including private fields, Cache, Auth credentials. Now they receive
a narrow `Reader` interface:

```go
type Reader interface {
    ReadIndex(ctx context.Context, relPath string) ([]byte, error)
    BaseURL() *url.URL
}

type Layout interface {
    ListModules(ctx context.Context, r Reader) ([]string, error)
    ListVersions(ctx context.Context, r Reader, module string) ([]string, error)
}
```

`*Backend` implements `Reader`, so Backend can still be passed
wherever Reader is expected. Layout implementations are the only
callers affected — they need to swap their parameter type from
`*httpstore.Backend` to `httpstore.Reader`, and call `r.ReadIndex(...)`
instead of `b.getBytes(...)` (which is now exposed publicly under
the documented name).

#### 3. `NewOptions.Layout` no longer defaults

Previously nil Layout silently defaulted to `CanopyIndex{}`. Now
nil Layout returns `ErrInvalidOptions` from `New()`. Operators must
explicitly pick the substrate-appropriate Layout — there is no
universal correct default.

**Migration:** pass `Layout: httpstore.HTMLAutoindex{}` for nginx /
Caddy / Apache-fronted stores, or your own implementation for
custom shapes. There is no library-shipped equivalent to the old
default behavior.

#### 4. `Cache` interface gains `Delete(ctx, key)`

Required for write invalidation. Implementations of `Cache` must
add a `Delete` method (idempotent — absent key is a no-op).
`MemoryCache.Delete` ships in v0.2.0.

**Migration:** if you have a custom `Cache` implementation, add
the `Delete` method.

### Added

#### Reader interface + Backend.ReadIndex

- **`Reader` interface** — `ReadIndex(ctx, relPath) ([]byte, error)`
  + `BaseURL() *url.URL`. The narrow contract Layout receives;
  `*Backend` implements it.
- **`Backend.ReadIndex(ctx, relPath)`** — exported path-generic
  read. Routes through the configured Cache like other reads.
  Designed for Layout implementations (no enforcement; external
  callers can use it too).

#### Write surface

7 typed write methods, all routing through `Backend.do()` (shared
transport helper extracted in v0.1.1):

- `WriteMetadataJSON(ctx, module, body, opts)` — default content-type `application/json`
- `WriteSourceJSON(ctx, module, version, body, opts)` — `application/json`
- `WriteModuleBazel(ctx, module, version, body, opts)` — `text/plain; charset=utf-8`
- `WritePatch(ctx, module, version, patchName, body, opts)` — `text/x-diff`
- `WriteOverlay(ctx, module, version, overlayPath, body, opts)` — `application/octet-stream`
- `WriteBazelRegistryJSON(ctx, body, opts)` — `application/json`
- `WriteBlob(ctx, key, body io.Reader, length int64, opts)` — streaming; `application/octet-stream`; bypasses cache

#### WriteOptions

```go
type WriteOptions struct {
    IfMatch     string  // RFC 7232 If-Match header for conditional writes
    ContentType string  // overrides the method's default Content-Type
}
```

`IfMatch` enables concurrent-publish safety: read the current ETag
via `Stat`, hold it, write with `IfMatch=<read ETag>`. A 412 from
upstream (surfaced as `ErrConflict`) tells you someone else won
the race; retry against the new ETag.

#### New error sentinels

- **`ErrConflict`** — 412 Precondition Failed (IfMatch race loss)
  or 409 Conflict
- **`ErrUnauthorized`** — 401; rotate the credential
- **`ErrForbidden`** — 403; identity known, permission denied

#### Cache invalidation (write-invalidate semantic)

Write methods invoke `cache.Delete(relPath)` on success. The next
read re-fetches from upstream and stores the new ETag. `WriteBlob`
doesn't touch the cache (blobs bypass the cache on read too).

### Tests

86 tests under `-race -count=1`, all green. Coverage includes:

- 18 new write-side tests: per-method path coverage, success
  statuses (200/201/204), error mapping (412/409/401/403/5xx),
  IfMatch header passed verbatim, ContentType override, streaming
  body for WriteBlob, key-path-separator defence
- 2 new MemoryCache.Delete tests: present key removed, absent
  key is no-op
- 1 new test: write invalidates cache + subsequent read
  re-populates with new ETag
- All existing 76 tests still green (interface migration only
  affected layout_test.go internals)

### Removed

- `httpstore.CanopyIndex` type, `canopyIndexFile` struct, related
  constants — ~130 LOC. Lifted to canopy.
- `TestNew_NilLayout_DefaultsToCanopyIndex` and other CanopyIndex
  tests — replaced by `TestNew_NilLayout_IsErrInvalidOptions`.

## [0.1.1] - 2026-06-01

Pure internal refactor — **zero public surface change**. Pays for the
v0.2.0 write surface that lands next: `Write*` methods drop in on top
of a shared transport helper at ~15 LOC each instead of duplicating
the URL+auth+http.Client block.

### Refactored

- **Extracted `Backend.do(ctx, method, relPath, headers, body)`** —
  shared low-level transport helper. URL construction + auth
  application + per-call header injection + HTTP transport in one
  place; response semantics (304 / 404 / non-2xx mapping) stay in
  callers.

- **Extracted `cacheLookup` / `cacheStore`** — nil-safe wrappers
  around the configured Cache. `getBytes` consumes them instead of
  open-coding `if b.cache != nil` blocks.

- **Extracted `infoFromResponse`** — Info construction lifted out
  of Stat; reusable for any future code path that wants to map a
  response to Info (e.g. a future Cache validator that wants to
  HEAD-then-decide before a full GET).

- **Split `backend.go` (479 LOC) → `backend.go` (257) + `read.go`
  (262).** `backend.go` keeps types + constructor + accessors +
  internal helpers + `getBytes`. `read.go` owns Read\* + Stat +
  Exists + ListModules/Versions + Info type. Anticipates v0.2.0's
  `write.go`.

### Effect on call sites (illustrative)

- `getBytes` dropped from ~80 LOC to ~40 — cache+conditional-GET
  semantics still in one place, transport plumbing factored out.
- `Stat` dropped from ~40 LOC to ~20.
- `ReadBlob` dropped from ~30 LOC to ~15.

### Tests

No new test code. The existing 76 tests under `-race -count=1` were
the regression contract for this refactor — they all pass unchanged,
which is exactly what a zero-surface-change refactor must demonstrate.
Body-streaming path through `do` (used by v0.2.0 `Write*`) will be
exercised by the write-surface test suite when it lands.

### Why this slice exists

The v0.1.0 `Cache` wiring added ~30 LOC to `getBytes`. v0.2.0 will
add 7 Write methods, each needing the same URL+auth+transport block.
Without the refactor, that's ~210 LOC of duplicated transport code.
With the refactor, it's ~7 × 15 = ~105 LOC of focused write logic on
top of the shared `do` helper.

Cheaper to extract once than to duplicate seven times.

## [0.1.0] - 2026-06-01

Closes the documented v0.1.x scope: ships the response cache that
turns Stat/Exists (v0.0.5) into the foundation of an ETag-aware
conditional-GET pipeline. With Cache in hand, the library is now
production-shaped — every read can be served from cache when the
upstream confirms freshness via 304, and bandwidth burn drops to
zero on warm paths.

### Added

- `Cache` interface — `Get(ctx, key) (Entry, bool)` and
  `Put(ctx, key, Entry)`. ctx is plumbed through so future
  Cache impls backed by Redis / BoltDB / disk can honour
  cancellation. Implementations MUST be safe for concurrent
  use and MUST defensive-copy `Entry.Body` across the boundary.

- `Entry` struct — `Body []byte`, `ETag string` (verbatim,
  including wrapping quotes and `W/` prefix), `LastModified
  time.Time`, `StoredAt time.Time`. `StoredAt` is stamped by
  `Put` at the call site; callers don't populate it.

- `MemoryCache` — in-process LRU cache, mutex-guarded, with
  `MaxEntries`-bounded eviction. Defensive copies on both Get
  and Put boundaries (so caller mutation of the returned slice
  doesn't poison the store and caller mutation of the input
  slice doesn't poison the store either). Construct via
  `NewMemoryCache(MemoryCacheOptions{MaxEntries: int}) (*MemoryCache, error)`.
  `MaxEntries <= 0` returns `ErrInvalidOptions`. `Len()` is
  exposed for diagnostics and tests; not part of the `Cache`
  interface.

- `NewOptions.Cache` field — nilable. Nil = no caching (the
  v0.0.5 behavior, regression-tested). Non-nil routes every
  `Read*` method through the cache via conditional GET.

### Cache flow

When `b.cache != nil`, the workhorse internal `getBytes` does:

1. `cache.Get` the relPath.
2. If hit and entry has ETag → request adds `If-None-Match`.
3. Send the GET.
4. `304 Not Modified` → return cached body unchanged.
5. `200 OK` → store fresh Entry (with new ETag + body), return body.
6. `404 Not Found` → leave cache untouched, return wrapped `errHTTP404`.
7. Other non-2xx → leave cache untouched, return `ErrUpstreamStatus`.

The cache key is `relPath` (no BaseURL namespacing) — one Cache
instance is intended to be paired one-to-one with one Backend.
Sharing across Backends pointing at different upstreams is
unsafe; doc-warned on the `NewOptions.Cache` field.

### Bypass paths

- `ReadBlob` bypasses the cache. Blobs are streaming
  `io.ReadCloser` responses, typically hundreds of MiB for
  source tarballs; buffering them into memory defeats the
  streaming purpose. Documented; explicit test asserts it.
- `Stat` and `Exists` bypass the cache. They're HEAD probes
  with no body to cache. Documented; explicit test asserts it.

### Tests

18 new tests pushed total to ~76 across cache_test.go +
existing files:

- MemoryCache unit: `NewMemoryCache` rejects zero/negative
  `MaxEntries`; Get-miss returns false; Put-then-Get returns
  body + ETag + non-zero `StoredAt`; LRU evicts oldest at cap;
  Get bumps entry to MRU (Get-then-Put doesn't evict the just-
  Get'd entry); Put-overwrite refreshes; defensive copy on Get
  doesn't poison store; defensive copy on Put doesn't poison
  store; concurrent Put+Get under `-race` doesn't trip.
- Backend+Cache integration: nil cache = v0.0.5 behavior;
  first read populates + second read 304s; upstream ETag
  change refreshes cache; 404 doesn't poison cache; 5xx
  doesn't poison cache; upstream without ETag still caches
  bodies (but no conditional GET possible); Stat+Exists don't
  populate cache; ReadBlob doesn't populate cache; misbehaving
  upstream sending 304 without prior cache hit surfaces
  `ErrUpstreamStatus` rather than deadlocking on cached.Body.

### Closes v0.1.x scope

Per the project memory's "Deferred for later v0.x" v0.1.x
slate (`BearerAuth`, `BasicAuth`, `CustomHeaderAuth`,
`HTMLAutoindex`, `ReadPatch`, `ReadBlob`, `Stat`, `Exists`,
`Cache` interface + `NewMemoryCache`), everything is now
shipped — distributed across v0.0.2 through v0.1.0.

Next: v0.2.x = Write surface + `artifactoryext` sub-package
(week 7 in the canopy library-birth calendar).

## [0.0.5] - 2026-05-31

Rounds out the auth slate and adds the HEAD-based primitives a
future `Cache` implementation needs (validate-by-ETag without
paying the body read cost).

### Added

- `CustomHeaderAuth{HeaderName, Value}` — Sets a vendor-specific
  header for stores that key on something other than
  `Authorization` (Artifactory's `X-JFrog-Art-Api`, GitLab's
  `PRIVATE-TOKEN`, JFrog Xray's `X-Xray-Token`, etc.). Empty
  HeaderName or empty Value returns `ErrEmptyToken`.
  `Name()` returns `"custom-header:<HeaderName>"` so audit logs
  identify which header carried the credential without leaking
  the value.

- `Info{Size, LastModified, ETag}` — Response of `Stat`. ETag is
  preserved verbatim including wrapping quotes (`"strong"`,
  `W/"weak"`, `bare`) since conditional GETs send it unchanged
  to upstreams in `If-None-Match`. Zero fields signal "the
  upstream didn't expose this header" rather than empty values
  matching empty cache entries.

- `Stat(ctx, relPath) (Info, error)` — HEAD probe returning the
  Info above. 404 → wrapped `errHTTP404` (path-generic; callers
  map to module/version/patch). Non-2xx non-404 →
  `ErrUpstreamStatus`. Used by future caches to validate stored
  entries without re-reading bodies.

- `Exists(ctx, relPath) (bool, error)` — Cheap HEAD-based
  predicate. (true, nil) on 2xx, (false, nil) on 404,
  (false, err) on transport / 5xx. The 5xx → error path is
  intentional: callers must not conflate "upstream outage"
  with "artifact deleted".

### Tests

11 new tests pushed total to 58 across `auth_test.go` and
`backend_test.go`:

- CustomHeaderAuth: header verbatim, empty-HeaderName hard-
  fail, empty-Value hard-fail, Name identifies header without
  leaking value.
- Stat: success populates all three fields, missing headers
  leave fields zero (no "empty matches empty cache" confusion),
  ETag format preservation across strong/weak/bare shapes,
  5xx → ErrUpstreamStatus.
- Exists: 200 → true, 404 → false-not-error, 5xx → error so
  callers don't conflate outage with absence.

### Why this slice

This is the v0.1.0 setup. With Stat + Exists in hand, the
upcoming `Cache` interface gets the primitives it needs to
implement true ETag-aware conditional GET semantics — the next
slice can ship NopCache + MemoryCache cleanly without back-
filling support primitives.

CustomHeaderAuth rounds out the auth slate (the four canopy-
portfolio common cases are now all present: Anonymous, Bearer,
Basic, CustomHeader).

## [0.0.4] - 2026-05-31

Unlocks vanilla nginx-fronted (and Caddy `file_server browse`)
BCR mirrors — the dominant third-party deploy shape — without
requiring operators to publish `_canopy_index.json`.

### Added

- `HTMLAutoindex{}` Layout — enumerates modules + versions by
  parsing autoindex HTML pages. Tolerates the three common
  shapes:

  - nginx default: `<pre>` block with `<a href="name/">name/</a>`
  - Caddy `file_server browse`: `<tbody>` with `<tr><td><a href=...>`
  - Apache mod_autoindex: similar to nginx with extra `<img>` icons

  Entries are filtered to directory-shaped hrefs (ending with `/`),
  skipping parent (`../`), current (`./`), dotfiles, query-only
  sort links (`?C=N;O=A`), and absolute URLs (some Caddy themes
  emit them).

  `ListVersions` additionally filters `metadata.json` since it's
  the only non-directory entry expected in a well-formed module
  dir.

  Soft-fail behaviour matches `CanopyIndex` semantics: 404 on the
  `modules/<m>/` dir for `ListVersions` returns
  `ErrModuleNotFound`. Buffer-exceeded errors during parsing
  return `ErrIndexUnreadable`.

  New transitive dependency: `golang.org/x/net/html` (well-
  maintained, MIT-equivalent, ~100 KiB vendored). Pulled in at
  v0.55.0.

### Tests

6 new tests pushed total to 47 across `layout_test.go`:

- `ListModules` against real nginx autoindex output (3 modules
  surface, parent link skipped).
- `ListModules` against Caddy `file_server browse` HTML (table-
  shaped, 2 modules surface).
- `ListModules` soft-fail on missing dir (no crash).
- `ListVersions` filters `metadata.json` while keeping version
  directories (3 versions surface).
- `ListVersions` 404 maps to `ErrModuleNotFound`.
- Skip rules: parent, current, dotfiles, query strings, absolute
  URLs all filtered; only the real module entry survives.

### Caveats documented

HTML parsing across autoindex implementations is inherently
fuzzy. Custom themes, XSL transforms, or directory listings that
return JSON instead of HTML won't enumerate correctly. Operators
who can publish `_canopy_index.json` should prefer `CanopyIndex`
(more predictable, smaller bytes-on-the-wire). HTMLAutoindex is
the right fallback for third-party stores where the operator
controls neither the publishing pipeline nor the listing format.

## [0.0.3] - 2026-05-31

Reaches public-API parity with canopy's `internal/backend.Backend`
interface, unblocking the canopy-side `httpstore` adapter as a
drop-in alternative to `BCRMirror`.

### Added

- `ReadBazelRegistryJSON(ctx) ([]byte, error)` — reads the BCR
  root marker at `<BaseURL>/bazel_registry.json`. Returns the new
  `ErrRegistryJSONNotFound` sentinel on 404 (almost certainly a
  misconfigured BaseURL).

- `ReadOverlay(ctx, module, version, overlayPath)` —
  reads `modules/<m>/<v>/overlay/<overlayPath>`. Overlay paths
  can be relative-nested ("internal/BUILD") and are joined via
  `path.Join`. Returns `ErrOverlayNotFound` on 404. Callers are
  responsible for path-traversal validation against their own
  discipline — the library doesn't reject leading "../".

- `ReadBlob(ctx, key) (io.ReadCloser, error)` — streams an opaque
  blob from `<BaseURL>/blobs/<key>`. Unlike the other reads this
  returns `io.ReadCloser` because blobs are typically source
  tarballs in the tens-to-hundreds of MiB; buffering the full
  body in memory would be wasteful. **Caller MUST Close** the
  returned reader.

  Defence at the boundary: blob keys containing path separators
  (`/`, `\`) are rejected with `ErrBlobNotFound` before any HTTP
  request fires — keys are opaque content-addresses and embedded
  separators are always wrong.

### Sentinel errors added

- `ErrRegistryJSONNotFound` — `ReadBazelRegistryJSON` 404.
- `ErrOverlayNotFound` — `ReadOverlay` 404.
- `ErrBlobNotFound` — `ReadBlob` 404 or rejected key.

### Tests

8 new tests pushed total to 41 across `backend_test.go`:

- `ReadBazelRegistryJSON`: success path with a realistic
  registry-marker body, 404 → `ErrRegistryJSONNotFound`.
- `ReadOverlay`: flat path success, nested-path success
  (`internal/BUILD`), 404 → `ErrOverlayNotFound`.
- `ReadBlob`: streaming success (~13 KiB body, read-full
  round-trip), 404 → `ErrBlobNotFound`, boundary defence
  against keys containing `/`, `..`, `\`.

### Why this slice

Canopy's `internal/backend.Backend` interface has 7 methods:
`GetBazelRegistryJSON`, `GetMetadata`, `GetModuleBazel`,
`GetSourceJSON`, `GetPatch`, `GetOverlay`, `GetBlob`. v0.0.2
shipped 4 of those (Metadata/ModuleBazel/SourceJSON/Patch);
v0.0.3 lifts the other 3. With this slice an `httpstore`-backed
adapter on the canopy side can be a drop-in alternative to
`BCRMirror`, paving the way for nginx-fronted or S3-fronted
canopy deployments without a git clone underneath.

## [0.0.2] - 2026-05-31

### Added

- `BearerAuth{Token: ...}` implements the canopy-standard
  `Authorization: Bearer <token>` scheme. Empty Token at Apply
  time returns the new `ErrEmptyToken` sentinel — operators
  want loud failure when a credential file is missing, not a
  silent anonymous request. `BearerAuth.Name() == "bearer"`;
  the token never appears in Name.

- `BasicAuth{User, Pass}` covers legacy hosts that key on HTTP
  Basic (some Artifactory configs, Forgejo with basic-auth-
  over-HTTPS). Either field empty returns `ErrEmptyToken`.
  Verified round-trips through `net/http`'s `Request.BasicAuth()`
  parser.

- `ReadPatch(ctx, module, version, patchName)` reads
  `modules/<module>/<version>/patches/<patchName>`. Returns
  the new `ErrPatchNotFound` sentinel on 404 — distinct from
  `ErrVersionNotFound` so operators can distinguish "the whole
  version is gone" from "this one patch was removed or
  renamed upstream".

### Sentinel errors added

- `ErrEmptyToken` — Auth implementations refuse to sign a
  request because the credential is unset. Treatable as
  recoverable (re-read file, retry once) by callers wrapping
  Auth in a load-from-file shim.
- `ErrPatchNotFound` — `ReadPatch` 404 condition.

### Tests

8 new tests pushed total to 33 across `auth_test.go` and
`backend_test.go`:

- BearerAuth: sets header verbatim, empty-token hard-fail,
  Name returns "bearer".
- BasicAuth: base64-encodes user:pass, round-trip via stdlib
  parser, table-driven empty-field hard-fail (empty user,
  empty pass, both empty), Name returns "basic".
- ReadPatch: success path with a realistic unified-diff body,
  404 → ErrPatchNotFound.

## [0.0.1] - 2026-05-31

Initial release. Mode-A library #2 per canopy's Plan 27 / Plan 43.

### Added

- Public types: `Backend`, `NewOptions`, with `New(opts) (*Backend, error)`
  constructor that fails fast on missing BaseURL / Auth / HTTP, rejects
  malformed URLs, and refuses non-http(s) schemes (defence against
  file://-traversal-by-config).

- `Auth` interface + `Anonymous{}` implementation. Operators MUST pass
  an `Auth` value explicitly — there is no implicit default, so the
  audit log records the choice rather than treating
  "configured-but-unset" as anonymous. Bearer / Basic / CustomHeader
  defer to v0.1.x.

- `Layout` interface + `CanopyIndex{}` implementation. CanopyIndex
  reads `_canopy_index.json` at BaseURL root with schema:

  ```json
  {
    "apiVersion": "canopy-index/v1",
    "modules": { "bazel_skylib": ["1.6.0", "1.7.0"] }
  }
  ```

  Missing index = soft-fail (returns empty list); malformed JSON or
  wrong `apiVersion` = `ErrIndexUnreadable` hard-fail. Default Layout
  when `NewOptions.Layout` is nil.

- Read methods: `ReadMetadataJSON`, `ReadSourceJSON`, `ReadModuleBazel`.
  404 maps to `ErrModuleNotFound` (module-scoped) or
  `ErrVersionNotFound` (version-scoped) based on what the caller
  asked for. Non-2xx non-404 wraps as `ErrUpstreamStatus` with the
  status code in the message.

- Listing methods: `ListModules`, `ListVersions`. Both delegate to
  the configured `Layout`. Defensive-copy contract on `ListVersions`
  return value — mutating the slice doesn't leak across calls
  (forward-compat with the cache landing in v0.1.x).

- Sentinel errors as `errors.Is` predicates: `ErrInvalidOptions`,
  `ErrModuleNotFound`, `ErrVersionNotFound`, `ErrIndexUnreadable`,
  `ErrUpstreamStatus`.

### Tests

25 tests covering:

- NewOptions validation matrix (missing fields, malformed URL,
  scheme refusal, trailing-slash normalisation).
- Each read method: success path, 404 → correct sentinel,
  5xx → `ErrUpstreamStatus`.
- Auth pass-through: Apply called once per read; AuthName reflects
  the configured scheme.
- Path construction: BaseURLs with subpath prefixes preserve the
  prefix on every read.
- CanopyIndex boundary cases: sorted output, missing index soft-fail,
  malformed JSON / wrong apiVersion hard-fail, defensive-copy of
  versions slice.

### Not yet shipped

The following are designed (Plan 43) but deferred to later v0.x:

- `BearerAuth`, `BasicAuth`, `CustomHeaderAuth` (v0.1.x).
- `HTMLAutoindex` layout for nginx / Caddy autoindex pages (v0.1.x).
- `ReadPatch`, `ReadBlob`, `Stat`, `Exists` (v0.1.x).
- `Cache` interface + `NewMemoryCache` with ETag round-trips (v0.1.x).
- Write surface: `PutBlob`, `PutObject`, `DeleteObject` (v0.2.x).
- `artifactoryext` sub-package for Artifactory generic repos (v0.2.x).
- Plan 21 layered staleness fields on receipts (v0.3.x).
