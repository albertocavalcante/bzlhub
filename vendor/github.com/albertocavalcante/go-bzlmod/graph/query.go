package graph

import (
	"fmt"
	"strings"
)

// Get returns the node for a module key, or nil if not found.
func (g *Graph) Get(key ModuleKey) *Node {
	return g.Modules[key]
}

// GetByName returns the node for a module by name (any version).
// Returns nil if not found.
func (g *Graph) GetByName(name string) *Node {
	for key, node := range g.Modules {
		if key.Name == name {
			return node
		}
	}
	return nil
}

// Contains returns true if the graph contains the given module.
func (g *Graph) Contains(key ModuleKey) bool {
	_, ok := g.Modules[key]
	return ok
}

// ContainsName returns true if the graph contains a module with the given name.
func (g *Graph) ContainsName(name string) bool {
	return g.GetByName(name) != nil
}

// DirectDeps returns the direct dependencies of a module.
func (g *Graph) DirectDeps(key ModuleKey) []ModuleKey {
	if node := g.Modules[key]; node != nil {
		return node.Dependencies
	}
	return nil
}

// DirectDependents returns modules that directly depend on the given module.
func (g *Graph) DirectDependents(key ModuleKey) []ModuleKey {
	if node := g.Modules[key]; node != nil {
		return node.Dependents
	}
	return nil
}

// TransitiveDeps returns all transitive dependencies of a module.
// The result is in breadth-first order.
func (g *Graph) TransitiveDeps(key ModuleKey) []ModuleKey {
	result := make([]ModuleKey, 0)
	visited := make(map[ModuleKey]bool)

	queue := []ModuleKey{key}
	visited[key] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		node := g.Modules[current]
		if node == nil {
			continue
		}

		for _, dep := range node.Dependencies {
			if !visited[dep] {
				visited[dep] = true
				result = append(result, dep)
				queue = append(queue, dep)
			}
		}
	}

	return result
}

// TransitiveDependents returns all modules that transitively depend on the given module.
// The result is in breadth-first order (closest dependents first).
func (g *Graph) TransitiveDependents(key ModuleKey) []ModuleKey {
	result := make([]ModuleKey, 0)
	visited := make(map[ModuleKey]bool)

	queue := []ModuleKey{key}
	visited[key] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		node := g.Modules[current]
		if node == nil {
			continue
		}

		for _, dep := range node.Dependents {
			if !visited[dep] {
				visited[dep] = true
				result = append(result, dep)
				queue = append(queue, dep)
			}
		}
	}

	return result
}

// Path finds the shortest dependency path from one module to another.
// Returns nil if no path exists.
func (g *Graph) Path(from, to ModuleKey) []ModuleKey {
	if from == to {
		return []ModuleKey{from}
	}

	// BFS to find shortest path
	type queueItem struct {
		key  ModuleKey
		path []ModuleKey
	}

	visited := make(map[ModuleKey]bool)
	queue := []queueItem{{key: from, path: []ModuleKey{from}}}
	visited[from] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		node := g.Modules[current.key]
		if node == nil {
			continue
		}

		for _, dep := range node.Dependencies {
			if dep == to {
				return append(current.path, dep)
			}
			if !visited[dep] {
				visited[dep] = true
				newPath := make([]ModuleKey, len(current.path)+1)
				copy(newPath, current.path)
				newPath[len(current.path)] = dep
				queue = append(queue, queueItem{key: dep, path: newPath})
			}
		}
	}

	return nil
}

// AllPaths finds all dependency paths from one module to another.
// This can be expensive for large graphs with many paths.
func (g *Graph) AllPaths(from, to ModuleKey) [][]ModuleKey {
	var result [][]ModuleKey
	g.findAllPaths(from, to, []ModuleKey{from}, make(map[ModuleKey]bool), &result)
	return result
}

func (g *Graph) findAllPaths(current, target ModuleKey, path []ModuleKey, visited map[ModuleKey]bool, result *[][]ModuleKey) {
	if current == target {
		pathCopy := make([]ModuleKey, len(path))
		copy(pathCopy, path)
		*result = append(*result, pathCopy)
		return
	}

	visited[current] = true
	defer func() { visited[current] = false }()

	node := g.Modules[current]
	if node == nil {
		return
	}

	for _, dep := range node.Dependencies {
		if !visited[dep] {
			g.findAllPaths(dep, target, append(path, dep), visited, result)
		}
	}
}

// Explain returns a detailed explanation of why a module is at its current version.
func (g *Graph) Explain(moduleName string) (*Explanation, error) {
	node := g.GetByName(moduleName)
	if node == nil {
		return nil, fmt.Errorf("module %q not found in graph", moduleName)
	}

	explanation := &Explanation{
		Module:    node.Key,
		Selection: node.Selection,
	}

	// Find all paths from root to this module
	paths := g.AllPaths(g.Root, node.Key)
	for _, path := range paths {
		chain := DependencyChain{
			Path: path,
		}
		// Get the requested version from the immediate parent
		// Path must have at least 2 nodes to have a parent
		if len(path) >= 2 {
			parent := path[len(path)-2]
			if requestedVersion, ok := node.RequestedVersions[parent]; ok {
				chain.RequestedVersion = requestedVersion
			}
		}
		explanation.DependencyChains = append(explanation.DependencyChains, chain)
	}

	// Build request summary
	explanation.RequestSummary = g.buildRequestSummary(node)

	return explanation, nil
}

