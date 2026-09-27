package scip

import (
	"context"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
)

// TransitiveClosure builds the closure for root by walking its dependency graph
// through deps.
//
// The walk is ordinary BFS; what matters is where deps gets its answers.
// Resolving at index time is correct and standard -- scip-go reads versions from
// go.mod, scip-java from the Maven graph, because the build system has already
// pinned them, and Bazel pins them too via MODULE.bazel plus MVS. The earlier
// version of this function asked the STORE, whose contents are a function of
// ingest HISTORY, and that is the only reason ingest order ever mattered.
// Chaining an authoritative source behind the store (see FirstOf) makes the
// closure a function of the module's own declaration instead, which is what it
// should always have been.
//
// Dev dependencies follow Bazel: the root module's are used, everyone else's are
// ignored. Keeping a dependency's dev deps would resolve symbols Bazel itself
// cannot see, and confidently-wrong navigation is harder to notice than none.
//
// A dependency no source can answer for stops the descent rather than failing
// the walk: its coordinate is already known from the parent's declaration, so
// references to it still resolve; only its subtree is invisible.
func TransitiveClosure(ctx context.Context, deps DepsFunc, root *report.ModuleReport) (bzlmod.Closure, error) {
	if root == nil {
		return bzlmod.Closure{}, nil
	}

	// resolved is the answer being built: repo key -> coordinate.
	resolved := make(bzlmod.Closure)
	// bestVersion tracks the winning version per MODULE name, which is the unit
	// MVS resolves on. Keyed separately from resolved because that is keyed by
	// repo alias, and two repos could in principle alias one module.
	bestVersion := make(map[string]string)

	// queue holds modules whose OWN dependencies still need expanding.
	var queue []report.ModuleKey

	// The root's dev deps count; the loop below drops everyone else's.
	for _, d := range root.BazelDeps {
		if promoted := record(resolved, bestVersion, d); promoted {
			queue = append(queue, d)
		}
	}

	// Breadth-first. A module is expanded when it first appears and again if a
	// higher version later wins, because MVS must walk the winner's
	// dependencies, not whichever version happened to be seen first.
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]

		found, err := deps(ctx, current.Name, current.Version)
		if err != nil {
			// No source could answer. The coordinate itself is already recorded
			// from the parent's declaration, so references to this module still
			// resolve; only its subtree is invisible. Incomplete knowledge is a
			// normal state, not a failure.
			continue
		}
		for _, d := range found {
			if d.DevDependency {
				continue
			}
			if promoted := record(resolved, bestVersion, d); promoted {
				queue = append(queue, d)
			}
		}
	}
	return resolved, nil
}

// record adds or upgrades one dependency and reports whether the caller should
// expand it.
//
// It returns true only when this coordinate is new or strictly newer than what
// was already known. That is what terminates the walk: a cycle revisits a
// module at the same version, wins nothing, and stops.
func record(resolved bzlmod.Closure, bestVersion map[string]string, d report.ModuleKey) bool {
	if d.Name == "" || d.Version == "" {
		// Neither half can be guessed and a partial coordinate cannot produce a
		// conformant symbol, so leaving it out is the honest outcome.
		return false
	}
	if known, ok := bestVersion[d.Name]; ok && !versionIsNewer(d.Version, known) {
		return false
	}
	bestVersion[d.Name] = d.Version
	resolved[repoKeyOf(d)] = bzlmod.Coordinate{Module: d.Name, Version: d.Version}
	return true
}

// versionIsNewer compares two already-resolved version strings.
//
// A lexical compare, matching what internal/closurediff does for the same job.
// Real MVS ordering lives in gobzlmod and needs registry metadata; here both
// sides come from a MODULE.bazel that Bazel itself accepted, and the comparison
// only has to break ties between two versions of one module. Where lexical and
// semver disagree (1.10 vs 1.9) the walk can pick the lower, which costs
// coverage for that module and never correctness — a wrong pick yields
// unresolved references, not references to the wrong place.
func versionIsNewer(candidate, known string) bool {
	return candidate > known
}
