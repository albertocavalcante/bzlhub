# assay

Static introspection of Bazel modules. Given a module's source tree, produce a structured `ModuleReport` describing what's in it — rules, providers, macros, toolchains, hermeticity profile — without running Bazel.

## What it does

- Parses `MODULE.bazel` via [`go-bzlmod-ast`](https://github.com/albertocavalcante/go-bzlmod-ast) and surfaces:
  - `module()` declaration with `bazel_compatibility` constraints
  - `bazel_dep(...)` entries with `dev_dependency` + `repo_name` annotations
  - `use_extension(...)` chains, including `use_repo` imports + renames
    and per-tag `<local>.<tag>(...)` invocations with typed kwargs
  - All `*_override(...)` forms (single_version, multiple_version, git, archive, local_path)
  - `register_toolchains(...)` and `register_execution_platforms(...)`
  - `include(...)` labels (Bazel 7.2+)
- Walks every `.bzl` and `BUILD`/`BUILD.bazel` file with `go.starlark.net/syntax`, extracts:
  - `rule()` definitions with attribute schemas
    (including `attr.label(providers = [...])` provider-group constraints)
  - `provider()` and `aspect()` definitions
    (full aspect surface: attrs, attr_aspects, required_providers, provides,
    fragments, host_fragments, toolchains, apply_to_generating_rules)
  - `macro()` definitions (top-level exported `def`s)
  - `repository_rule()` definitions
  - `module_extension()` calls with their `tag_class(...)` schemas
  - `toolchain_type()` declarations and concrete `toolchain(...)` registrations
    (pairings of toolchain_type with implementation targets)
- Classifies the module's **hermeticity profile** — `pure-starlark` / `prebuilt-binaries-pinned` / `build-from-source` / `network-fetch-pinned` / `network-fetch-unpinned` / `requires-system-tools` / `repository-rule-arbitrary-code`. Each finding carries provenance (file path + line range).
- Surfaces module assets: README, LICENSE (+ heuristic SPDX name), CHANGELOG path, and CI presence + provider list (`.github/workflows/`, `.forgejo/workflows/`, `.bazelci/`).
- Emits a single `ModuleReport` struct (JSON-serializable; JSON Schema available via `assay schema`).

## Why

The intelligence engine for [canopy](https://github.com/albertocavalcante/canopy) and any other tool that wants to understand Bazel modules from Go without invoking `bazel`. Lib-first; CLI is a thin wrapper.

## Status

v0 working, hardened through multiple iterations. Bazel modules only; a `Dialect` abstraction is in place from day one for future Buck2 support. Validated against 13 real-world modules in the corpus test (rules_cc, rules_go, rules_java, rules_python, rules_jvm_external, bazel-gazelle, contrib_rules_jvm, rules_kotlin, aspect_rules_lint, rules_scala, rules_swift, stardoc, bazel-lib) plus determinism + benchmark gates.

## Corpus testing

A driver test exercises real modules in `$REFS_DIR`:

```bash
REFS_DIR=$HOME/dev/refs go test -run TestCorpus -v
```

The test is skipped if `REFS_DIR` is unset. Per-module summary lines print rule/provider/macro/repo-rule counts + hermeticity classification, useful for spot-checking extraction quality on real input.

## v0 heuristics (known approximations)

- **"Macros"** are top-level exported `def NAME(...)` whose first parameter isn't `ctx`, whose file path doesn't go through `test/`, `tests/`, `examples/`, `vendor/`, `third_party/` (etc.), and whose body either (a) directly calls a rule-instantiating symbol — `native.X(...)`, a `load()`-imported name, a same-file rule binding — or (b) calls another same-file def-macro identified by the same rule (Phase B fixpoint composition). The body-inspection filter, path filter, and fixpoint together cut 45–80%+ of false positives across the corpus depending on module (see `docs/validation.md`, `docs/macro-detection-plan.md`). Macros that only compose private (`_`-prefixed) helpers are still missed; uncommon pattern.
- **Hermeticity `network-fetch-pinned` vs `network-fetch-unpinned`** is determined by whether the `sha256` / `integrity` kwarg is a *literal* string. References like `ctx.attr.sha256` are conservatively flagged as unpinned — the rule itself can't prove pinning.
- **`prebuilt-binaries-pinned`** fires when a fetch call has both `executable = True` and a pinned `sha256` / `integrity` kwarg. "Pinned" recognizes three shapes: a literal string, a same-file all-literal-dict subscript (`INTEGRITY[platform]`), or a cross-file all-literal-dict subscript via `load()` (the canonical bazel-lib pattern where `RELEASED_BINARY_INTEGRITY` lives in `tools/integrity.bzl` and is loaded into the toolchain `.bzl`).
- **`build-from-source`** fires when the module's own `BUILD` / `BUILD.bazel` files (outside `test/`, `examples/`, `vendor/`, `third_party/`, etc.) invoke a compilation rule — `go_binary`, `cc_library`, `java_binary`, `kt_jvm_binary`, `rust_library`, `swift_library`, `py_binary`, `scala_library`, and friends. That signal distinguishes rulesets the consumer's build compiles (rules_go's gobuilder, bazel-gazelle, rules_kotlin's compiler helpers, rules_lint's sarif) from ones whose source exists only for the maintainer's release pipeline (bazel-lib's `copy_directory`, where consumers download the released binary via toolchain). The discriminator: if the module's repository rules download executables from URLs containing its own name (`github.com/bazel-contrib/bazel-lib/releases/...`) AND every compilation rule call lives under `tools/`, the BFS finding is demoted — the consumer never compiles. The two main classes (`prebuilt-binaries-pinned` and `build-from-source`) are orthogonal — `rules_lint` is the canonical hybrid that fires both (pins linter binaries upstream, compiles its sarif converter from source).
- **Attrs extraction tier ladder** — Tier 0 (literal dict) → Tier 1 (same-file symbol fold) → Tier 2 (cross-file `load()` resolution). Each tier is fully deterministic; the per-rule `AttrsExtractionMethod` field tells consumers which tier resolved the attrs (or empty when no tier could). For rules built via helper functions (`MY_ATTRS = build_attrs(); rule(attrs = MY_ATTRS)`), Tier 3 runs the `.bzl` in a sandboxed Starlark interpreter via `assay.WithInterpreterFallback()` — opt-in because it's significantly slower than AST-only.

## Usage

```go
import "github.com/albertocavalcante/assay"

report, err := assay.Analyze(ctx, "/path/to/module-source", assay.WithDialect(dialect.Bazel()))
```

`ctx` bounds the analysis — cancel it to abort the walk at file-granularity. The CLI wires `signal.NotifyContext(os.Interrupt)` so `Ctrl+C` cancels in-progress runs cleanly.

For rules whose attrs Tier 0-2 (literal / same-file fold / cross-file load) can't resolve — e.g. `attrs = make_attrs()` — pass `assay.WithInterpreterFallback()` to enable Tier-3, which runs the `.bzl` in a sandboxed Starlark interpreter (via [`starlark-go-bazel`](https://github.com/albertocavalcante/starlark-go-bazel)) and reads the resulting `RuleClass` globals. Off by default because the interpreter is significantly slower than AST-only extraction; enable for batch indexing where coverage matters more than latency. The CLI exposes it as `--interp`.

CLI:

```bash
assay report /path/to/module-source                     # JSON by default
assay report /path/to/module-source --format=markdown   # human-readable
assay report /path/to/module-source --interp            # opt into Tier-3
assay --version                                          # version + VCS revision
```

## Performance

Indicative numbers on an Apple M4 (warm OS file cache, `count=10`, `benchtime=10x`; baseline re-recorded 2026-05-31 after Rounds D-F + Phase 0):

| Module | Files | sec/op | Allocs/op |
|---|---:|---:|---:|
| testdata/tiny-module | 4 | 0.6ms ±11% | 999 |
| rules_lint | ~500 | 48ms ±3% | 114k |
| rules_go | ~1000 | 74ms ±7% | 144k |
| rules_python | ~2000 | 211ms ±5% | 424k |

The merged-walks refactor (`internal/walkparse`) brought a 4× speedup over the v0.1 per-package walks; benchmarks lock that in. Corpus-scale modules sit at or under the v0.1 baseline post-D-F; the synthetic tiny fixture absorbs the per-module fixed costs from CI presence detection + CHANGELOG read. Full baseline (raw benchstat-compatible output + reproducibility recipe + comparison workflow) lives in [`docs/benchmarks.md`](docs/benchmarks.md). To silence the default stderr noise when embedding as a library or in benchmarks, pass `assay.WithParseErrorHandler(fn)`.

## Deterministic vs heuristic

Every output field is either an exact AST extraction (deterministic) or a pattern-matched best-effort signal (heuristic). Hermeticity findings carry an explicit `Confidence` field (`definitive` / `heuristic`); other fields document their status in godoc. The full audit lives in [`docs/epistemic-status.md`](docs/epistemic-status.md). Consumers (canopy, audit tools) should render heuristic findings with a "best-effort" marker so users know the signal isn't authoritative.

## Naming

`assay` = to determine the composition or quality of something (chemistry/metallurgy). The library determines the composition of a Bazel module. Working name; rename is cheap (single Go module path).

## Two parsers, by design

assay parses Starlark through two distinct libraries because Bazel itself does:

- **MODULE.bazel** goes through [`go-bzlmod-ast`](https://github.com/albertocavalcante/go-bzlmod-ast), a Go port of `bazelbuild/buildtools`. MODULE.bazel is a strictly-typed declarative file Bazel reads through buildtools — same parser, same Span shape, same `<var>.<tag>(...)` extension-call semantics. assay's `modulefile/` package projects the parsed tree onto `report.ModuleReport` through go-bzlmod-ast's `Handler` interface in a single walk.
- **`.bzl` / `BUILD` / `BUILD.bazel`** go through [`go.starlark.net/syntax`](https://pkg.go.dev/go.starlark.net/syntax), the canonical Go Starlark parser. Helpers shared across `bzlwalk/` and `hermetic/` live in the [`go-starlark-syntaxutil`](https://github.com/albertocavalcante/go-starlark-syntaxutil) sibling library (kwarg/ident/load/topdict/path utilities).

The split is intentional — each parser is purpose-built for the file flavor it handles. assay's job is to bridge them: one ModuleReport, two underlying grammars.

## Further reading

See [`docs/README.md`](docs/README.md) for the navigation index over the per-topic reference docs (validation audit, epistemic status, roadmap, refactoring plan, macro-detection plan, determinism contract, benchmarks).

## License

MIT.
