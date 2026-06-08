# Changelog

All notable changes to assay will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Sections used per release (Keep-A-Changelog v1.1.0): Added, Changed,
Deprecated, Removed, Fixed, Security. Empty sections are omitted.

## [Unreleased]

This section accumulates changes targeting the next minor release (v0.2.0).
Round D of [`docs/registry-surface-plan.md`](docs/registry-surface-plan.md)
expects to land:

- Bzlmod registry essentials: tag-class extraction, dev-dependency flag,
  full MODULE.bazel surface (use_extension / use_repo / register_toolchains /
  overrides / include), toolchain registrations, attr provider constraints.
- Output enrichment: aspect completeness, JSON Schema export.
- Asset enrichment: CHANGELOG detection, CI presence.

Entries below are appended as the work lands; v0.2.0 is tagged once Round D
+ E + F complete.

### Added

- `ModuleKey.DevDependency` (bool) and `ModuleKey.RepoName` (string)
  projected from `bazel_dep(..., dev_dependency = True, repo_name = "...")`.
  Consumers can now split runtime vs dev dependencies and surface the
  local alias when a module imports a dep under a different name (Round D2).
- `TagClassSpec` (new type) and `ModuleExtSpec.TagClasses` populated.
  `tag_class(...)` bindings discovered via a per-file pre-scan and
  resolved through `module_extension(tag_classes = {...})`'s Ident-valued
  dict entries. Attrs follow the existing tier ladder (literal /
  symbol_fold / load_resolve); each TagClassSpec carries its own
  `AttrsExtractionMethod`. rules_python's `python` extension now
  surfaces 5 tag classes (previously zero) (Round D1).
- `ModuleReport.Overrides` (`[]ModuleOverride`), `ModuleReport.UsedExtensions`
  (`[]ExtensionUse`), `ModuleReport.RegisteredToolchains` (`[]string`),
  and `ModuleReport.RegisteredExecutionPlatforms` (`[]string`) populated
  via a second AST pass over MODULE.bazel. Captures `archive_override`,
  `git_override`, `single_version_override`, `multiple_version_override`,
  `local_path_override`, `use_extension`+`use_repo`+`<local>.<tag>(...)`
  chains, and `register_{toolchains,execution_platforms}` positionals.
  Tag-invocation kwargs are split into `kwargs` / `kwarg_lists` /
  `kwarg_bools` / `kwarg_ints` so consumers can render typed values
  without re-parsing strings (Round D3).
- `syntaxutil.IntKeywordArg` and `syntaxutil.PositionalStrings` helpers
  for extractors that read int kwargs and verbatim positional string
  literals (Round D3 supporting infrastructure).
