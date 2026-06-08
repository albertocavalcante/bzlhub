# Changelog

All notable changes to go-starlark-syntaxutil are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

Initial release:

- `kwarg.go` — `KeywordArg`, `StringKeywordArg`, `BoolKeywordArg`,
  `IntKeywordArg`, `StringListKeywordArg`, `IdentListKeywordArg`,
  `PositionalStrings`
- `ident.go` — `IdentName`
- `loads.go` — `CollectLoads`, `ImportedSymbol`
- `loadpath.go` — `ResolveLoadedFile`
- `topdict.go` — `CollectTopLevelDictBindings`
- `paths.go` — `IsTestOrExamplePath`, `TestOrExamplePathSegments`
- `provenance.go` — `ProvenanceFrom` (returns named ints; no struct)
