// Package selection implements Bazel's module version selection algorithm.
//
// This is a Go port of Bazel's Selection.java implementing Minimal Version Selection (MVS)
// with Bazel-specific extensions for compatibility levels and overrides.
//
// # Algorithm Overview
//
// The selection algorithm is documented in Bazel's Selection.java (lines 44-84):
//
//	"Runs module selection. This step of module resolution reads the output of Discovery
//	and applies the Minimal Version Selection algorithm to it, removing unselected modules
//	from the dependency graph and rewriting dependencies to point to the selected versions."
//
// # Basic MVS Case
//
// From Selection.java lines 51-58:
//
//	"In the most basic case, only one version of each module is selected (ie. remains in
//	the dep graph). The selected version is simply the highest among all existing versions
//	in the dep graph. In other words, each module name forms a 'selection group'. If foo@1.5
//	is selected, then any other foo@X is removed from the dep graph, and any module
//	depending on foo@X will depend on foo@1.5 instead."
//
// # Unreachable Module Removal
//
// From Selection.java lines 58-59:
//
//	"As an extension of the above, we also remove any module that becomes unreachable from
//	the root module because of the removal of some other module."
//
// # Compatibility Levels
//
// From Selection.java lines 60-63:
//
//	"If, however, versions of the same module but with different compatibility levels exist
//	in the dep graph, then one version is selected for each compatibility level (ie. we
//	split the selection groups by compatibility level). In the end, though, still only one
//	version can remain in the dep graph after the removal of unselected and unreachable
//	modules."
//
// # Multiple-Version Overrides
//
// From Selection.java lines 64-73:
//
//	"Things get more complicated with multiple-version overrides. If module foo has a
//	multiple-version override which allows versions [1.3, 1.5, 2.0] (using the major
//	version as the compatibility level), then we further split the selection groups by
//	the target allowed version (keep in mind that versions are upgraded to the nearest
//	higher-or-equal allowed version at the same compatibility level). If, for example,
//	some module depends on foo@1.0, then it'll depend on foo@1.3 post-selection instead
//	(and foo@1.0 will be removed). If any of foo@1.7, foo@2.2, or foo@3.0 exist in the
//	dependency graph before selection, they must be removed before the end of selection
//	(by becoming unreachable, for example), otherwise it'll be an error since they're
//	not allowed by the override."
//
// # Selection Groups
//
// From Selection.java lines 102-107:
//
//	"During selection, a version is selected for each distinct 'selection group'.
//	record SelectionGroup(String moduleName, int compatibilityLevel, Version targetAllowedVersion)"
//
// For modules without multiple-version overrides, the selection group is simply
// (moduleName, compatibilityLevel). For modules with multiple-version overrides, it
// additionally includes the targetAllowedVersion.
//
// # Version Comparison
//
// Version comparison follows Bazel's Version.java (lines 33-63):
//
//	"The version format we support is RELEASE[-PRERELEASE][+BUILD], where RELEASE,
//	PRERELEASE, and BUILD are each a sequence of 'identifiers' (defined as a non-empty
//	sequence of ASCII alphanumerical characters and hyphens) separated by dots.
//	The RELEASE part may not contain hyphens."
//
// Key comparison rules (Version.java lines 182-191):
//   - Empty version (signifying non-registry override) compares HIGHER than everything
//   - Release segments are compared lexicographically using identifier rules
//   - Prerelease versions compare LOWER than the same version without prerelease
//   - Prerelease segments are compared lexicographically
//
// Identifier comparison (Version.java lines 111-114):
//   - Digits-only identifiers sort BEFORE alphanumeric identifiers
//   - Digits-only identifiers compare numerically
//   - Alphanumeric identifiers compare lexicographically
//
// # Differences from Pure MVS
//
// Bazel's implementation extends Russ Cox's pure MVS algorithm with:
//
//  1. Compatibility levels: Modules with different compatibility levels can initially
//     coexist in the graph, but conflicts are detected during the walk phase.
//
//  2. Multiple-version overrides: Allow multiple versions of the same module in the
//     final dependency graph (rare, advanced use case).
//
//  3. Version snapping: With multiple-version overrides, versions snap to the nearest
//     allowed version using the ceiling method.
//
// 4. Graph pruning: Unreachable modules are removed in a BFS walk from the root.
//
// # References
//
// Bazel source files (as of 2024):
//   - Selection.java: Core selection algorithm
//   - Version.java: Version parsing and comparison
//   - Discovery.java: Dependency graph discovery (BFS)
//   - InterimModule.java: Module representation during resolution
//   - BazelModuleResolutionFunction.java: Resolution orchestration
//
// Russ Cox's MVS research: https://research.swtch.com/vgo-mvs
package selection
