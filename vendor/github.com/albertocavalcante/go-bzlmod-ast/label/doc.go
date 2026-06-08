// Package label provides strongly-typed, validated label components
// for Bazel modules. All types in the package are immutable and
// validate their values at construction time; use the constructor
// functions (NewModule, NewVersion, etc.) rather than the zero value.
//
// Types:
//   - [Module]: a validated module name (e.g. "rules_go").
//   - [Version]: a semantic version with Bazel extensions
//     (e.g. "0.50.1", "1.0.0-rc1").
//   - [ApparentRepo]: a repository name as it appears in labels.
//   - [CanonicalRepo]: a fully-qualified repo name (module+version).
//   - [ApparentLabel]: a Bazel label (e.g. "@rules_go//go:def.bzl").
//   - [StarlarkIdentifier]: a valid Starlark identifier.
//
// Validation patterns:
//
//	Module names         [a-z]([a-z0-9._-]*[a-z0-9])?
//	Repository names     [a-zA-Z][a-zA-Z0-9._-]*
//	Starlark identifiers [a-zA-Z_][a-zA-Z0-9_]*
//
// # Version format
//
// Versions look like SemVer with Bazel extensions:
// RELEASE[-PRERELEASE][+BUILD]. RELEASE is a dot-separated identifier
// sequence (for example "1.2.3" or "1.2.3.bcr.1"), PRERELEASE is an
// optional hyphen-prefixed identifier sequence, BUILD is optional
// metadata ignored for comparison.
//
// # Comparison
//
// Comparison follows Bazel's reference implementation:
//
//  1. Empty versions compare as HIGHEST (used for non-registry overrides).
//  2. Release segments are compared lexicographically.
//  3. Prerelease versions are lower than release versions with the
//     same release prefix.
//  4. Prerelease segments are compared lexicographically.
//
// Within a segment, numeric identifiers sort before non-numeric ones;
// numeric identifiers compare as unsigned integers; non-numeric
// identifiers compare as strings.
package label
