// Package selection implements Bazel's module version selection algorithm.
//
// This is a Go port of Bazel's Selection.java, implementing the Minimal Version Selection
// algorithm with Bazel-specific extensions for compatibility levels and overrides.
//
// Reference implementations:
//   - Selection.java: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java
//   - InterimModule.java: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java
//   - ModuleKey.java: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleKey.java
package selection

// ModuleKey uniquely identifies a module in the dependency graph.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleKey.java
type ModuleKey struct {
	Name    string
	Version string
}

// String returns the module key as "name@version" or "name@_" if version is empty.
func (k ModuleKey) String() string {
	if k.Version == "" {
		return k.Name + "@_"
	}
	return k.Name + "@" + k.Version
}

// DepSpec represents a dependency specification.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java#L59
type DepSpec struct {
	Name    string
	Version string
	// MaxCompatibilityLevel allows depending on modules with compatibility levels
	// up to this value. -1 means no max (use the dep's own compat level).
	// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java#L61
	MaxCompatibilityLevel int
}

// ToModuleKey converts a DepSpec to a ModuleKey.
func (d DepSpec) ToModuleKey() ModuleKey {
	return ModuleKey{Name: d.Name, Version: d.Version}
}

// Module represents a node in the dependency graph during resolution.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java
type Module struct {
	Key         ModuleKey
	Deps        []DepSpec
	CompatLevel int // The compatibility_level from module(), default 0

	// NodepDeps are dependencies that participate in version selection but don't
	// create transitive dependency edges during graph pruning. Modules reachable
	// only via NodepDeps are not included in the final resolved graph.
	// Introduced in Bazel 7.6+.
	// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L397-L403
	NodepDeps []DepSpec
}

// DepGraph represents the complete dependency graph before selection.
type DepGraph struct {
	Modules map[ModuleKey]*Module
	RootKey ModuleKey
}

// Override is the interface for all override types.
type Override interface {
	isOverride()
}

// SingleVersionOverride forces a specific version for a module.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/SingleVersionOverride.java
type SingleVersionOverride struct {
	Version  string
	Registry string
	Patches  []string
}

func (o *SingleVersionOverride) isOverride() {}

// MultipleVersionOverride allows multiple versions of the same module.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/MultipleVersionOverride.java
//
// From Selection.java lines 64-73:
// "If module foo has a multiple-version override which allows versions [1.3, 1.5, 2.0],
// then we further split the selection groups by the target allowed version."
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L64-L73
type MultipleVersionOverride struct {
	Versions []string
	Registry string
}

func (o *MultipleVersionOverride) isOverride() {}

// NonRegistryOverride represents git_override, local_path_override, or archive_override.
// These override the module source entirely, so version becomes empty.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/NonRegistryOverride.java
type NonRegistryOverride struct {
	// Type is "git", "local_path", or "archive"
	Type string

	// Path is the local filesystem path for local_path overrides.
	Path string
}

func (o *NonRegistryOverride) isOverride() {}

// Result contains the output of the selection algorithm.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L88-L100
type Result struct {
	// ResolvedGraph is the final dep graph with unused modules removed.
	// Sorted in BFS iteration order.
	ResolvedGraph map[ModuleKey]*Module

	// UnprunedGraph contains all modules including unused ones.
	// Useful for inspection/debugging.
	UnprunedGraph map[ModuleKey]*Module

	// BFSOrder maintains the breadth-first traversal order of modules.
	BFSOrder []ModuleKey
}

// SelectionGroup identifies a group of module versions that compete for selection.
// One version is selected per SelectionGroup.
//
// "During selection, a version is selected for each distinct 'selection group'."
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L102-L107
type SelectionGroup struct {
	ModuleName  string
	CompatLevel int
	// TargetAllowedVersion is only used for modules with multiple-version overrides.
	// Empty string means no multiple-version override.
	TargetAllowedVersion string
}

// SelectionError represents an error during version selection.
type SelectionError struct {
	Code    string
	Message string
}

func (e *SelectionError) Error() string {
	return e.Message
}
