// Package graph provides dependency graph representation and query capabilities
// for Bazel module resolution.
//
// This package supports functionality equivalent to Bazel's `bazel mod graph`
// and `bazel mod explain` commands, allowing users to:
//
//   - Visualize the complete dependency graph
//   - Explain why a module is at a particular version
//   - Find dependency paths between modules
//   - Query direct and transitive dependencies
//
// # Building a Graph
//
// A Graph is automatically built during resolution:
//
//	result, _ := bzlmod.Resolve(ctx, moduleContent, opts)
//	graph := result.Graph // already populated
//
// # Querying the Graph
//
// Once built, the graph supports various queries:
//
//	// Get direct dependencies
//	deps := graph.DirectDeps(moduleKey)
//
//	// Explain version selection
//	explanation, _ := graph.Explain("rules_go")
//
//	// Find path between modules
//	path, _ := graph.Path(fromKey, toKey)
//
// # Output Formats
//
// The graph can be serialized to multiple formats:
//
//	// Bazel-compatible JSON (matches `bazel mod graph --output=json`)
//	jsonBytes, _ := graph.ToJSON()
//
//	// Graphviz DOT format for visualization
//	dotString := graph.ToDOT()
//
//	// Human-readable text
//	textString := graph.ToText()
package graph
