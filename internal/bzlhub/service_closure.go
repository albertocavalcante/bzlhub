package bzlhub

import (
	"context"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// closureMaxDepth caps the BFS so a degenerate graph (cycle in the
// bazel_dep declarations, or a deeply-pathological module) can't
// burn unbounded store reads. 10 is comfortably past any real-world
// Bazel module closure depth I've seen — go-bzlmod's MVS would
// flatten anything that deep — but bounded enough that the worst
// case stays tractable.
const closureMaxDepth = 10

// Closure walks the bazel_dep graph rooted at (name, version) via
// persisted ModuleReports. BFS so the first hit per module is the
// shortest path from root (matters for de-duping versions when two
// parents disagree — first writer wins, matching MVS semantics
// loosely enough for visualization).
//
// External nodes: any (name, version) referenced as a bazel_dep
// child but not present in the local store. They appear as leaf
// External=true nodes; the renderer dims them so the reader sees
// where the closure escapes canopy's index.
func (s *Service) Closure(ctx context.Context, name, version string) (*api.ClosureGraph, error) {
	g := &api.ClosureGraph{
		Root:  nodeKey(name, version),
		Nodes: []api.ClosureNode{},
		Edges: []api.ClosureEdge{},
	}
	type queued struct {
		name, version string
		depth         int
	}
	visited := map[string]bool{}
	queue := []queued{{name, version, 0}}
	addNode := func(n, v string, external bool) {
		key := nodeKey(n, v)
		if visited[key] {
			return
		}
		visited[key] = true
		g.Nodes = append(g.Nodes, api.ClosureNode{
			Name: n, Version: v, External: external,
		})
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		curKey := nodeKey(cur.name, cur.version)
		addNode(cur.name, cur.version, false)

		if cur.depth >= closureMaxDepth {
			g.MaxDepthReached = true
			continue
		}

		rep, err := s.store.GetReport(ctx, cur.name, cur.version)
		if err != nil || rep == nil {
			// Not in the store → leaf external (already added as a
			// non-external node above because we only know it as a
			// queued root, but the FIRST iteration treats root as
			// in-store. For any child queued and not found, this
			// branch fires and we mark it external retroactively).
			// Find the just-added node and flip External.
			for i := range g.Nodes {
				if nodeKey(g.Nodes[i].Name, g.Nodes[i].Version) == curKey {
					g.Nodes[i].External = true
					break
				}
			}
			continue
		}
		for _, dep := range rep.BazelDeps {
			// Compat-only deps with empty version are skipped — they
			// have nowhere to go, and a node with no version label
			// would be confusing in the graph.
			if dep.Version == "" || dep.Version == "0" {
				continue
			}
			childKey := nodeKey(dep.Name, dep.Version)
			g.Edges = append(g.Edges, api.ClosureEdge{From: curKey, To: childKey})
			if !visited[childKey] {
				queue = append(queue, queued{dep.Name, dep.Version, cur.depth + 1})
			}
		}
	}
	return g, nil
}

// nodeKey is the "name@version" stable identifier used in the
// graph's node + edge wire shape.
func nodeKey(name, version string) string {
	return name + "@" + version
}

// ReverseDeps walks every indexed (module, version) and returns the
// subset whose bazel_deps include the requested coordinate. O(N)
// store reads where N is the corpus size — fine for hundreds of
// modules, would want an inverted index at thousands. Documented as
// a deferred optimization rather than a fix-now: the data already
// answers the question, and a join table would need its own
// migration + backfill pass.
//
// Returned list is sorted by (name, version) for deterministic
// output. Empty Deps is a valid response — "nothing in the index
// uses this yet" is distinct information from "we couldn't look."
func (s *Service) ReverseDeps(ctx context.Context, name, version string) (*api.ReverseDeps, error) {
	out := &api.ReverseDeps{
		Module:  name,
		Version: version,
		Deps:    []api.ReverseDep{},
	}
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, mv := range rows {
		rep, err := s.store.GetReport(ctx, mv.Module, mv.Version)
		if err != nil || rep == nil {
			continue
		}
		for _, d := range rep.BazelDeps {
			if d.Name == name && d.Version == version {
				key := mv.Module + "@" + mv.Version
				if seen[key] {
					break
				}
				seen[key] = true
				out.Deps = append(out.Deps, api.ReverseDep{
					Name:    mv.Module,
					Version: mv.Version,
				})
				break
			}
		}
	}
	return out, nil
}

// IngestClosureMissing computes the closure of (name, version) and
// runs Bump for every external coordinate. Serial — one Bump at a
// time, since the bump pipeline does meaningful work (fetch +
// extract + SCIP) and we'd rather wait honestly than queue parallel
// network fetches against a single upstream.
//
// Per-coordinate failures collected, not propagated: a flaky
// upstream on one module shouldn't abort the others. Each
// successful bump emits its own module_indexed SSE event, so a
// subscribed UI sees progress unfold without polling.
func (s *Service) IngestClosureMissing(ctx context.Context, name, version string) (*api.IngestClosureResult, error) {
	g, err := s.Closure(ctx, name, version)
	if err != nil {
		return nil, err
	}
	out := &api.IngestClosureResult{}
	for _, n := range g.Nodes {
		if !n.External {
			continue
		}
		_, berr := s.Bump(ctx, api.BumpOptions{
			Module:  n.Name,
			Version: n.Version,
			Source:  "closure-ingest",
		})
		if berr != nil {
			out.Failed++
			out.Errors = append(out.Errors, api.IngestClosureError{
				Module:  n.Name,
				Version: n.Version,
				Error:   berr.Error(),
			})
			continue
		}
		out.Bumped++
	}
	return out, nil
}
