package graph

import (
	"github.com/albertocavalcante/go-bzlmod/selection"
)

// Builder constructs a Graph from resolution results.
type Builder struct {
	// PreSelectionRequests contains all version requests before MVS selection.
	// Map structure: moduleName -> version -> list of requesters
	PreSelectionRequests map[string]map[string][]string

	// Overrides contains modules that have version overrides.
	Overrides map[string]string
}

// NewBuilder creates a new graph builder.
func NewBuilder() *Builder {
	return &Builder{
		PreSelectionRequests: make(map[string]map[string][]string),
		Overrides:            make(map[string]string),
	}
}

// RecordRequest records a version request for later explanation.
// Call this during dependency graph construction, before MVS selection.
func (b *Builder) RecordRequest(moduleName, version, requester string) {
	if b.PreSelectionRequests[moduleName] == nil {
		b.PreSelectionRequests[moduleName] = make(map[string][]string)
	}
	b.PreSelectionRequests[moduleName][version] = append(
		b.PreSelectionRequests[moduleName][version],
		requester,
	)
}

// RecordOverride records that a module has a version override.
func (b *Builder) RecordOverride(moduleName, version string) {
	b.Overrides[moduleName] = version
}

// BuildFromSelection constructs a Graph from selection results.
func (b *Builder) BuildFromSelection(result *selection.Result, rootKey selection.ModuleKey) *Graph {
	g := &Graph{
		Root:    rootKey,
		Modules: make(map[ModuleKey]*Node),
	}

	// First pass: create all nodes
	for selKey, module := range result.ResolvedGraph {
		node := &Node{
			Key:               selKey,
			Dependencies:      make([]ModuleKey, 0, len(module.Deps)),
			Dependents:        make([]ModuleKey, 0),
			RequestedVersions: make(map[ModuleKey]string),
			IsRoot:            selKey == rootKey,
		}

		// Convert dependencies
		for _, dep := range module.Deps {
			// Find the resolved version for this dependency
			resolvedKey := b.findResolvedVersion(result.ResolvedGraph, dep.Name)
			if resolvedKey != nil {
				node.Dependencies = append(node.Dependencies, *resolvedKey)
			}
		}

		// Build selection info
		node.Selection = b.buildSelectionInfo(selKey.Name, selKey.Version)

		g.Modules[selKey] = node
	}

	// Second pass: build reverse edges (dependents)
	for key, node := range g.Modules {
		for _, depKey := range node.Dependencies {
			if depNode, ok := g.Modules[depKey]; ok {
				depNode.Dependents = append(depNode.Dependents, key)
				depNode.RequestedVersions[key] = b.getRequestedVersion(key, depKey.Name)
			}
		}
	}

	return g
}

// findResolvedVersion finds the resolved version of a module by name.
func (b *Builder) findResolvedVersion(resolved map[selection.ModuleKey]*selection.Module, name string) *selection.ModuleKey {
	for key := range resolved {
		if key.Name == name {
			return &key
		}
	}
	return nil
}

// getRequestedVersion returns the version that was originally requested.
func (b *Builder) getRequestedVersion(requester ModuleKey, moduleName string) string {
	if versions, ok := b.PreSelectionRequests[moduleName]; ok {
		for version, requesters := range versions {
			for _, r := range requesters {
				if r == requester.String() || r == requester.Name {
					return version
				}
			}
		}
	}
	return ""
}

// buildSelectionInfo creates selection info for a module.
func (b *Builder) buildSelectionInfo(moduleName, selectedVersion string) *SelectionInfo {
	info := &SelectionInfo{
		SelectedVersion: selectedVersion,
		Candidates:      make([]VersionCandidate, 0),
	}

	// Check if this was an override
	if overrideVersion, ok := b.Overrides[moduleName]; ok {
		if overrideVersion == selectedVersion {
			info.Strategy = StrategyOverride
			info.DecidingFactor = "single_version_override"
			return info
		}
	}

	// Get all version candidates
	if versions, ok := b.PreSelectionRequests[moduleName]; ok {
		for version, requesters := range versions {
			candidate := VersionCandidate{
				Version:     version,
				RequestedBy: make([]ModuleKey, 0, len(requesters)),
				Selected:    version == selectedVersion,
			}

			for _, r := range requesters {
				// Parse requester string back to ModuleKey
				candidate.RequestedBy = append(candidate.RequestedBy, parseModuleKey(r))
			}

			if !candidate.Selected {
				candidate.RejectionReason = "lower version (MVS selects highest)"
			}

			info.Candidates = append(info.Candidates, candidate)
		}
	}

	// Determine strategy
	if len(info.Candidates) <= 1 {
		info.Strategy = StrategyMVS
		info.DecidingFactor = "only version requested"
	} else {
		info.Strategy = StrategyMVS
		info.DecidingFactor = "highest version among candidates"
	}

	return info
}

// parseModuleKey parses a "name@version" string into a ModuleKey.
func parseModuleKey(s string) ModuleKey {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			version := s[i+1:]
			if version == "_" {
				version = ""
			}
			return ModuleKey{Name: s[:i], Version: version}
		}
	}
	return ModuleKey{Name: s}
}

// Build constructs a Graph from a simple module list.
// This is a convenience method when full selection data isn't available.
func Build(root ModuleKey, modules []SimpleModule) *Graph {
	g := &Graph{
		Root:    root,
		Modules: make(map[ModuleKey]*Node),
	}

	// Create nodes
	for _, m := range modules {
		key := ModuleKey{Name: m.Name, Version: m.Version}
		node := &Node{
			Key:               key,
			Dependencies:      make([]ModuleKey, len(m.Dependencies)),
			Dependents:        make([]ModuleKey, 0),
			RequestedVersions: make(map[ModuleKey]string),
			IsRoot:            key == root,
			DevDependency:     m.DevDependency,
		}
		copy(node.Dependencies, m.Dependencies)
		g.Modules[key] = node
	}

	// Build reverse edges
	for key, node := range g.Modules {
		for _, depKey := range node.Dependencies {
			if depNode, ok := g.Modules[depKey]; ok {
				depNode.Dependents = append(depNode.Dependents, key)
			}
		}
	}

	return g
}

// SimpleModule is a simplified module representation for building graphs.
type SimpleModule struct {
	Name          string
	Version       string
	Dependencies  []ModuleKey
	DevDependency bool
}
