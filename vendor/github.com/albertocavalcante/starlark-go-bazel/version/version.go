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
type Version int

const (
	// VLatest is the active LTS — currently Bazel 9.
	VLatest Version = iota

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

// Latest returns the active LTS version. Currently V9. Bumped as new
// Bazel LTS majors stabilize.
func Latest() Version { return V9 }

// String returns the human-readable name of the version.
func (v Version) String() string {
	switch v {
	case VLatest:
		return "latest"
	case V7:
		return "v7"
	case V8:
		return "v8"
	case V9:
		return "v9"
	}
	return "unknown"
}

// Resolved returns the concrete LTS version, replacing VLatest with
// the actual most-recent stable. Callers that compare versions should
// resolve first.
func (v Version) Resolved() Version {
	if v == VLatest {
		return Latest()
	}
	return v
}
