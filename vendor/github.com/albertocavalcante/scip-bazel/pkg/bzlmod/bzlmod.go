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
// into the canonical SCIP symbol string used elsewhere in the canopy
// stack:
//
//	bzlmod rules_python@0.40.0 python/defs.bzl#py_library
//
// scip-bazel deliberately performs no MVS or registry I/O. The
// consumer (canopy) is responsible for handing us a fully-resolved
// {name → version} map; this package only does the (cheap) string
// rewrite.
package bzlmod

import (
	"strings"

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
func NewBzlmodResolver(closure map[string]string) func(scipstarlark.LoadTarget) string {
	return func(target scipstarlark.LoadTarget) string {
		module, relPath, ok := parseBzlmodTarget(target.Raw)
		if !ok {
			return ""
		}
		if target.Symbol == "" {
			return ""
		}
		version, ok := closure[module]
		if !ok {
			return ""
		}
		// canopy's canonical scheme:
		//   bzlmod <module>@<version> <relpath>#<symbol>
		return "bzlmod " + module + "@" + version + " " + relPath + "#" + target.Symbol
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
