# Tier-3 attrs extractor — known limitations

The interpreter-backed extractor (`interp.Hydrate`) resolves rule
attribute schemas for `.bzl` files that the AST-only walker can't
handle. It works for the long tail of real-world Bazel rules, but
some shapes still fall through to the "dynamic schema" UI panel.
Each case below is **deliberate** — listed here so callers (and the
test corpus) stay honest about coverage. When upstream
`starlark-go-bazel` evolves, update this file and any pinning test.

## Hard ceiling: rules whose source can't be evaluated

These never reach the interpreter, so there's nothing Tier 3 can do
about them.

### Parse errors

A `.bzl` file with a syntax error (unterminated string, illegal
identifier, etc.) fails at `syntax.Parse` time. Hydrate silently
skips the file. `bzlwalk` separately reports the file as a per-file
parse error via its `onFileError` handler — same recovery path as
Tier 0.

### File missing on disk

A rule's `Provenance.File` points at a path that isn't in
`workspaceRoot`. Causes: the upstream tarball was repacked, the
ingest tree was cleaned mid-run, or the report came from a different
checkout. Hydrate logs at Debug and skips the rule.

## Soft ceiling: rules the interpreter starts but can't finish

These would resolve if `starlark-go-bazel` matured further.
Each is pinned by a test or a doc comment so a future audit catches
when the upstream changes flip the answer.

### `repository_rule()` and `module_extension()` — **NOW SUPPORTED**

Closed by `starlark-go-bazel` milestone M2 of the
bazel-builtins-emulation plan (see that repo's
`docs/plans/01-bazel-builtins-emulation/`). Both builtins return
typed values (`*types.RepositoryRuleClass` /
`*types.ModuleExtensionClass`); Hydrate consults the new types and
extracts attr names alongside the existing rule() path.

- Pinned by: `TestHydrate_RepositoryRule_HydratesAttrs` (positive
  assertion).
- Attr Type/Default/Doc inheriting from `attr.*()` calls — same
  open limitation as `rule()`, tracked under the next section.

### Per-attr `Type` / `Default` / `Doc` / `Mandatory`

`starlark-go-bazel`'s `types.RuleClass` conversion stubs the
attribute descriptor (`types/rule_class.go:712` — _"we create a
basic descriptor"_). The interpreter sees `attrs = {"x":
attr.string(default = "hi", doc = "...")}` but stores only the
NAME `"x"` in the `RuleClass.attrs` map; everything else is dropped.

So interpreted attrs carry attribute NAMES only — Type/Default/Doc/
Mandatory are empty even when the source had them. Tier 0/1 (AST-
based) extracts those fields fully; interpreted entries don't yet.

- Pinned by:
  `TestHydrate_ResolvesHelperFunctionAttrs` has a
  HEADS-UP `t.Logf` that fires if upstream starts emitting real
  defaults — that's the signal to tighten the assertion.

### Stubbed external load symbols used as values at module load

The `stubExternalLoads` rewrite replaces `load("@external//...",
"X")` with `X = None`. If module-level code then tries to USE `X`
as a non-None value, evaluation will fail. Examples that break:

```python
load("@x//:defs.bzl", "BASE")
ATTRS = BASE | {"extra": attr.string()}  # BASE is None, error
```

vs the case Tier 3 DOES handle, where the external symbol is only
referenced inside function bodies that aren't called at module load:

```python
load("@x//:defs.bzl", "helper")
def _impl(ctx):
    helper(...)  # only called at analysis time, OK
```

This is rare in practice because `BASE` is usually defined in the
same module (Tier 1 territory) — external base-attr dicts are
unusual.

### `native.*` called at module load time

`native` is predeclared as a permissive stub (`eval/evaluator.go`
`newNativeStub`) so identifier resolution succeeds. But the stub
returns `None` from every `native.foo(...)` call. If module-level
code uses that return value (e.g. `PKG = native.package_name()`
then `attrs = {PKG: attr.string()}`), the comprehension gets `None`
as a key and may fail.

This is also rare — `native.*` is almost always called from inside
helper/macro bodies that run during analysis, not at module load.

### Files whose transitive load chain depends on workspace state

`bazel_dep`-style cross-module loads (e.g.
`load("@aspect_rules_lint//format:defs.bzl", ...)`) resolve at
ingest time only if the referenced module is also in the local
mirror. We stub them as missing externals (lenient), but the
importing file can't then USE any symbol from those loads at module
level. Same workaround as the external-load case above.

## Adding a new limitation

Anytime a new case is discovered that the extractor can't handle:

1. **If it's deliberate** (e.g. we choose not to interpret some
   construct for safety): add a pinning test in `interp_test.go`
   that documents the gap, then append a section here pointing at
   that test.
2. **If it's a starlark-go-bazel limitation**: open a sibling note
   in `starlark-go-bazel`'s repo (its own LIMITATIONS / TODO if
   started) and link from here.
3. **If it's an extractor bug**: write a failing test first, fix
   it, then this file shouldn't need to mention it.

## Stardoc compatibility surface

This file is about the EXTRACTOR. There's a separate question about
how docs are RENDERED that lives upstream in canopy's UI, but it
matters here because the test corpus often comes from Stardoc-shaped
sources.

**Bazel's docs convention is Stardoc**, which is a Bazel build tool
that takes `.bzl` files and emits per-module Markdown with these
properties:

- One page per `.bzl` module (or one per stardoc target)
- Bare-name anchors: `<h2 id="format_multirun">format_multirun</h2>`
- Cross-symbol references via plain Markdown: `[name](#name)`
- "Args" / "Returns" sections in macro/function docs (Google-Python-
  style, NOT CommonMark)
- Attribute tables with type + default + doc columns

What this means for `assay` consumers:

1. **Cross-refs**: doc strings routinely contain `[symbol](#symbol)`
   links that EXPECT the receiver page to have `id="symbol"` for
   every symbol the module defines. Canopy renders bare-name anchors
   on every symbol kind (rules, providers, macros, repository_rules,
   module extensions, toolchains) so these links work in-place.
   Authors who write `[fn](#fn)` get correct behavior without
   modification.

2. **Args sections**: Stardoc parses a Python-style `Args:` block in
   docstrings and renders it as a parameter table. canopy does NOT
   do this today — macro docstrings render as raw Markdown
   (paragraph + list, etc.) without auto-table extraction. The
   `interp` package returns the Macro's parameter NAMES via
   `MacroSpec.Params`; pairing them with per-arg docstrings from an
   Args block would require a separate parser pass.

3. **Should there be a stardoc-go library?** Open question. The
   tactical case is small — canopy's job is to BROWSE a registry,
   not regenerate Stardoc output. The strategic case would be an
   "export to docs" workflow that takes a ModuleReport and emits
   the same Markdown Stardoc would have. Deferred until someone has
   a concrete need; for now, see assay/interp as the structural
   source and canopy's UI as the renderer, and treat Stardoc as a
   sibling tool we're compatible WITH rather than a superset we
   should match.

## What this file is NOT

Not a backlog. Each entry is a current, intentional gap with a
known reason. If you find yourself adding "we should fix this
later" entries, those go in the codebase as `TODO:` comments next
to the relevant code instead.
