# Changelog

All notable changes to go-bzlmod-ast are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

Initial release:

- `parser.go` — `ParseFile(path)` and `ParseContent(filename, data)`
  returning a `*ParseResult` with the typed `*ModuleFile`, plus
  parse errors and warnings.
- `types.go` — `Statement` interface and concrete types for every
  recognized MODULE.bazel directive (`ModuleDecl`, `BazelDep`,
  `UseExtension`, `UseRepo`, the five `*Override` variants,
  `RegisterToolchains`, `RegisterExecutionPlatforms`, `Include`,
  `ExtensionTagCall`, `UseRepoRule`, `InjectRepo`, `OverrideRepo`,
  `FlagAlias`, `UnknownStatement`).
- `handler.go` — `Handler` interface, `Walk(file, handler)`,
  `BaseHandler` no-op embed, and two ready-made collectors
  (`DependencyCollector`, `OverrideCollector`).
- `label/` — typed `Module`, `Version`, `ApparentRepo`,
  `ApparentLabel`, `StarlarkIdentifier`.
- `buildutil/` — generic attribute-extraction helpers over the
  buildtools AST (`String`, `Bool`, `Int`, `StringList`,
  `ExtractValue`).
- `third_party/buildtools/` — vendored `bazelbuild/buildtools`
  parser (Apache-2.0).
