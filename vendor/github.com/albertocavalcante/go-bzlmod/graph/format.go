package graph

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const separatorWidth = 60 // Width of separator lines in text output

// BazelModGraph represents Bazel's mod graph JSON output structure.
// This matches the output of `bazel mod graph --output=json`.
type BazelModGraph struct {
	Key                  string            `json:"key"`
	Name                 string            `json:"name,omitempty"`
	Version              string            `json:"version,omitempty"`
	Dependencies         []BazelDependency `json:"dependencies,omitempty"`
	IndirectDependencies []BazelDependency `json:"indirectDependencies,omitempty"`
	Cycles               []BazelDependency `json:"cycles,omitempty"`
	Root                 bool              `json:"root,omitempty"`
}

// BazelDependency represents a dependency in Bazel's module graph.
type BazelDependency struct {
	Key                  string            `json:"key"`
	Dependencies         []BazelDependency `json:"dependencies,omitempty"`
	IndirectDependencies []BazelDependency `json:"indirectDependencies,omitempty"`
	Cycles               []BazelDependency `json:"cycles,omitempty"`
	Unexpanded           bool              `json:"unexpanded,omitempty"`
}

// ToJSON outputs the graph in Bazel-compatible mod graph JSON format.
func (g *Graph) ToJSON() ([]byte, error) {
	bazelGraph := g.toBazelFormat()
	return json.MarshalIndent(bazelGraph, "", "  ")
}

// toBazelFormat converts the graph to Bazel's JSON format.
func (g *Graph) toBazelFormat() *BazelModGraph {
	rootNode := g.Modules[g.Root]
	if rootNode == nil {
		return &BazelModGraph{}
	}

	visited := make(map[ModuleKey]bool)
	cycles := g.FindCycles()
	cycleKeys := make(map[ModuleKey]bool)
	for _, cycle := range cycles {
		for _, key := range cycle {
			cycleKeys[key] = true
		}
	}

	return &BazelModGraph{
		Key:          g.Root.String(),
		Name:         g.Root.Name,
		Version:      g.Root.Version,
		Root:         true,
		Dependencies: g.buildBazelDeps(rootNode, visited, cycleKeys),
	}
}

// buildBazelDeps recursively builds Bazel-format dependencies.
func (g *Graph) buildBazelDeps(node *Node, visited, cycleKeys map[ModuleKey]bool) []BazelDependency {
	if node == nil {
		return nil
	}

	deps := make([]BazelDependency, 0, len(node.Dependencies))

	for _, depKey := range node.Dependencies {
		if visited[depKey] {
			// Already visited, mark as unexpanded to avoid infinite recursion
			deps = append(deps, BazelDependency{
				Key:        depKey.String(),
				Unexpanded: true,
			})
			continue
		}

		visited[depKey] = true
		depNode := g.Modules[depKey]

		bazelDep := BazelDependency{
			Key: depKey.String(),
		}

		if cycleKeys[depKey] {
			// This node is part of a cycle
			bazelDep.Cycles = []BazelDependency{{Key: depKey.String()}}
		} else if depNode != nil {
			bazelDep.Dependencies = g.buildBazelDeps(depNode, visited, cycleKeys)
		}

		deps = append(deps, bazelDep)
	}

	return deps
}

// ToDOT outputs the graph in Graphviz DOT format.
func (g *Graph) ToDOT() string {
	var buf bytes.Buffer

	buf.WriteString("digraph dependencies {\n")
	buf.WriteString("  rankdir=LR;\n")
	buf.WriteString("  node [shape=box];\n\n")

	// Add nodes (using explicit quotes for DOT format compatibility)
	for key, node := range g.Modules {
		label := fmt.Sprintf("%s\\n%s", key.Name, key.Version)
		attrs := fmt.Sprintf(`label="%s"`, label) //nolint:gocritic // DOT format requires this quote style
		if node.IsRoot {
			attrs += ", style=bold"
		}
		if node.DevDependency {
			attrs += ", style=dashed"
		}
		fmt.Fprintf(&buf, "  %q [%s];\n", key.String(), attrs)
	}

	buf.WriteString("\n")

	// Add edges
	for key, node := range g.Modules {
		for _, dep := range node.Dependencies {
			fmt.Fprintf(&buf, "  %q -> %q;\n", key.String(), dep.String())
		}
	}

	buf.WriteString("}\n")
	return buf.String()
}

