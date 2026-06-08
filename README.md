<h1 align="center">bzlhub</h1>

<p align="center">
  <em>A self-hosted Bazel module registry with introspection, search, drift detection,<br/>and an MCP surface for coding agents — shipping as a single Go binary.</em>
</p>

<p align="center">
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&amp;logoColor=white"></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/License-MIT-blue.svg"></a>
  <img alt="Status: alpha" src="https://img.shields.io/badge/status-alpha-orange">
  <a href="https://bazel.build/external/registry"><img alt="Protocol: BCR HTTP" src="https://img.shields.io/badge/protocol-BCR%20HTTP-1F8B4C"></a>
  <img alt="UI: SvelteKit 2 / Svelte 5 / Tailwind 4" src="https://img.shields.io/badge/UI-SvelteKit%20%2B%20Svelte%205-FF3E00?logo=svelte&amp;logoColor=white">
</p>

<br/>

> [!WARNING]
> **Work in progress.** bzlhub is pre-1.0 software. APIs, CLI flags, on-disk schemas, and HTTP endpoints can change without notice between commits. There is no stable release line yet; the canonical `main` branch is what runs on the demo instance and is what the documentation describes. **Do not run this in front of production traffic.**

---

## What it is

A standalone server that implements the [Bazel Central Registry HTTP protocol](https://bazel.build/external/registry) and indexes every module it serves, so the same process answers `bazel mod show_repo` *and* `where is `cc_binary.linkstatic` defined?`. The substrate is small and explicit:

- **One Go binary.** BCR HTTP, REST + SSE, MCP-over-stdio, and a SvelteKit UI (embedded via `go:embed`) all dispatch into a single `internal/api.Bzlhub` interface. There is no separate registry service to deploy.
- **SQLite via `modernc.org/sqlite`.** No cgo. FTS5 with a trigram tokenizer gives sub-millisecond search across the index.
- **Pluggable storage.** The `internal/backend.Backend` interface today has a filesystem implementation and a federation cascade that proxies to N upstream BCR-shape registries; other backends (git working tree, object store, OCI) are designed but not yet shipped.
- **Static analysis baked in.** The companion library [`assay`](https://github.com/albertocavalcante/assay) extracts rules, providers, macros, repository rules, module extensions, attribute schemas, and a hermeticity profile from each indexed `(module, version)`. The web UI, MCP tools, and CLI are projections of that report.

<details>
<summary><strong>Capabilities at a glance</strong></summary>

| Surface | What it does |
| --- | --- |
| **BCR HTTP** | Serves `/bazel_registry.json`, `/modules/<m>/metadata.json`, `/modules/<m>/<v>/{source.json,MODULE.bazel,patches/*,overlay/*}` to real Bazel clients (validated against 9.1.0). Cascade fallback to N upstreams configurable. |
| **Search** | FTS5-trigram across module / rule / provider / macro names + docstrings. Faceted by hermeticity class. |
| **Module page** | README + auto-Stardoc per rule/provider/macro + attribute schemas + dependency graph + reverse-deps + hermeticity badges. |
| **Diff** | Structural diff between two versions of a module (per-rule, per-attribute, per-provider field), with a closure-wide rollup for the full `bazel_dep` graph. |
| **Drift** | Compare the local mirror against an upstream registry; surface behind / yanked / local-only / unreachable per module. |
| **Compat-check** | Given a `MODULE.bazel` blob, classify each `bazel_dep` against the latest indexed version. Emits a migration plan (Markdown) and a `migrate.sh` script of `buildozer` codemods. |
| **MCP server** | `bzlhub mcp` exposes search, module reports, diff, drift, bump, compat-check, code-nav, and consumers as MCP tools so coding agents can query the registry mid-conversation. |
| **Publish** | `bzlhub publish` writes BCR-shape entries to a target. Three modes: filesystem (default), direct git commit + push, or git commit + push + open a pull request via the configured forge (GitHub, GitLab, Bitbucket DC, Forgejo). |
| **Cross-corpus consumers** | For a given rule / provider / macro / repository rule / module extension, list every call site across the indexed corpus (powered by per-module SCIP indexes generated during ingest). |
| **Air-gap surface** | Compute the full URL set every `repository_rule` / `module_extension` in a closure would fetch, classified by ecosystem (BCR, Maven, PyPI, npm, Go proxy, GitHub releases, OCI, etc.), so an operator can mirror everything required for a sealed build. |

</details>

## Status

The single binary works end-to-end against the public BCR. The list below is **what is committed and tested**; anything not on it is either designed or not started.

- BCR HTTP server, validated by real `bazel mod show_repo` against indexed modules
- SQLite FTS5 index with hermeticity-aware ranking
- REST + SSE + MCP transports over one cohesion interface
- Registry-driven ingest with streaming SHA-256 SRI verification and an extraction cap (decompression-bomb defense)
- Atomic mirror writer with `fsync`-before-rename durability
- Recursive `bazel_dep` closure ingest
- Drift detector (CLI, REST endpoint, UI page)
- Federation cascade across N upstreams with per-upstream response cache, reachability probe loop, and collision audit
- `bzlhub publish` with filesystem / git-direct / git-PR modes against four forges via the [`bigorna`](https://github.com/albertocavalcante/bigorna) library
- Cross-module hover card on Stardoc references; conditional code-nav link gated on cached SCIP availability
- Upstream-body and archive-extraction size caps with sentinel errors throughout the fetch + cascade paths

## Quickstart

```sh
go build -o bzlhub ./cmd/bzlhub

# Mirror bazel_skylib's full closure from upstream BCR.
./bzlhub ingest --recursive \
    --from https://bcr.bazel.build \
    --mirror-to ./mirror \
    --db ./bzlhub.db \
    bazel_skylib@1.7.1

# Serve BCR HTTP + REST/SSE + UI on one port.
./bzlhub serve --root ./mirror --db ./bzlhub.db --addr :8080
#   http://localhost:8080/         SvelteKit UI
#   http://localhost:8080/drift    drift dashboard vs upstream
#   http://localhost:8080/api/*    JSON API
#   http://localhost:8080/modules  module-index endpoint Bazel speaks

# Point Bazel at it.
echo 'common --registry=http://localhost:8080' >> ~/.bazelrc

# Run as an MCP server for a coding agent (stdio).
./bzlhub mcp --db ./bzlhub.db
```

## Architecture

<details>
<summary><strong>Single binary, embedded UI, library substrate</strong></summary>

```
                       +------------------------------+
   bazel client  ----->|  BCR HTTP (/bazel_registry,  |
                       |  /modules/.../source.json,   |
                       |  /modules/.../MODULE.bazel)  |
                       +--------------+---------------+
                                      |
   browser       ----->+--------------v---------------+
   (SvelteKit UI       |                              |
   served via          |     internal/api.Bzlhub      |
   go:embed)           |    (cohesion interface)      |
                       |                              |<---- MCP stdio
                       +---+----------+----------+----+      (coding agents)
                           |          |          |
                           v          v          v
                       backend     store     publish
                       (Cascade    (SQLite    (Filesystem |
                        + File)     + FTS5)    GitDirect  |
                                               GitPR)
                                       |
                                       v
                                    assay
                              (rule/provider/macro
                               extraction, hermeticity
                               profile, ModuleReport)
```

- **`internal/api/bzlhub.go`** is the cohesion interface every transport calls; adding a transport is a thin marshalling shim over those methods.
- **`internal/backend.Backend`** is the read-side abstraction. `File` + `Cascade` ship today; OCI / git / object-store backends are roadmap.
- **`internal/publish.Publisher`** is the write-side abstraction. `FilesystemPublisher`, `GitDirectPublisher`, and `GitPRPublisher` ship today.
- **`internal/store`** owns the SQLite schema (relational tables + FTS5 virtual tables + SCIP blob table) and the search query path.
- **`assay`** (separate repo, vendored via `go.mod replace` in development) is the static analysis engine bzlhub projects from.

</details>

<details>
<summary><strong>Library substrate (composition, not duplication)</strong></summary>

| Layer | Project | What it gives |
| --- | --- | --- |
| Module introspection | [`assay`](https://github.com/albertocavalcante/assay) | `MODULE.bazel` + `.bzl` parsing; ModuleReport with rules / providers / macros / hermeticity |
| Resolution / MVS | [`go-bzlmod`](https://github.com/albertocavalcante/go-bzlmod) | `Resolve()`, registry chain, lockfile read/write |
| BCR wire types | [`go-bcr`](https://github.com/albertocavalcante/go-bcr) | `Source`, `Metadata`, `Maintainer` |
| Starlark eval | [`starlark-go-bazel`](https://github.com/albertocavalcante/starlark-go-bazel) | Bazel-flavored Starlark interpreter for attribute resolution |
| Code navigation | [`understory`](https://github.com/albertocavalcante/understory) | SCIP index read API; symbol lookup + xrefs |
| Forge integration | [`bigorna`](https://github.com/albertocavalcante/bigorna) | GitHub / GitLab / Bitbucket DC / Forgejo client used by `bzlhub publish` |

</details>

## Configuration

<details>
<summary><strong>Federation (multi-upstream cascade)</strong></summary>

bzlhub can sit in front of multiple BCR-shape registries, returning the first one that has each `(module, version)` and surfacing a 503 with `Retry-After` only when every upstream failed transiently. The local mirror is always primary; upstreams cascade in parallel on a local miss; first-200 wins and the remaining probes either cancel (`CANOPY_DISABLE_SHADOW_DETECTION=true`) or complete to populate the collision audit.

```sh
./bzlhub serve --root ./mirror --db ./bzlhub.db --addr :8080 \
    --upstream https://git.example.com/org/bazel-registry/raw/main \
    --upstream https://bcr.bazel.build
```

| Variable | Default | Effect |
| --- | --- | --- |
| `CANOPY_UPSTREAMS` | _(empty — federation disabled)_ | Comma-separated upstream URLs as an alternative to `--upstream`. Each URL must be the directory containing `bazel_registry.json`. |
| `CANOPY_UPSTREAM_CACHE_SIZE` | `1000` | LRU response-cache capacity. Negative integer disables caching entirely. |
| `CANOPY_PROMOTE_ON_SERVE` | `false` | When `true`, every upstream-won `(module, version)` is async-bumped into the local mirror. Changes the mirror from curated to greedy — opt-in only. |
| `CANOPY_UPSTREAM_PROBE_INTERVAL` | `60s` | Background reachability probe interval. `0` or negative disables the loop (boot probe still runs). |
| `CANOPY_DISABLE_SHADOW_DETECTION` | `false` | Cancel runner-up upstreams as soon as a winner returns. Saves N-1 HTTP requests per resolve at the cost of an empty collision-audit row. |

Per-upstream auth is supported by embedding `oauth2:${PAT}` userinfo in the URL; bzlhub strips it at boot, renders `Authorization: Basic` per request, and sanitizes the URL before logging.

Full design: [`docs/plans/16-federation.md`](docs/plans/16-federation.md).

</details>

<details>
<summary><strong>Corporate deployment / security</strong></summary>

The defaults target a single-user laptop install. For multi-tenant or air-gapped deployments, see the documents below.

> **Egress is unrestricted by default.** Without `CANOPY_ALLOWED_HOSTS`, ingest will follow any URL a `source.json` points at (GitHub, S3, arbitrary CDNs). Set the allowlist before exposing ingest to anyone but yourself.

| Variable | Default | Effect |
| --- | --- | --- |
| `CANOPY_ALLOWED_HOSTS` | _(empty — no enforcement)_ | Comma-separated host allowlist for ingest / bump fetches. Exact host (`bcr.bazel.build`) or wildcard subdomain (`*.githubusercontent.com`). |
| `CANOPY_TRUSTED_PROXY_CIDR` | _(empty — header trust disabled)_ | CIDRs of the reverse proxy that authenticates users. `X-Forwarded-User` / `-Email` / `-Groups` headers are honored only for requests inside one of these CIDRs. |
| `CANOPY_INGEST_WRITE_ENABLED` | `true` on dev profile | Master switch for web-driven ingest. Set to `false` to require all writes via CLI / PR. |
| `CANOPY_GITHUB_META_ENABLED` | `false` | Enable GitHub-side enrichment (stars / forks / languages) on a 6-hour cadence. Set `GITHUB_TOKEN[_FILE]` to upgrade from anonymous (60 req/h) to authenticated (5000 req/h). |
| `<NAME>_FILE` | _(per secret)_ | If set, bzlhub reads the secret from the file at this path instead of `$NAME`. Designed for compose / k8s short-lived token mounts. |

- [`docs/plans/08-corporate-security.md`](docs/plans/08-corporate-security.md) — threat model, trust boundaries, auth ladder.
- [`docs/deployment/reverse-proxy-oidc.md`](docs/deployment/reverse-proxy-oidc.md) — running bzlhub behind `oauth2-proxy` + nginx.

</details>

<details>
<summary><strong>Development overrides in <code>go.mod</code></strong></summary>

`go.mod` carries `replace` directives for sibling libraries developed alongside bzlhub. They use **relative paths** so the build works from any clone location, provided the siblings are checked out next to bzlhub (`<workspace>/bzlhub`, `<workspace>/assay`, …):

```
replace github.com/albertocavalcante/assay => ../assay
replace github.com/albertocavalcante/go-bzlmod => ../go-bzlmod
```

`vendor/` carries a snapshot of every dependency, so the plain `git clone && go build` path works without checking out any siblings — Go uses `-mod=vendor` when a `vendor/` directory exists. The `replace` directives only matter for contributors who want to develop against in-progress changes in the sibling repos.

Before publishing bzlhub as a Go module others can `go install`, the replaces must be removed and substituted with real published versions.

</details>

## Documentation

| Document | Scope |
| --- | --- |
| [`docs/ideas.md`](docs/ideas.md) | Full feature surface and rationale |
| [`docs/plan.md`](docs/plan.md) | Phased roadmap |
| [`docs/research.md`](docs/research.md) | Bzlmod internals, lockfile mechanics, Bazel `Version.java` findings |
| [`docs/plans/`](docs/plans/) | Per-feature design notes (federation, corporate security, git-as-backend, airgap, compat analyzer, code-nav, …) |

## Acknowledgements & prior art

bzlhub stands on the shoulders of the broader Bazel registry ecosystem. The work below taught us the field; bzlhub's analysis layer (hermeticity classification, structural breaking detection, migration hints, MVS closure walk, label-aware navigation) builds on top of it.

- [**Bazel Central Registry**](https://github.com/bazelbuild/bazel-central-registry) — canonical source of truth for Bazel modules. The `source.json` + `metadata.json` conventions defined there are what bzlhub's ingestion speaks.
- [**bcr.stack.build**](https://bcr.stack.build/) — official BCR frontend. Reference for clean module-detail-page UX, per-ingest PR provenance, versions cadence notation, and the documented-symbols counter.
- [**registry.build**](https://registry.build/) — third-party BCR browser. Reference for the install-snippet affordance, GitHub social-signal density, and version-dropdown UX.
- [**registry.bazel.build**](https://registry.bazel.build/) — Bazel's official search frontend; the not-found fallback link in bzlhub's UI.

Adjacent ecosystem registries that shaped how "package landing pages" are framed: [pkg.go.dev](https://pkg.go.dev/), [crates.io](https://crates.io/), [docs.rs](https://docs.rs/), [PyPI](https://pypi.org/), [npm](https://www.npmjs.com/).

## License

[MIT](LICENSE). Copyright (c) 2026 Alberto Cavalcante.
