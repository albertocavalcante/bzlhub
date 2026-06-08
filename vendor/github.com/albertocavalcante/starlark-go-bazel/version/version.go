// Package version pins which Bazel LTS major a starlark-go-bazel
// evaluation targets. The runtime Version enum is orthogonal to the
// per-feature experimental/incompatible flag axis on
// bzl.Options.FeatureFlags.
//
// Per-version feature tables (HasFeature, Deltas) are populated in
// M6 of the upstream plan; the M1 surface establishes only the enum
// and the Latest() helper so bzl.Options can take a Version field
// without depending on M6 work.
//
// See docs/plans/01-bazel-builtins-emulation/ for the full design.
package version

// Version names a Bazel LTS major. See the LTS support matrix at
// https://bazel.build/release for end-of-support dates per major.
//
// Numeric order matches the Bazel major: V7 < V8 < V9, so callers
// can write `if v >= V8 { ... }` and have it mean what a Bazel
// engineer would expect. VLatest is an alias for the active LTS
// (currently V9) so `v >= V8` includes VLatest without needing
// `|| v == VLatest`.
type Version int

const (
	// _ leaves iota=0 unallocated so the zero value of Version is
	// neither V7 nor an LTS marker; callers who care can use
	// Resolved() to turn an unset value into Latest().
	_ Version = iota

	// V7 targets the Bazel 7.x series (maintenance through Dec 2026).
	// Latest patch: 7.7.1.
	V7

	// V8 targets the Bazel 8.x series (maintenance through Dec 2027).
	// Latest patch: 8.7.0. Marquee feature: symbolic macros (macro()).
	V8

	// V9 targets the Bazel 9.x series (active LTS through Dec 2028).
	// Latest patch: 9.1.0. Marquee features: repo_metadata,
	// extension_metadata(facts=), --enable_workspace default-off.
	V9
)

// VLatest is an alias for the active LTS version. Bumped as new
// Bazel LTS majors stabilize. Aliased so `v >= V8` includes VLatest
// without a disjunction.
const VLatest = V9

// Latest returns the active LTS version. Currently V9. Bumped as new
// Bazel LTS majors stabilize. Identical to the VLatest constant;
// retained as a function for the rare caller that needs the value at
// runtime rather than as a constant expression.
func Latest() Version { return V9 }

// String returns the human-readable name of the version. VLatest
// is an alias for the active LTS so it prints as that LTS's name
// ("v9" today); use Resolved() before stringifying if you want
// the canonical name.
func (v Version) String() string {
	switch v {
	case V7:
		return "v7"
	case V8:
		return "v8"
	case V9:
		return "v9"
	}
	return "unknown"
}

// Resolved returns the concrete LTS version. With VLatest aliased
// to the active LTS, Resolved is now a no-op for all non-zero
// values — kept for API compatibility with consumers that defensively
// resolve before comparing.
func (v Version) Resolved() Version {
	if v == 0 {
		return Latest()
	}
	return v
}
