// Package bzlmod provides helpers that bridge a consumer's Bzlmod
// dependency graph to scip-starlark's CrossModuleResolver contract.
//
// The flagship helper, NewBzlmodResolver, accepts a closure of
// resolved bazel_dep coordinates (name → version) and returns a
// function suitable for plugging into scip-bazel's
// Options.CrossModuleResolver. The returned resolver translates
// load() targets such as
//
//	@rules_python//python:defs.bzl     (with symbol py_library)
//
// into the conformant SCIP symbol string:
//
//	starlark bzlmod rules_python 0.40.0 python/defs.bzl/py_library#
//
// Note bzlmod is the package MANAGER, not the scheme. The previous form
// ("bzlmod rules_python@0.40.0 python/defs.bzl#py_library") was rejected by
// scip.ParseSymbol: it packed the module and version into one field and left
// the grammar a field short.
//
// scip-bazel deliberately performs no MVS or registry I/O. The
// consumer (canopy) is responsible for handing us a fully-resolved
// {name → version} map; this package only does the (cheap) string
// rewrite.
package bzlmod

import (
	"strings"

	scipbazel "github.com/albertocavalcante/scip-bazel/pkg/index"
	"github.com/albertocavalcante/scip-kit/symbol"
	scipstarlark "github.com/albertocavalcante/scip-starlark/pkg/index"
)

// NewBzlmodResolver returns a CrossModuleResolver function that maps
// Bzlmod-style load() targets to canonical SCIP symbol strings using
// the supplied closure of bazel_dep name → version.
//
// The resolver returns "" — which scip-starlark renders as its
// "unresolved-load" placeholder — when:
//
//   - the load target's Raw doesn't start with "@" (relative load, e.g.
//     "//tools:helpers.bzl"; handled by scip-starlark's same-module
//     scope, not us);
//   - the leading "@<repo>" isn't present in the closure;
//   - the load target is the bare "@//..." main-repo pseudonym (out of
//     scope for Phase 1);
//   - the target string is malformed (empty, missing "//", missing the
//     final ":", or has an empty repo / file segment).
//
// The function NEVER panics, including on a nil closure.
func NewBzlmodResolver(closure Closure) func(scipstarlark.LoadTarget) string {
	return func(target scipstarlark.LoadTarget) string {
		repo, relPath, ok := parseBzlmodTarget(target.Raw)
		if !ok {
			return ""
		}
		if target.Symbol == "" {
			return ""
		}
		// Keyed by REPO, which is what a load() names. The MODULE name comes
		// out of the coordinate, because those differ whenever a bazel_dep sets
		// repo_name -- and the symbol has to carry the module name to match the
		// definition in that module's own index.
		coord, ok := closure[repo]
		if !ok {
			return ""
		}
		// An incomplete coordinate is a caller bug. Declining sends the caller
		// to its unresolved placeholder, which is honest; guessing the missing
		// half is what produced a wrong-but-plausible symbol before.
		if coord.Module == "" || coord.Version == "" {
			return ""
		}
		// Must match, byte for byte, what scip-starlark emits for the
		// DEFINITION in that module -- that identity is what makes
		// cross-module navigation resolve at all. Both sides go through
		// symbol.Global for exactly this reason.
		//
		// On an unencodable module or path, return "" so the caller falls back
		// to its unresolved placeholder rather than emitting a symbol the
		// reference parser rejects.
		sym, err := symbol.Global(scipstarlark.SymbolScheme,
			symbol.Package{Manager: scipbazel.BzlmodManager, Name: coord.Module, Version: coord.Version},
			relPath, target.Symbol)
		if err != nil {
			return ""
		}
		return sym
	}
}

// parseBzlmodTarget splits a load() target of the form
// "@<repo>//<package>:<file.bzl>" into the canopy-canonical
// (repo, relPath) pair, where relPath is "<package>/<file.bzl>" with
// the colon separator collapsed to a slash. The root-package form
// "@<repo>//:<file.bzl>" maps to relPath == "<file.bzl>".
//
// Returns ok=false for any input we don't claim to resolve:
//   - empty
//   - not prefixed with "@"
//   - the "@//..." main-repo pseudonym (empty repo segment)
//   - missing "//", missing ":" or with an empty file segment
func parseBzlmodTarget(raw string) (module, relPath string, ok bool) {
	if raw == "" || raw[0] != '@' {
		return "", "", false
	}
	// Strip the leading '@'. The body has shape "<repo>//<pkg>:<file>".
	body := raw[1:]
	slashIdx := strings.Index(body, "//")
	if slashIdx < 0 {
		return "", "", false
	}
	module = body[:slashIdx]
	if module == "" {
		// "@//..." is the main-repo pseudonym; not our concern.
		return "", "", false
	}
	rest := body[slashIdx+2:] // after the "//"
	colonIdx := strings.LastIndex(rest, ":")
	if colonIdx < 0 {
		return "", "", false
	}
	pkg := rest[:colonIdx]
	file := rest[colonIdx+1:]
	if file == "" {
		return "", "", false
	}
	if pkg == "" {
		// Root package: "@platforms//:cpu.bzl" → relPath "cpu.bzl".
		return module, file, true
	}
	return module, pkg + "/" + file, true
}

// Coordinate is the registry identity of one resolved dependency.
//
// Module and Version are separate fields, and both are required, because they
// land in separate fields of the SCIP symbol grammar --
// `starlark bzlmod <module> <version> ...`. There is no meaningful default for
// either: a coordinate missing one is a caller bug, and the resolver declines
// rather than inventing the other half.
type Coordinate struct {
	// Module is the registry module name, e.g. "rules_foo". This is what
	// appears in the symbol, NOT the repo name.
	Module string

	// Version is the resolved version, e.g. "1.2.3".
	Version string
}

// Closure maps a REPO NAME to the coordinate it resolves to.
//
// Keyed by repo name because that is what a load() names: `@foo//:defs.bzl`
// says "foo", and `bazel_dep(name = "rules_foo", repo_name = "foo")` makes
// "foo" an alias for module "rules_foo". Keying by module name instead means
// every repo-renamed dependency misses.
//
// For the common case, where no repo_name is set, the key and
// Coordinate.Module are the same string.
type Closure map[string]Coordinate
