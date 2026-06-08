# go-bcr-httpstore

[![go.mod](https://img.shields.io/badge/go-1.26-blue)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/albertocavalcante/go-bcr-httpstore/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/go-bcr-httpstore/actions)

Pure-Go BCR-shape HTTP store client. Reads (and, in later versions, writes) the [Bazel Central Registry](https://registry.bazel.build/) on-disk shape over HTTP from any substrate that speaks it: nginx, S3, R2, MinIO, GCS, Artifactory, GitHub raw, Forgejo raw.

## What it does

A BCR-shape tree organises module metadata at known paths:

```
<root>/modules/<name>/metadata.json
<root>/modules/<name>/<version>/MODULE.bazel
<root>/modules/<name>/<version>/source.json
<root>/modules/<name>/<version>/patches/<name>
```

`go-bcr-httpstore` lets a Go program read this layout from any HTTP store using a single `*Backend`, regardless of whether the substrate is a static-file webserver, an S3-shape object store, an Artifactory generic repo, or a Forgejo raw-view.

Pairs with [`go-bcr-mirror`](https://github.com/albertocavalcante/go-bcr-mirror) (the same shape over a git clone) — pick whichever substrate fits the deployment.

## Status

**v0.0.1.** Pre-1.0 — the public API will shake out as the consumers (canopy first, then external callers) drive it. Every break is recorded in [`CHANGELOG.md`](CHANGELOG.md). Post-v1.0 follows semver.

## Install

```bash
go get github.com/albertocavalcante/go-bcr-httpstore@v0.0.1
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/albertocavalcante/go-bcr-httpstore"
)

func main() {
    b, err := httpstore.New(httpstore.NewOptions{
        BaseURL: "https://my-registry.example/bcr",
        Auth:    httpstore.Anonymous{},
        HTTP:    &http.Client{Timeout: 30 * time.Second},
    })
    if err != nil {
        panic(err)
    }
    meta, err := b.ReadMetadataJSON(context.Background(), "bazel_skylib")
    if err != nil {
        panic(err)
    }
    fmt.Println(string(meta))
}
```

### The `Layout` problem

Plain HTTP has no universal listing protocol. `Layout` lets callers choose how `ListModules` / `ListVersions` resolve:

| Substrate | Recommended Layout |
|---|---|
| nginx / Caddy autoindex | `HTMLAutoindex{}` (v0.1.x) |
| S3 / R2 / GCS / MinIO (canopy-published) | `CanopyIndex{}` ← v0.0.1 default |
| Artifactory generic repo | `VendorList{Provider: artifactoryext.Adapter{...}}` (sub-pkg, v0.2.x) |
| Forgejo raw, GitHub raw | `CanopyIndex{}` |

The default is `CanopyIndex{}` — reads `_canopy_index.json` at the BaseURL root. This is the shape canopy auto-writes on publish, so it's always available and always current when canopy is the publisher.

### Auth

Auth is pluggable via the `Auth` interface. Implementations as of v0.0.2:

- **`Anonymous{}`** — explicit "no auth". Operators must opt in even for public stores, so the audit log records the choice rather than treating "configured-but-unset" as anonymous.
- **`BearerAuth{Token: "..."}`** — canopy-standard `Authorization: Bearer <token>`. Empty Token at Apply time returns `ErrEmptyToken` so a missing credential file fails loudly.
- **`BasicAuth{User, Pass}`** — legacy HTTP Basic. Empty user or pass returns `ErrEmptyToken`.

`CustomHeaderAuth` lands in v0.1.x.

### Errors

Sentinel errors expose stable predicates for `errors.Is`:

- `ErrInvalidOptions` — `New` rejected the constructor argument
- `ErrModuleNotFound` — a metadata.json read or a layout ListVersions found no such module
- `ErrVersionNotFound` — version-scoped read found no such version directory
- `ErrPatchNotFound` — `ReadPatch` found no such patch under an existing version
- `ErrIndexUnreadable` — the layout's discovery index failed to parse / had wrong apiVersion
- `ErrUpstreamStatus` — non-2xx, non-404 HTTP response (wrapped with status code)
- `ErrEmptyToken` — an Auth implementation refused to sign because its credential is unset (BearerAuth empty Token, BasicAuth empty User or Pass)

## Design

Full design lives in canopy's [Plan 43 — `go-bcr-httpstore` deep design](https://github.com/albertocavalcante/canopy/blob/main/docs/plans/43-go-bcr-httpstore-deep-design.md). Pre-1.0 priorities:

- **No canopy types in the public API** — the library exports its own shapes; canopy maps to its internal `api.SystemStatus`-shape types at the call site.
- **HTTP client injected, never constructed** — so consumers can wire their own egress.Client + audit log + lint check without forking the library.
- **TDD throughout** — every public method has at least one `httptest.Server` test covering the success + error paths.
- **Soft-fail on listing** — a missing `_canopy_index.json` returns `(nil, nil)` so the consumer renders "no modules yet" rather than an error page.

## Repos

- **Primary:** [`github.com/albertocavalcante/go-bcr-httpstore`](https://github.com/albertocavalcante/go-bcr-httpstore)
- **GitHub mirror:** [`github.com/albertocavalcante/go-bcr-httpstore`](https://github.com/albertocavalcante/go-bcr-httpstore)

CI runs on the Forgejo primary; the GitHub workflow only fires on `workflow_dispatch` to avoid double-burning CI credits.

## License

MIT. See [`LICENSE`](LICENSE).