// ToText outputs a human-readable text representation of the graph.
func (g *Graph) ToText() string {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "Dependency Graph (root: %s)\n", g.Root.String())
	buf.WriteString(strings.Repeat("=", separatorWidth) + "\n\n")

	// Get stats
	stats := g.Stats()
	fmt.Fprintf(&buf, "Total modules: %d\n", stats.TotalModules)
	fmt.Fprintf(&buf, "Direct dependencies: %d\n", stats.DirectDependencies)
	fmt.Fprintf(&buf, "Transitive dependencies: %d\n", stats.TransitiveDependencies)
	fmt.Fprintf(&buf, "Max depth: %d\n", stats.MaxDepth)
	if stats.DevDependencies > 0 {
		fmt.Fprintf(&buf, "Dev dependencies: %d\n", stats.DevDependencies)
	}
	buf.WriteString("\n")

	// Sort modules for deterministic output
	keys := make([]ModuleKey, 0, len(g.Modules))
	for key := range g.Modules {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b ModuleKey) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Version, b.Version)
	})

	// Print tree from root
	buf.WriteString("Dependency Tree:\n")
	visited := make(map[ModuleKey]bool)
	g.printTree(&buf, g.Root, "", true, visited)

	return buf.String()
}

func (g *Graph) printTree(buf *bytes.Buffer, key ModuleKey, prefix string, isLast bool, visited map[ModuleKey]bool) {
	// Print current node
	connector := "├── "
	if isLast {
		connector = "└── "
	}
	if prefix == "" {
		buf.WriteString(key.String())
	} else {
		buf.WriteString(prefix + connector + key.String())
	}

	node := g.Modules[key]
	if node != nil && node.DevDependency {
		buf.WriteString(" (dev)")
	}

	if visited[key] {
		buf.WriteString(" (circular)\n")
		return
	}
	buf.WriteString("\n")

	visited[key] = true
	defer func() { visited[key] = false }()

	if node == nil {
		return
	}

	// Print children
	for i, dep := range node.Dependencies {
		isLastChild := i == len(node.Dependencies)-1
		childPrefix := prefix
		if prefix != "" {
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
		}
		g.printTree(buf, dep, childPrefix, isLastChild, visited)
	}
}

// ToExplainText outputs a human-readable explanation for a specific module.
func (g *Graph) ToExplainText(moduleName string) (string, error) {
	explanation, err := g.Explain(moduleName)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer

	fmt.Fprintf(&buf, "Explanation for: %s\n", explanation.Module.String())
	buf.WriteString(strings.Repeat("=", separatorWidth) + "\n\n")

	// Version selection info
	if explanation.Selection != nil {
		buf.WriteString("Version Selection:\n")
		fmt.Fprintf(&buf, "  Selected version: %s\n", explanation.Selection.SelectedVersion)
		fmt.Fprintf(&buf, "  Strategy: %s\n", explanation.Selection.Strategy)
		fmt.Fprintf(&buf, "  Deciding factor: %s\n", explanation.Selection.DecidingFactor)

		if len(explanation.Selection.Candidates) > 0 {
			buf.WriteString("\n  Candidates considered:\n")
			for _, c := range explanation.Selection.Candidates {
				status := "  "
				if c.Selected {
					status = "✓ "
				}
				requesters := make([]string, len(c.RequestedBy))
				for i, r := range c.RequestedBy {
					requesters[i] = r.String()
				}
				fmt.Fprintf(&buf, "    %s%s - requested by: %s\n",
					status, c.Version, strings.Join(requesters, ", "))
				if !c.Selected && c.RejectionReason != "" {
					fmt.Fprintf(&buf, "      Reason not selected: %s\n", c.RejectionReason)
				}
			}
		}
	}

	// Dependency chains
	if len(explanation.DependencyChains) > 0 {
		buf.WriteString("\nDependency Chains (paths from root):\n")
		for i, chain := range explanation.DependencyChains {
			fmt.Fprintf(&buf, "  %d. %s\n", i+1, chain.String())
		}
	}

	return buf.String(), nil
}

// ToModuleList outputs a flat list of modules, similar to ResolutionList.
func (g *Graph) ToModuleList() []ModuleInfo {
	modules := make([]ModuleInfo, 0, len(g.Modules))

	for key, node := range g.Modules {
		if key == g.Root {
			continue // Skip root module
		}

		requiredBy := make([]string, len(node.Dependents))
		for i, dep := range node.Dependents {
			requiredBy[i] = dep.String()
		}

		modules = append(modules, ModuleInfo{
			Name:          key.Name,
			Version:       key.Version,
			DevDependency: node.DevDependency,
			RequiredBy:    requiredBy,
		})
	}

	// Sort by name for deterministic output
	slices.SortFunc(modules, func(a, b ModuleInfo) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return modules
}

// ModuleInfo represents a module in the flat list output.
type ModuleInfo struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	DevDependency bool     `json:"dev_dependency,omitempty"`
	RequiredBy    []string `json:"required_by,omitempty"`
}
