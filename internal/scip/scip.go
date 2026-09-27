// Package scip wraps scip-bazel for bzlhub's ingest pipeline: take a
// materialized module source tree + (module, version) coordinate, run
// scip-bazel.Index against it with the right package identity, and return
// the resulting binary protobuf bytes ready for storage.
//
// The wrapper is intentionally minimal — scip-bazel itself handles the
// Bazel-flavored annotation; scip-starlark underneath handles language
// indexing. This package's only job is to:
//
//   - Pin the package identity so bzlhub's per-(module, version) indexes
//     don't collide. The coordinate lands in the symbol's manager, name
//     and version fields:
//     "starlark bzlmod rules_python 0.40.0 defs.bzl/py_binary#".
//   - Write the *scip.Index to protobuf bytes for SQLite storage.
//
// Callers should treat a Generate error as non-fatal — bzlhub ingest
// can still proceed; the SCIP index is supplementary navigation data,
// not part of the canonical ModuleReport.
package scip

import (
	"bytes"
	"fmt"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
	scipbazel "github.com/albertocavalcante/scip-bazel/pkg/index"
	"github.com/albertocavalcante/scip-kit/scipio"
	"github.com/albertocavalcante/scip-kit/symbol"
)

// Generate indexes the materialized module source rooted at sourceDir
// and returns the resulting SCIP index as binary protobuf bytes.
//
// The Package is pinned to the module's registry coordinate so every symbol
// in the resulting index carries it. The closure resolves load() statements
// that reach other modules. The
// supplied ModuleReport's BazelDeps populate a closure map handed to
// scip-bazel's BzlmodResolver, which turns load() statements like
// `load("@platforms//:cpu.bzl", "get_default_cpu")` into fully-qualified
// SCIP symbols like `starlark bzlmod platforms 0.0.10 cpu.bzl/get_default_cpu#`
// -- the wire shape that makes cross-module navigation work via exact-string
// symbol match against other bzlhub-served indexes.
//
// What can still be unresolved, and why: a dependency that has not been
// ingested is not in the store, so references into it keep scip-starlark's
// placeholder -- `starlark unresolved <raw-label> . <symbol>#`, which parses
// but carries no coordinate. That makes ingest ORDER matter: a module indexed
// before its dependencies resolves fewer symbols until it is re-indexed.
// Closing that needs a reindex pass, not a change here.
//
// The closure is a parameter, not derived from a ModuleReport, so the caller
// picks how far it reaches: DirectClosure for the module's direct deps, or
// TransitiveClosure for the transitive walk. Making that visible at the call site is
// the point -- it used to be a hidden choice inside this function, and it was
// silently the narrow one.
//
// Generate itself stays free of I/O and policy. It runs per module during
// ingest; whatever resolves the closure is the caller's business, and the
// caller is already operating under the egress policy.
func Generate(sourceDir, moduleName, version string, closure bzlmod.Closure) ([]byte, error) {
	if sourceDir == "" || moduleName == "" || version == "" {
		return nil, fmt.Errorf("scip.Generate: sourceDir, module, and version are all required (got %q, %q, %q)", sourceDir, moduleName, version)
	}
	idx, err := scipbazel.Index(sourceDir, scipbazel.Options{
		Package: symbol.Package{
			Manager: scipbazel.BzlmodManager,
			Name:    moduleName,
			Version: version,
		},
		CrossModuleResolver: bzlmod.NewBzlmodResolver(closure),
	})
	if err != nil {
		return nil, fmt.Errorf("scip-bazel index %s@%s: %w", moduleName, version, err)
	}
	var buf bytes.Buffer
	_, err = scipio.WriteIndex(&buf, idx)
	if err != nil {
		return nil, fmt.Errorf("write scip.Index for %s@%s: %w", moduleName, version, err)
	}
	return buf.Bytes(), nil
}

// DirectClosure projects a ModuleReport's own bazel_deps into the repo-keyed
// closure scip-bazel's BzlmodResolver expects.
//
// This is the narrow closure: direct dependencies only, no transitive walk. It
// is what the ingest paths fall back to when the transitive walk cannot complete,
// because resolving a module's direct deps beats resolving nothing.
// Tolerant of a nil report (returns an empty map; the resolver
// gracefully degrades to "" returns, which scip-starlark renders as
// unresolved placeholders).
func DirectClosure(r *report.ModuleReport) bzlmod.Closure {
	if r == nil {
		return nil
	}
	out := make(bzlmod.Closure, len(r.BazelDeps))
	for _, dep := range r.BazelDeps {
		if dep.Name == "" || dep.Version == "" {
			// Neither half can be guessed, and a partial coordinate cannot
			// produce a conformant symbol. Leaving it out sends the reference
			// to the unresolved placeholder, which is the honest outcome.
			continue
		}
		out[repoKeyOf(dep)] = bzlmod.Coordinate{Module: dep.Name, Version: dep.Version}
	}
	return out
}

// repoKeyOf returns the key a load() statement will use for this dependency.
//
// `load("@foo//...")` names the REPO, and
// bazel_dep(name = "rules_foo", repo_name = "foo") makes "foo" an alias for
// module "rules_foo". RepoName is empty for the majority that set no alias,
// where the repo and module names coincide.
//
// Shared by the direct closure here and the transitive walk: two
// copies of this rule is how one of them ends up keyed by module name again.
func repoKeyOf(dep report.ModuleKey) string {
	if dep.RepoName != "" {
		return dep.RepoName
	}
	return dep.Name
}
