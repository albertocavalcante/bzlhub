package version

import (
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// BazelFeaturesValue builds the synthetic `bazel_features` struct
// the upstream bazel-features-bzl module exposes from its
// `//:features.bzl`. Returns a nested struct whose fields match the
// dotted Feature constants (e.g., `external_deps.module_extension_has_os_arch_dependent`).
//
// Use with stub.LoaderFor (or any custom thread.Load) to satisfy
// `load("@bazel_features//:features.bzl", "bazel_features")` in a
// .bzl file under analysis: register the synthetic at the load
// target, then access values via bazel_features.<subgroup>.<feature>.
//
// Drift caveat: upstream bazel-features-bzl may add new feature
// flags between releases. Our table reflects the curation date in
// docs/plans/01-bazel-builtins-emulation/03-builtins-surface.md;
// for newer additions, supply via bzl.Options.PredeclaredBzl or
// extend AllFeatures().
func BazelFeaturesValue(v Version) starlark.Value {
	v = v.Resolved()

	// Group features by their first dotted segment ("external_deps",
	// "rules", "native", etc.) so the resulting struct mirrors the
	// upstream's hierarchical shape.
	groups := map[string]starlark.StringDict{}
	for _, f := range AllFeatures() {
		group, leaf, ok := splitFeaturePath(string(f))
		if !ok {
			continue
		}
		if groups[group] == nil {
			groups[group] = starlark.StringDict{}
		}
		groups[group][leaf] = starlark.Bool(v.HasFeature(f))
	}

	top := starlark.StringDict{}
	for group, leaves := range groups {
		top[group] = starlarkstruct.FromStringDict(starlarkstruct.Default, leaves)
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, top)
}

func splitFeaturePath(p string) (group, leaf string, ok bool) {
	idx := strings.Index(p, ".")
	if idx < 0 {
		return "", "", false
	}
	return p[:idx], p[idx+1:], true
}

// BazelFeaturesModule is the canonical load target for the synthetic
// bazel_features module. Use this as the key in stub.LoaderFor's
// tryReal map or as the comparison in a custom Load handler.
const BazelFeaturesModule = "@bazel_features//:features.bzl"

// BazelFeaturesLoader returns a *starlark.Thread.Load function that
// serves the synthetic bazel_features module at v's surface and
// delegates anything else to next. Pass nil for next to error on
// other load targets.
func BazelFeaturesLoader(v Version, next func(*starlark.Thread, string) (starlark.StringDict, error)) func(*starlark.Thread, string) (starlark.StringDict, error) {
	val := BazelFeaturesValue(v)
	return func(t *starlark.Thread, module string) (starlark.StringDict, error) {
		if module == BazelFeaturesModule {
			return starlark.StringDict{"bazel_features": val}, nil
		}
		if next != nil {
			return next(t, module)
		}
		return nil, nil
	}
}
