// Package moduledeps reads the bazel_dep list out of a MODULE.bazel.
//
// It exists to be the ONE definition of "what a bazel_dep is" in bzlhub. Two
// subsystems need that list and want different subsets of it: the recursive
// ingest walk takes non-dev dependencies (it is deciding what to fetch next),
// while the SCIP closure takes the root module's dev dependencies but not a
// dependency's, and needs repo_name because a load() statement names the repo
// rather than the module. A parser that pre-filtered could serve only one of
// them, which is exactly how a codebase ends up with two MODULE.bazel parsers
// that disagree.
//
// So this package filters nothing and drops nothing. Callers filter.
package moduledeps

import (
	"fmt"

	"github.com/albertocavalcante/assay/report"
	gobzlmod "github.com/albertocavalcante/go-bzlmod"
)

// FromModuleBazel parses MODULE.bazel content and returns every bazel_dep in
// source order, preserving repo_name and dev_dependency.
//
// report.ModuleKey is the return type on purpose: it is what
// ModuleReport.BazelDeps already holds, so a dependency list read from a
// MODULE.bazel and one read from a stored report are the same type and can feed
// the same code. That is what keeps the closure walk to a single path.
//
// A parse failure is an error rather than an empty list, because an empty list
// reads as "this module has no dependencies" and is indistinguishable from
// success at every call site.
func FromModuleBazel(content []byte) ([]report.ModuleKey, error) {
	info, err := gobzlmod.ParseModuleContent(string(content))
	if err != nil {
		return nil, fmt.Errorf("moduledeps: parse MODULE.bazel: %w", err)
	}
	if len(info.Dependencies) == 0 {
		return nil, nil
	}
	out := make([]report.ModuleKey, 0, len(info.Dependencies))
	for _, d := range info.Dependencies {
		out = append(out, report.ModuleKey{
			Name:          d.Name,
			Version:       d.Version,
			RepoName:      d.RepoName,
			DevDependency: d.DevDependency,
		})
	}
	return out, nil
}
