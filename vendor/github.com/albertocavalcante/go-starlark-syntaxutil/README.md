# go-starlark-syntaxutil

Generic helpers for working with `go.starlark.net/syntax` AST nodes.
Pure Starlark — no knowledge of any dialect (Bazel, Buck2, Copybara,
Tilt) is baked in.

## Install

```bash
go get github.com/albertocavalcante/go-starlark-syntaxutil
```

## Usage

```go
import syntaxutil "github.com/albertocavalcante/go-starlark-syntaxutil"

doc := syntaxutil.StringKeywordArg(call, "doc")
providers := syntaxutil.IdentListKeywordArg(call, "providers")
loads := syntaxutil.CollectLoads(file)
```

## What's here

- `kwarg.go` — `KeywordArg` and its typed variants
  (`StringKeywordArg`, `BoolKeywordArg`, `IntKeywordArg`,
  `StringListKeywordArg`, `IdentListKeywordArg`); `PositionalStrings`.
- `ident.go` — `IdentName` returns the trailing identifier of common
  `syntax.Node` shapes.
- `loads.go` — `CollectLoads` builds a `local-name → (path, original)`
  map from a file's top-level `load()` statements.
- `loadpath.go` — `ResolveLoadedFile` normalizes a label-style load
  path (`:foo.bzl`, `//pkg:bar.bzl`) against a caller's relative path.
- `topdict.go` — `CollectTopLevelDictBindings` indexes a file's
  `IDENT = {...}` top-level bindings.
- `paths.go` — `IsTestOrExamplePath` is a heuristic classifier for
  paths that traverse `test/`, `examples/`, `vendor/`, etc.
- `provenance.go` — `ProvenanceFrom` returns four named ints
  (`startCol`, `startRow`, `endCol`, `endRow`); callers wrap them in
  their own struct.

## Design

- **No struct in the public API for source positions.** Each consumer
  has its own serialization shape; the library returns coordinates as
  named ints and stays out of the way.
- **No dialect-specific predicates.** Anything that requires knowing a
  symbol is a rule, a `bazel_dep`, a `buck` macro, etc. belongs in the
  consumer.

## Status

Pre-1.0. The API may change.

## License

MIT.