- `ToolchainImpl` (new type) and `ModuleReport.ToolchainImpls`
  populated from BUILD-file `toolchain(...)` registrations. Captures
  Name, ToolchainType, ToolchainImpl, ExecCompatibleWith,
  TargetCompatibleWith, TargetSettings — the concrete pairing of a
  toolchain_type with an implementation target. Distinct from
  `Toolchains` (the toolchain_type declarations) and from
  `RegisteredToolchains` (MODULE.bazel's register_toolchains labels)
  (Round D4).
- `Dialect.IsToolchainSymbol` — Bazel returns true for "toolchain",
  Buck2 returns false (Round D4).
- `AttrSpec.ProviderGroups` (`[][]string`) populated from
  `attr.label(providers = ...)` and `attr.label_list(providers = ...)`.
  Encodes the disjunction-of-conjunctions shape Bazel actually
  supports: outer slices are OR alternatives, inner slices are AND
  requirements within an alternative. Wired through all three of
  Tier-0 (literal extractAttrs), Tier-1/2 (symbol-fold
  dictEntriesToAttrs), and Tier-3 (interpreter attrsFromRuleClass)
  so coverage is uniform across the attrs tier ladder. rules_go's
  go_library now surfaces `providers = [GoInfo]` on `deps` and `embed`
  (previously empty) (Round D5).
- `AspectSpec` extension: `Attrs`, `AttrsExtractionMethod`, `Provides`,
  `Fragments`, `HostFragments`, `Toolchains`, `ApplyToGeneratingRules`
  added. Aspect attrs flow through the same `extractAttrsWithFold`
  Tier 0/1/2 ladder as rules; method tag records which tier resolved
  them. rules_go's `go_pkg_info_aspect` now surfaces 2 attrs
  (previously empty) (Round E1).
- `syntaxutil.IdentListKeywordArg` helper for `kwarg = [Ident, ...]`
  list extraction. Used by `provides` and `required_providers` (E1
  supporting infrastructure).
- `report.SchemaJSON()` and `assay schema` subcommand: hand-maintained
  JSON Schema (Draft 2020-12) for ModuleReport, ~360 lines covering
  20 top-level fields + 19 nested types. Embedded via `go:embed`;
  `$id` points at the GitHub raw URL. `assay schema` prints to
  stdout for piping into validators or TypeScript generators.
  Pure-stdlib drift guard: reflect-walk tests in `report/` catch
  schema/struct misalignment without an external validator dep
  (Round E2).
- `ModuleAssets.Changelog` and `ModuleAssets.ChangelogPath` populated
  from CHANGELOG.md / CHANGELOG.markdown / CHANGELOG.rst /
  CHANGELOG.txt / CHANGELOG / CHANGES.md / CHANGES / HISTORY.md /
  HISTORY in that preference order. Capped at 256KB like README.
  rules_python's CHANGELOG.md surfaces; canopy can now render the
  version-to-version delta inline on the registry page (Round F1).
- `ModuleAssets.HasCI` (bool) and `ModuleAssets.CIProviders` ([]string)
  populated by checking for non-hidden files under `.github/workflows/`,
  `.forgejo/workflows/`, and `.bazelci/`. Provider names are emitted
  alphabetized ("bazelci", "forgejo", "github"). Empty-workflows-dir
  and hidden-only-entry cases (`.gitkeep`) correctly skip. rules_python
  surfaces ["bazelci", "github"]; rules_go surfaces ["bazelci"]
  (Round F2).
- `ModuleReport.Includes` (`[]string`): labels referenced by
  `include("//path:MODULE.bazel")` statements (Bazel 7.2+) are now
  emitted verbatim. The included fragments themselves are not
  recursively parsed; consumers can decide whether to chase or just
  report. Resolves the prior "Known limitation" entry (Phase 0D).
- `ExtensionUse.RenamedRepos` (`map[string]string`): captures the
  rename kwarg form of `use_repo`,
  `use_repo(ext, my_alias = "remote_repo")`. Keyed by local alias.
  Resolves the prior "Known limitation" on use_repo rename kwargs
  (Phase 0D).

### Fixed

- `AspectSpec.RequiredProviders` is now populated. The field was
  previously always empty because the extraction went through
  `StringListKeywordArg` but Bazel passes provider symbols as Idents,
  not strings — every entry was silently dropped. Round E1 switches
  to `IdentListKeywordArg`. JSON shape unchanged.
- Aspect Tier-3 (interpreter fallback for `attrs = make_helper()`-
  style aspects) now hydrates. Was blocked on `starlark-go-bazel`'s
  `aspect()` builtin rejecting the `AttrDescriptor` type that the
  `attr.*` module produced; upstream M0 (plans 07 + 08) unified the
  type via the `AttrDescriptorHolder` interface. assay's vendor
  refresh picks up the fix; aspects with attrs that defeat Tier 0/1/2
  (`attrs = make_attrs()`, `attrs = A | B if cond else C`) now
  hydrate via Tier 3 the same way rules do.

### Changed

- **BREAKING**: `ModuleExtSpec.TagClasses` is `[]TagClassSpec` rather than
  `[]string`. JSON key unchanged (`tag_classes`); each entry is now an
  object with `name`, `doc`, `attrs`, `attrs_extraction_method`, and
  `provenance`. The old field was always empty in practice (no consumer
  surface ever read it as `[]string`), but the Go type and JSON shape
  are technically incompatible. Canopy migration: iterate as objects
  and read `.name`.
- **BREAKING**: `AttrSpec.Providers` (`[]string`) is renamed to
  `AttrSpec.ProviderGroups` (`[][]string`); JSON key changes from
  `providers` to `provider_groups`. The old field was always empty
  (extraction was never wired up), so no data is lost. New shape
  preserves the AND/OR distinction Bazel actually models. Canopy
  migration: rename consumer; flatten with `groups.flatMap(g => g)`
  if a flat list is wanted for display.
- Internal: `modulefile/` now drives MODULE.bazel projection through
  the [`go-bzlmod-ast`](https://github.com/albertocavalcante/go-bzlmod-ast)
  Handler interface in a single parse + single walk. Replaces the
  prior dual-parse architecture (go-bzlmod's `ModuleInfoCollector`
  for module/bazel_dep/overrides + a separate `go.starlark.net/syntax`
  AST pass in `surface.go` for the remaining surface). No
  user-visible output change; both parsers now use buildtools-based
  parsing under the hood, which is the correct fit for MODULE.bazel
  per the [two-parser design note](README.md) (Phase 0D).
- Internal: `internal/syntaxutil` package removed. Its public
  Starlark/syntax helpers moved to the [`go-starlark-syntaxutil`](https://github.com/albertocavalcante/go-starlark-syntaxutil)
  sibling library; the remaining MODULE.bazel-only helpers are now
  encapsulated inside go-bzlmod-ast (Phase 0D).

### Known limitations

- Tag-class attrs that need Tier-3 (interpreter fallback) are not
  hydrated: `interp.Hydrate` walks `Rules` and `RepositoryRules` only.
  Real-world tag classes overwhelmingly use literal attr dicts (verified
  across rules_python, bazel-gazelle, rules_go), so Tier 0/1/2 covers
  the corpus. Tracked for a follow-up round.
- Tag-invocation kwargs whose values aren't string / list-of-string /
  bool / int literals are dropped (matching the existing attrs-
  extraction policy: best-effort, no error).

### Migrating from 0.1.x to 0.2.0

Three breaking changes; each has a concrete migration recipe below.

#### Recipe 1 — `module_extensions[].tag_classes` shape change

`tag_classes` was `string[]` (and always empty, because extraction
was never implemented). It is now
`[{name, doc, attrs, attrs_extraction_method, provenance}]`.

```typescript
// Before (v0.1.x):
for (const tagClassName of moduleExt.tag_classes) {
  renderName(tagClassName);
}

// After (v0.2.0):
for (const tc of moduleExt.tag_classes) {
  renderName(tc.name);
  renderDoc(tc.doc);
  renderAttrs(tc.attrs);
}
```

```go
// Before:
for _, tcName := range ext.TagClasses {
    handle(tcName)
}

// After:
for _, tc := range ext.TagClasses {
    handle(tc.Name) // plus tc.Doc, tc.Attrs as needed
}
```

#### Recipe 2 — `rules[].attrs[].providers` → `provider_groups`

The field is both renamed (`providers` → `provider_groups`) and
reshaped (`string[]` → `string[][]`). The new shape is a
disjunction of conjunctions: outer slice is OR, inner slice is AND.
Picked because Bazel's `attr.label(providers = ...)` accepts both
`[A, B]` (single conjunction) and `[[A], [B, C]]` (disjunction).

```typescript
// Before (v0.1.x):
for (const p of attr.providers ?? []) {
  renderProvider(p);
}

// After — flat-display approach (drop OR/AND distinction):
const flat = (attr.provider_groups ?? []).flatMap((g) => g);
for (const p of flat) {
  renderProvider(p);
}

// After — full-disjunction approach (preserve semantics):
const groups = attr.provider_groups ?? [];
const rendered = groups
  .map((g) => g.join(" AND "))
  .join(" OR ");
renderText(rendered);
```

```go
// Before:
for _, name := range attr.Providers {
    handle(name)
}

// After — flatten:
for _, group := range attr.ProviderGroups {
    for _, name := range group {
        handle(name)
    }
}
```

The CLI renderer (`assay report --format=markdown`) chooses the
full-disjunction form: `A&B | C`.

#### Recipe 3 — `bazel_deps[]` mixes dev + runtime entries

Pre-v0.2 reports flattened dev dependencies into the runtime list.
Post-v0.2 dev-deps surface alongside runtime entries with
`dev_dependency: true` so consumers can split or annotate.

```typescript
// Before:
for (const d of report.bazel_deps) {
  renderRuntimeDep(d);
}

// After:
const runtime = report.bazel_deps.filter((d) => !d.dev_dependency);
const dev = report.bazel_deps.filter((d) => d.dev_dependency);
runtime.forEach(renderRuntimeDep);
dev.forEach(renderDevDep);
```

```go
// Before:
for _, d := range report.BazelDeps {
    renderRuntimeDep(d)
}

// After:
for _, d := range report.BazelDeps {
    if d.DevDependency {
        renderDevDep(d)
    } else {
        renderRuntimeDep(d)
    }
}
```

## [0.1.0] — 2026-05-29

Starting point of versioned releases. The state of the codebase at tag time
covers all functionality through commit
[`7a97ae8`](https://github.com/albertocavalcante/assay/commit/7a97ae8).

See [git log](https://github.com/albertocavalcante/assay/commits/v0.1.0) for
the path from v0 to here. Significant milestones reachable from this tag:

- Stable `ModuleReport` extraction across rules / providers / aspects /
  macros / repository_rules / module_extensions / toolchains / file inventory.
- Three-tier attrs ladder (literal / symbol_fold / load_resolve) plus opt-in
  Tier-3 interpreter fallback via `WithInterpreterFallback`.
- Hermeticity classifier with per-finding `Confidence` (Definitive vs
  Heuristic), `BuildFromSource` self-publish demotion, and cross-file
  dict-subscript integrity-hash resolution.
- Macro detection with body-inspection + path filter + fixpoint composition
  (Phase A and B from `docs/macro-detection-plan.md`).
- Determinism contract enforced by `TestAnalyze_IsDeterministic_*` —
  byte-identical input → byte-identical output.
- Performance baseline captured in `docs/bench-baseline.txt` for benchstat
  regression checks.
- Public packages now consumed by canopy via a relative `replace` directive.

This tag exists as canopy's pin point during the breaking changes in Round D
(per `docs/registry-surface-plan.md` §11). Downstream consumers wanting to
defer the v0.2.0 migration can hold at v0.1.0 indefinitely.
