package graph

import (
	"fmt"

	"github.com/albertocavalcante/go-bzlmod/selection"
)

// ModuleKey is an alias for selection.ModuleKey to avoid import cycles
// and provide a consistent API within the graph package.
type ModuleKey = selection.ModuleKey

// Graph represents a resolved module dependency graph.
// It supports bidirectional traversal (dependencies and dependents)
// and provides query methods for explaining version selections.
type Graph struct {
	// Root is the root module of the graph.
	Root ModuleKey

	// Modules contains all nodes in the graph, keyed by ModuleKey.
	Modules map[ModuleKey]*Node
}

// Node represents a module in the dependency graph.
type Node struct {
	// Key uniquely identifies this module.
	Key ModuleKey

	// Dependencies are the direct dependencies of this module (resolved versions).
	Dependencies []ModuleKey

	// Dependents are modules that directly depend on this one (reverse edges).
	Dependents []ModuleKey

	// RequestedVersions tracks which modules requested which versions of this module.
	// Key is the requesting module, value is the version they requested.
	RequestedVersions map[ModuleKey]string

	// Selection contains information about why this version was selected.
	Selection *SelectionInfo

	// IsRoot is true if this is the root module.
	IsRoot bool

	// DevDependency is true if this module is only a dev dependency.
	DevDependency bool
}

// SelectionInfo explains why a particular version was selected.
type SelectionInfo struct {
	// Strategy is how the version was selected: "mvs", "override", "root".
	Strategy SelectionStrategy

	// SelectedVersion is the version that was selected.
	SelectedVersion string

	// Candidates are all versions that were considered during selection.
	Candidates []VersionCandidate

	// DecidingFactor explains what determined the selection.
	DecidingFactor string
}

// SelectionStrategy indicates how a version was selected.
type SelectionStrategy string

const (
	// StrategyMVS indicates the version was selected by Minimal Version Selection.
	StrategyMVS SelectionStrategy = "mvs"

	// StrategyOverride indicates the version was forced by an override.
	StrategyOverride SelectionStrategy = "override"

	// StrategySingleVersion indicates a single_version_override was applied.
	StrategySingleVersion SelectionStrategy = "single_version_override"

	// StrategyRoot indicates this is the root module (no selection needed).
	StrategyRoot SelectionStrategy = "root"
)

// VersionCandidate represents a version that was considered during selection.
type VersionCandidate struct {
	// Version is the version string.
	Version string

	// RequestedBy lists modules that requested this version.
	RequestedBy []ModuleKey

	// Selected indicates if this version was selected.
	Selected bool

	// RejectionReason explains why this version was not selected (if applicable).
	RejectionReason string
}

// Explanation provides a detailed explanation of why a module is at its current version.
type Explanation struct {
	// Module is the module being explained.
	Module ModuleKey

	// Selection explains how the version was selected.
	Selection *SelectionInfo

	// DependencyChains shows all paths from the root to this module.
	DependencyChains []DependencyChain

	// RequestSummary summarizes all version requests for this module.
	RequestSummary string
}

// DependencyChain represents a path of dependencies from root to a module.
type DependencyChain struct {
	// Path is the sequence of modules from root to target.
	Path []ModuleKey

	// RequestedVersion is the version requested at the end of this chain.
	RequestedVersion string
}

// String returns a human-readable representation of the chain.
func (c DependencyChain) String() string {
	if len(c.Path) == 0 {
		return ""
	}
	result := c.Path[0].String()
	for i := 1; i < len(c.Path); i++ {
		result += " -> " + c.Path[i].String()
	}
	if c.RequestedVersion != "" {
		result += fmt.Sprintf(" (requested %s)", c.RequestedVersion)
	}
	return result
}

// GraphStats provides statistics about the graph.
type GraphStats struct {
	// TotalModules is the total number of modules in the graph.
	TotalModules int

	// DirectDependencies is the number of direct dependencies of the root.
	DirectDependencies int

	// TransitiveDependencies is the number of transitive dependencies.
	TransitiveDependencies int

	// MaxDepth is the maximum depth of the dependency tree.
	MaxDepth int

	// DevDependencies is the number of dev-only dependencies.
	DevDependencies int
}
