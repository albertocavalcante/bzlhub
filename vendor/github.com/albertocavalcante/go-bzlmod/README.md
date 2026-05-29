# go-bzlmod

[![CI](https://github.com/albertocavalcante/go-bzlmod/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/go-bzlmod/actions/workflows/ci.yml)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=albertocavalcante_go-bzlmod&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=albertocavalcante_go-bzlmod)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=albertocavalcante_go-bzlmod&metric=coverage)](https://sonarcloud.io/summary/new_code?id=albertocavalcante_go-bzlmod)
[![Go Reference](https://pkg.go.dev/badge/github.com/albertocavalcante/go-bzlmod.svg)](https://pkg.go.dev/github.com/albertocavalcante/go-bzlmod)
[![Go Report Card](https://goreportcard.com/badge/github.com/albertocavalcante/go-bzlmod)](https://goreportcard.com/report/github.com/albertocavalcante/go-bzlmod)
[![License](https://img.shields.io/badge/License-MIT%20OR%20Apache--2.0-blue.svg)](LICENSE)

A Go library that resolves Bazel module dependencies the same way Bazel does.

## How Bazel Resolves Dependencies (bzlmod)

Since Bazel 6, the [bzlmod](https://bazel.build/external/module) system manages external
dependencies through `MODULE.bazel` files. When you run `bazel build`, here's what happens
under the hood:

**1. Discovery.** Bazel reads your root `MODULE.bazel`, fetches each `bazel_dep`'s
`MODULE.bazel` from a registry, then recursively fetches their dependencies. This builds
a raw dependency graph containing every version of every module that anyone in the
transitive tree requested.

By default Bazel uses the [Bazel Central Registry](https://registry.bazel.build) (BCR).
You can specify additional or alternative registries with `--registry` flags — Bazel
searches them in order and the first registry where a module is found becomes the source
for **all versions** of that module (module stickiness). This matches
[ModuleFileFunction.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileFunction.java).

**2. Selection.** With the full graph in hand, Bazel applies
[Minimal Version Selection](https://research.swtch.com/vgo-mvs) (MVS) — for each module,
it picks the **highest** version requested by any dependent. But Bazel's selection is more
than textbook MVS:

- **Compatibility levels** — modules declare a `compatibility_level` integer. Two versions
  of the same module with different compatibility levels are treated as incompatible.
  `max_compatibility_level` on a `bazel_dep` allows cross-level upgrades.
- **Overrides** — the root module can pin versions (`single_version_override`), allow
  multiple coexisting versions (`multiple_version_override`), or bypass the registry
  entirely (`git_override`, `local_path_override`, `archive_override`).
- **Strategy enumeration** — when `max_compatibility_level` creates ambiguity (a dep could
  resolve to different compatibility levels), Bazel tries all valid combinations until
  one produces a conflict-free graph.

**3. Pruning.** After selection, Bazel walks the graph from the root and removes modules
that are no longer reachable (because a selected version dropped a dependency). Nodep
edges (from module extensions) participate in selection but don't create transitive edges.

**4. MODULE.tools injection.** Bazel silently adds its own implicit dependencies from
[`src/MODULE.tools`](https://github.com/bazelbuild/bazel/blob/master/src/MODULE.tools) —
things like `rules_java`, `protobuf`, `platforms`, etc. These participate in selection
but are hidden from `bazel mod graph` output by default.

This library implements all four phases as a Go API, verified against 976 of 990 modules
in the Bazel Central Registry across Bazel versions 6.6.0 through 9.1.0.

## Features

- **Bazel Selection Algorithm** — Full compatibility-level and override support ([resolver.go](resolver.go))
- **Multi-Registry** — Chain registries with priority ordering and module stickiness ([registry_chain.go](registry_chain.go))
- **Override Support** — `single_version_override`, `multiple_version_override`, `git_override`, `local_path_override`, `archive_override`
- **Graph Queries** — Dependency paths, explanations, cycle detection ([graph/](graph/))
- **Bazel Compatibility** — Validate `bazel_compatibility` constraints ([bazel_compat.go](bazel_compat.go))
- **Vendor Support** — Resolve from local vendor directories
- **MODULE.tools** — Inject Bazel's implicit dependencies ([bazeltools/](bazeltools/))
- **Nodep Edges** — Parse `repo_name = None` and handle nodep discovery semantics
- **Registry Trace** — Record Bazel-style `registryFileHashes` and source metadata

## Installation

```bash
go get github.com/albertocavalcante/go-bzlmod
```

Requires Go 1.21+.

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/albertocavalcante/go-bzlmod"
)

func main() {
    // Resolve from a file
    result, err := gobzlmod.Resolve(context.Background(),
        gobzlmod.FileSource("MODULE.bazel"),
    )
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Resolved %d modules:\n", result.Summary.TotalModules)
    for _, m := range result.Modules {
        fmt.Printf("  %s@%s\n", m.Name, m.Version)
    }
}
```

## Module Sources

Three ways to specify input:

```go
// From file path
result, _ := gobzlmod.Resolve(ctx, gobzlmod.FileSource("MODULE.bazel"))

// From string content
result, _ := gobzlmod.Resolve(ctx, gobzlmod.ContentSource(`
    module(name = "my_project", version = "1.0.0")
    bazel_dep(name = "rules_go", version = "0.50.1")
`))

// From registry (fetch module by name@version)
result, _ := gobzlmod.Resolve(ctx, gobzlmod.RegistrySource{
    Name:    "rules_go",
    Version: "0.50.1",
})
```

## Options

Configure resolution with functional options:

```go
result, err := gobzlmod.Resolve(ctx, src,
    // Include dev_dependency modules
    gobzlmod.WithDevDeps(),

    // Use custom registries (mirrors Bazel's --registry flag)
    // Searched in order; once a module is found in a registry,
    // all versions of that module come from that registry.
    gobzlmod.WithRegistries(
        "https://my-registry.example.com",  // Check first
        "file:///local/mirror",             // Then local
        gobzlmod.DefaultRegistry,           // BCR fallback
    ),

    // Set request timeout
    gobzlmod.WithTimeout(30*time.Second),

    // Check for yanked versions
    gobzlmod.WithYankedCheck(true),
    gobzlmod.WithYankedBehavior(gobzlmod.YankedVersionWarn),
)
```

See [Resolution Options](docs/resolution-options.md) for all options.

## Custom MODULE.tools Data

By default, `WithBazelVersion(...)` uses the built-in `bazeltools` mapping for
implicit `MODULE.tools` dependencies. If you need to support a newer Bazel
release, a fork, or a moving HEAD build before this library is updated, provide
your own lookup:

```go
import (
    "strings"

    "github.com/albertocavalcante/go-bzlmod"
    "github.com/albertocavalcante/go-bzlmod/bazeltools"
)

result, err := gobzlmod.Resolve(ctx, src,
    gobzlmod.WithBazelVersion("10.1.0-head.20260414"),
    gobzlmod.WithBazelToolsLookup(func(version string) []bazeltools.ToolDep {
        if strings.HasPrefix(version, "10.1.0-head.") {
            return []bazeltools.ToolDep{
                {Name: "rules_cc", Version: "0.1.1"},
                {Name: "platforms", Version: "0.0.11"},
            }
        }

        // Fall back to the built-in table for normal releases.
        return bazeltools.LookupDeps(version)
    }),
)
```

If you want to fully replace the built-in mapping for a known Bazel version,
return your fork-specific dependencies directly and skip the fallback.

If you only want to patch the built-in or lookup-provided list, use
`WithBazelToolsTransformer(...)` instead:

```go
result, err := gobzlmod.Resolve(ctx, src,
    gobzlmod.WithBazelVersion("8.0.0"),
    gobzlmod.WithBazelToolsTransformer(func(version string, deps []bazeltools.ToolDep) []bazeltools.ToolDep {
        deps = bazeltools.SetToolDep(deps, bazeltools.ToolDep{
            Name:    "rules_python",
            Version: "0.40.1-fork.1",
        })
        deps = bazeltools.SetToolDep(deps, bazeltools.ToolDep{
            Name:    "corp_internal_rules",
            Version: "1.2.3",
        })
        deps = bazeltools.RemoveToolDep(deps, "buildozer")
        return deps
    }),
)
```

## Registry Trace And Lockfile Export

Enable registry tracing when you need Bazel-style registry metadata for mirroring
or lockfile generation:

```go
result, err := gobzlmod.Resolve(ctx, src,
    gobzlmod.WithRegistryTrace(),
)
if err != nil {
    log.Fatal(err)
}

for url, hash := range result.RegistryFileHashes {
    if hash == nil {
        fmt.Printf("%s -> not found\n", url)
        continue
    }
    fmt.Printf("%s -> %s\n", url, *hash)
}

lf := result.ToLockfile()
if err := lf.WriteFile("MODULE.bazel.lock"); err != nil {
    log.Fatal(err)
}
```

When `WithRegistryTrace()` is enabled:

- `ResolutionList.RegistryFileHashes` records canonical `MODULE.bazel`, `source.json`,
  and `bazel_registry.json` URLs touched during resolution.
- Nil values represent explicit "not found" probes, matching Bazel's lockfile
  semantics for higher-priority registries that miss before fallback succeeds.
- `ModuleToResolve.Source` is populated for registry-backed modules.

## Dependency Graph

Query the resolved graph ([graph/query.go](graph/query.go)):

```go
result, _ := gobzlmod.Resolve(ctx, src)
g := result.Graph

// Explain why a module is at its version
explanation, _ := g.Explain("protobuf")
fmt.Println(explanation.RequestSummary)

// Find all paths from root to a module
chains, _ := g.WhyIncluded("protobuf")
for _, chain := range chains {
    fmt.Println(chain.String())  // root@1.0.0 -> rules_go@0.50.1 -> protobuf@3.19.6
}

// Check for cycles
if g.HasCycles() {
    cycles := g.FindCycles()
    fmt.Printf("Found %d cycles\n", len(cycles))
}

// Get graph statistics
stats := g.Stats()
fmt.Printf("Total: %d, Direct: %d, Transitive: %d\n",
    stats.TotalModules, stats.DirectDependencies, stats.TransitiveDependencies)

// Export formats
dotGraph := g.ToDOT()      // Graphviz DOT
jsonGraph, _ := g.ToJSON() // Bazel-compatible JSON
textGraph := g.ToText()    // Human-readable tree
```

See [Graph API](docs/graph-api.md) for complete documentation.

## Packages

| Package                     | Description                                     |
| --------------------------- | ----------------------------------------------- |
| [`gobzlmod`](.)             | Main API: `Resolve`, `Parse`, core types        |
| [`ast`](ast/)               | MODULE.bazel AST parsing                        |
| [`graph`](graph/)           | Dependency graph construction and queries       |
| [`label`](label/)           | Bazel label parsing (`@repo//pkg:target`)       |
| [`lockfile`](lockfile/)     | `MODULE.bazel.lock` parsing and generation      |
| [`registry`](registry/)     | Registry client and types                       |
| [`selection`](selection/)   | Bazel selection algorithm (MVS + compat levels) |
| [`bazeltools`](bazeltools/) | MODULE.tools implicit dependencies              |

See [Package Architecture](docs/packages.md) for details.

## Bazel Parity

The resolver implements Bazel's full selection algorithm including:

- Compatibility-level enforcement and `max_compatibility_level` constraints
- `multiple_version_override` with strategy enumeration
- Two-phase graph walking (nodep validation + pruning)
- `dev_dependency` only honored on root module; transitive dev edges ignored
- Non-registry overrides (`git_override`, `local_path_override`, `archive_override`) resolve as versionless modules
- `bazel_dep(..., repo_name = None)` treated as nodep edge (selection only, no transitive deps)
- Implicit `MODULE.tools` dependency injection per Bazel version

Verified against 976 of 990 modules in the [Bazel Central Registry](https://github.com/bazelbuild/bazel-central-registry) via `file://` resolution (~11 seconds, zero failures).

Reference: [Selection.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java), [Discovery.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Discovery.java)

### Multi-registry behavior

This library mirrors Bazel's `--registry` flag semantics
([ModuleFileFunction.java](https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileFunction.java)):

- Registries are searched **in order** (first match wins)
- **Module stickiness** — once a module is found in a registry, all versions of that
  module come from that registry
- Supports `https://`, `http://`, and `file://` URL schemes
- Each module name gets independent sticky-mapping (module A from Reg1, module B from Reg2)

The library also provides **enhanced resilience** beyond Bazel's current behavior: if a
cached registry returns a server error (HTTP 5xx, TLS failure, timeout), the library falls
back to the next registry instead of failing immediately. This addresses known Bazel issues
including [#26442](https://github.com/bazelbuild/bazel/issues/26442) (missing `source.json`
not falling back) and [#28101](https://github.com/bazelbuild/bazel/issues/28101) (BCR TLS
outage causing hard failures).

## Known Limitations

### Starlark expressions in MODULE.bazel

The parser handles literal string values in `bazel_dep()` and override declarations.
It does **not** evaluate Starlark expressions. Modules that compute versions dynamically
will resolve with empty versions, causing resolution to fail.

**Affected BCR modules** (3 of 990):

| Module         | Expression                                          | Evaluates to     |
| -------------- | --------------------------------------------------- | ---------------- |
| `rules_kotlin` | `version = ".".join(proto_version.split(".")[-2:])` | `"33.4"`         |
| `lanelet2`     | `version = BOOST_VERSION + ".bcr.2"`                | `"1.89.0.bcr.2"` |
| `cel-cpp`      | `version = ANTLR4_VERSION`                          | `"4.13.2"`       |

Bazel resolves these correctly because it evaluates Starlark. This library's parser
treats MODULE.bazel as a structured format and extracts literal values only.

### Workspace-relative local_path_override

Modules that are sub-modules in a monorepo often declare `local_path_override`
with relative paths pointing to sibling directories (e.g., `path = ".."`).
These paths only exist within the module's git workspace. When resolving
MODULE.bazel content in isolation (e.g., from a BCR clone or as a string),
these overrides will fail because the referenced paths don't exist.

This affects 11 of 990 BCR modules. The library handles `local_path_override`
correctly when paths exist — the limitation is environmental, not algorithmic.

## Documentation

- [Getting Started](docs/getting-started.md) — Installation and basic usage
- [Resolution Options](docs/resolution-options.md) — All configuration options
- [Graph API](docs/graph-api.md) — Query dependency relationships
- [Bazel Compatibility](docs/bazel-compatibility.md) — Version constraint validation
- [Package Architecture](docs/packages.md) — Package organization

## Testing

```bash
go test ./...              # Unit tests
go test -cover ./...       # With coverage
go test -race ./...        # Race detection
```

### BCR corpus test

Resolves the latest version of every module in a local [Bazel Central Registry](https://github.com/bazelbuild/bazel-central-registry) clone using `file://` registry (no network, no Bazel needed):

```bash
cd e2e
GO_BZLMOD_E2E_BCR_CORPUS=1 \
GO_BZLMOD_E2E_BCR_PATH=/path/to/bazel-central-registry \
  go test -run TestE2E_BCRCorpus -v
```

### Release matrix parity

Compares library output against golden files generated from real `bazel mod graph --output=json` across 37 Bazel versions (6.6.0 through 9.1.0):

```bash
cd e2e
GO_BZLMOD_E2E_RELEASE_MATRIX=1 go test -run TestE2E_BazelReleaseMatrix -v
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and guidelines.

## License

Licensed under [Apache 2.0](LICENSE-APACHE) or [MIT](LICENSE-MIT) at your option.