func (g *Graph) buildRequestSummary(node *Node) string {
	if node.Selection == nil || len(node.Selection.Candidates) == 0 {
		return fmt.Sprintf("%s is at version %s", node.Key.Name, node.Key.Version)
	}

	var parts []string
	for _, candidate := range node.Selection.Candidates {
		requesters := make([]string, len(candidate.RequestedBy))
		for i, r := range candidate.RequestedBy {
			requesters[i] = r.String()
		}
		part := fmt.Sprintf("  %s requested by: %s", candidate.Version, strings.Join(requesters, ", "))
		if candidate.Selected {
			part += " [SELECTED]"
		}
		parts = append(parts, part)
	}

	return fmt.Sprintf("%s version selection:\n%s\nStrategy: %s (%s)",
		node.Key.Name,
		strings.Join(parts, "\n"),
		node.Selection.Strategy,
		node.Selection.DecidingFactor,
	)
}

// WhyIncluded returns all dependency chains that cause a module to be included.
func (g *Graph) WhyIncluded(moduleName string) ([]DependencyChain, error) {
	node := g.GetByName(moduleName)
	if node == nil {
		return nil, fmt.Errorf("module %q not found in graph", moduleName)
	}

	paths := g.AllPaths(g.Root, node.Key)
	chains := make([]DependencyChain, len(paths))
	for i, path := range paths {
		chains[i] = DependencyChain{Path: path}
	}

	return chains, nil
}

// Stats returns statistics about the graph.
func (g *Graph) Stats() GraphStats {
	stats := GraphStats{
		TotalModules: len(g.Modules),
	}

	// Count direct dependencies of root
	if root := g.Modules[g.Root]; root != nil {
		stats.DirectDependencies = len(root.Dependencies)
	}

	// Count transitive (all minus root and direct)
	stats.TransitiveDependencies = stats.TotalModules - stats.DirectDependencies - 1
	if stats.TransitiveDependencies < 0 {
		stats.TransitiveDependencies = 0
	}

	// Count dev dependencies
	for _, node := range g.Modules {
		if node.DevDependency {
			stats.DevDependencies++
		}
	}

	// Calculate max depth
	stats.MaxDepth = g.calculateMaxDepth()

	return stats
}

func (g *Graph) calculateMaxDepth() int {
	depths := make(map[ModuleKey]int)
	onPath := make(map[ModuleKey]bool)
	var maxDepth int

	var dfs func(key ModuleKey, depth int)
	dfs = func(key ModuleKey, depth int) {
		// Follow Bazel's cycle-safe traversal pattern (ModExecutor.notCycle):
		// if a node is already on the current DFS path, this edge is a cycle back-edge.
		if onPath[key] {
			return
		}
		if existingDepth, ok := depths[key]; ok && existingDepth >= depth {
			return
		}

		onPath[key] = true
		defer delete(onPath, key)

		depths[key] = depth
		if depth > maxDepth {
			maxDepth = depth
		}

		node := g.Modules[key]
		if node == nil {
			return
		}
		for _, dep := range node.Dependencies {
			dfs(dep, depth+1)
		}
	}

	dfs(g.Root, 0)
	return maxDepth
}

// Roots returns all root nodes (nodes with no dependents).
// In a typical dependency graph, there should be exactly one root.
func (g *Graph) Roots() []ModuleKey {
	var roots []ModuleKey
	for key, node := range g.Modules {
		if len(node.Dependents) == 0 {
			roots = append(roots, key)
		}
	}
	return roots
}

// Leaves returns all leaf nodes (nodes with no dependencies).
func (g *Graph) Leaves() []ModuleKey {
	var leaves []ModuleKey
	for key, node := range g.Modules {
		if len(node.Dependencies) == 0 {
			leaves = append(leaves, key)
		}
	}
	return leaves
}

// HasCycles returns true if the graph contains cycles.
func (g *Graph) HasCycles() bool {
	visited := make(map[ModuleKey]bool)
	recStack := make(map[ModuleKey]bool)

	var hasCycle func(key ModuleKey) bool
	hasCycle = func(key ModuleKey) bool {
		visited[key] = true
		recStack[key] = true

		node := g.Modules[key]
		if node != nil {
			for _, dep := range node.Dependencies {
				if !visited[dep] {
					if hasCycle(dep) {
						return true
					}
				} else if recStack[dep] {
					return true
				}
			}
		}

		recStack[key] = false
		return false
	}

	for key := range g.Modules {
		if !visited[key] {
			if hasCycle(key) {
				return true
			}
		}
	}

	return false
}

// FindCycles returns all cycles in the graph.
func (g *Graph) FindCycles() [][]ModuleKey {
	var cycles [][]ModuleKey
	visited := make(map[ModuleKey]bool)
	recStack := make(map[ModuleKey]bool)
	path := make([]ModuleKey, 0)

	var findCycles func(key ModuleKey)
	findCycles = func(key ModuleKey) {
		visited[key] = true
		recStack[key] = true
		path = append(path, key)

		node := g.Modules[key]
		if node != nil {
			for _, dep := range node.Dependencies {
				if !visited[dep] {
					findCycles(dep)
				} else if recStack[dep] {
					// Found a cycle, extract it
					cycleStart := -1
					for i, k := range path {
						if k == dep {
							cycleStart = i
							break
						}
					}
					if cycleStart >= 0 {
						cycle := make([]ModuleKey, len(path)-cycleStart)
						copy(cycle, path[cycleStart:])
						cycles = append(cycles, cycle)
					}
				}
			}
		}

		path = path[:len(path)-1]
		recStack[key] = false
	}

	for key := range g.Modules {
		if !visited[key] {
			findCycles(key)
		}
	}

	return cycles
}
