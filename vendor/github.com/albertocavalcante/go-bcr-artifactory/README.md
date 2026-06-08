# go-bcr-artifactory

[![go.mod](https://img.shields.io/badge/go-1.26-blue)](go.mod)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/albertocavalcante/go-bcr-artifactory/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/go-bcr-artifactory/actions)

JFrog Artifactory adapter for [`go-bcr-httpstore`](https://github.com/albertocavalcante/go-bcr-httpstore). Implements `httpstore.Layout` against Artifactory's `api/storage/<repo>/<path>` JSON API so a `httpstore.Backend` pointed at an Artifactory generic repo can enumerate Bazel Central Registry modules + versions.

## What it does

Artifactory's "Generic" repository type doesn't expose a directory listing as HTML autoindex; it provides a structured JSON API at `/api/storage/<repo>/<path>` that returns a `children[]` array with folder vs. file discrimination. This library wires that API to `httpstore.Layout` so existing BCR consumers transparently work against Artifactory-fronted stores.

## Install

```bash
go get github.com/albertocavalcante/go-bcr-artifactory@v0.3.0
```

## Quickstart

```go
import (
    "net/http"
    "os"
    "time"

    "github.com/albertocavalcante/go-bcr-httpstore"
    "github.com/albertocavalcante/go-bcr-artifactory"
)

func main() {
    layout, err := artifactory.New("bcr-mirror") // your Artifactory repo name
    if err != nil {
        // empty repo name → httpstore.ErrInvalidOptions
        panic(err)
    }

    backend, err := httpstore.New(httpstore.NewOptions{
        BaseURL: "https://artifactory.example.com/artifactory",
        Auth: httpstore.CustomHeaderAuth{
            HeaderName: "X-JFrog-Art-Api",
            Value:      os.Getenv("ARTIFACTORY_API_KEY"),
        },
        HTTP:   &http.Client{Timeout: 30 * time.Second},
        Layout: layout,
    })
    if err != nil {
        panic(err)
    }

    modules, err := backend.ListModules(ctx)
    // ...
}
```

`backend.ListModules` / `ListVersions` enumerate via Artifactory's storage API; `backend.ReadMetadataJSON` / `ReadSourceJSON` / etc. read BCR-shape content from the Artifactory repo verbatim — Artifactory serves generic-repo content on the bare path, no API prefix needed for the content reads.

## Auth

Artifactory deployments typically use API-key auth via the `X-JFrog-Art-Api` header — pair with `httpstore.CustomHeaderAuth`. Bearer tokens, Basic auth (username + password), and reference tokens also work via the corresponding `httpstore.Auth` implementations:

| Artifactory auth | `httpstore.Auth` |
|---|---|
| API key | `CustomHeaderAuth{HeaderName: "X-JFrog-Art-Api", Value: "<key>"}` |
| Identity token (Bearer) | `BearerAuth{Token: "<token>"}` |
| Username + password | `BasicAuth{User: "...", Pass: "..."}` |
| Reference token | `BearerAuth{Token: "<reference-token>"}` |
| Anonymous / public repo | `Anonymous{}` |

## Why a separate library

JFrog Artifactory's APIs (storage listing, properties, build promotion) are vendor-specific surface that doesn't translate to other substrates. Keeping this code in its own library — rather than as a sub-package of `go-bcr-httpstore` — preserves `httpstore`'s substrate-agnostic posture and lets Artifactory-specific releases ship on their own cadence.

The same pattern applies to any other vendor adapter: if Sonatype Nexus support ever lands, it'd be `go-bcr-nexus`, not `go-bcr-httpstore/nexusext`.

## Scope

All three documented Artifactory surfaces are shipped:

- **v0.1.0** — listing: `Layout.ListModules` / `Layout.ListVersions` against `/api/storage/<repo>/<path>`.
- **v0.2.0** — properties: `GetProperties` / `SetProperties` / `DeleteProperties` against `?properties=k=v[,…]`.
- **v0.3.0** — build promotion: `PromoteBuild(ctx, backend, PromoteOptions)` against `/api/build/promote/<buildName>/<buildNumber>`. Free function (not a method) because promotion carries no per-instance state.

Next slice is consumer-driven: canopy (the first consumer) drives any further surface needs (likely AQL or repository config) when its release-management workflow lands.

## Status

**v0.3.0.** Pre-1.0 — the public API will continue shaking out as canopy drives consumption. Every break is recorded in [`CHANGELOG.md`](CHANGELOG.md). Post-v1.0 follows semver.

## Compatibility

| `go-bcr-artifactory` | `go-bcr-httpstore` |
|---|---|
| v0.3.x | ≥ v0.2.2 |
| v0.2.x | ≥ v0.2.1 |
| v0.1.x | ≥ v0.2.1 |

v0.3.x uses `httpstore.Backend.Do` (exported in v0.2.2) as the escape hatch for vendor-specific requests; v0.1.x / v0.2.x use `httpstore.ErrUpstream404` (added in v0.2.1) for 404 detection.

## License

[MIT](LICENSE) © 2026 Alberto Cavalcante
