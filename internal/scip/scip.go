// Package scip wraps scip-bazel for canopy's ingest pipeline: take a
// materialized module source tree + (module, version) coordinate, run
// scip-bazel.Index against it with the right SymbolPrefix, and return
// the resulting binary protobuf bytes ready for storage.
//
// The wrapper is intentionally minimal — scip-bazel itself handles the
// Bazel-flavored annotation; scip-starlark underneath handles language
// indexing. This package's only job is to:
//
//   - Pin the symbol scheme so canopy's per-(module, version) indexes
//     don't collide ("bzlmod rules_python@0.40.0 ..." vs the same
//     module at a different version).
//   - Marshal the *scip.Index to protobuf bytes for SQLite storage.
//
// Callers should treat a Generate error as non-fatal — canopy ingest
// can still proceed; the SCIP index is supplementary navigation data,
// not part of the canonical ModuleReport.
package scip

import (
	"fmt"

	"github.com/albertocavalcante/assay/report"
	bzlmodres "github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
	scipbazel "github.com/albertocavalcante/scip-bazel/pkg/index"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// Generate indexes the materialized module source rooted at sourceDir
// and returns the resulting SCIP index as binary protobuf bytes.
//
// The SymbolPrefix is pinned to "bzlmod <module>@<version>" so every
// symbol in the resulting index carries its registry coordinate. The
// supplied ModuleReport's BazelDeps populate a closure map handed to
// scip-bazel's BzlmodResolver, which turns load() statements like
// `load("@platforms//:cpu.bzl", "get_default_cpu")` into fully-qualified
// SCIP symbols like `bzlmod platforms@0.0.10 cpu.bzl#get_default_cpu` —
// the wire shape that makes cross-module navigation work via exact-
// string symbol match against other canopy-served indexes.
//
// Phase 1 limitation: the closure carries the module's DIRECT
// bazel_deps only. Transitive load() targets (`@some-trans-dep//...`)
// fall back to scip-starlark's `unresolved-load` placeholder. A future
// pass will plug in canopy's MVS-resolved closure (already available
// from closurediff.walkClosure) for full transitive coverage.
func Generate(sourceDir, moduleName, version string, r *report.ModuleReport) ([]byte, error) {
	if sourceDir == "" || moduleName == "" || version == "" {
		return nil, fmt.Errorf("scip.Generate: sourceDir, module, and version are all required (got %q, %q, %q)", sourceDir, moduleName, version)
	}
	closure := closureFromReport(r)
	idx, err := scipbazel.Index(sourceDir, scipbazel.Options{
		SymbolPrefix:        fmt.Sprintf("bzlmod %s@%s", moduleName, version),
		CrossModuleResolver: bzlmodres.NewBzlmodResolver(closure),
	})
	if err != nil {
		return nil, fmt.Errorf("scip-bazel index %s@%s: %w", moduleName, version, err)
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		return nil, fmt.Errorf("marshal scip.Index for %s@%s: %w", moduleName, version, err)
	}
	return b, nil
}

// closureFromReport projects a ModuleReport's bazel_deps into the
// {name → version} map shape scip-bazel's BzlmodResolver expects.
// Tolerant of a nil report (returns an empty map; the resolver
// gracefully degrades to "" returns, which scip-starlark renders as
// `unresolved-load` placeholders).
func closureFromReport(r *report.ModuleReport) map[string]string {
	if r == nil {
		return nil
	}
	out := make(map[string]string, len(r.BazelDeps))
	for _, dep := range r.BazelDeps {
		if dep.Name == "" || dep.Version == "" {
			continue
		}
		out[dep.Name] = dep.Version
	}
	return out
}

// Parse is the inverse of Generate: hand it the bytes back, get a
// *scip.Index. Useful for canopy's REST endpoint when it wants to do
// any server-side filtering before serving (e.g. strip diagnostics,
// project a subset of documents). Today canopy serves the bytes
// verbatim, but the helper is here for the next iteration.
func Parse(b []byte) (*scip.Index, error) {
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("unmarshal scip.Index: %w", err)
	}
	return &idx, nil
}
