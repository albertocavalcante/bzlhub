package canopy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/store"
)

func TestServiceClosure_MarksMissingDepsExternalAndSkipsStubDeps(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{
		Name:    "root",
		Version: "1.0.0",
		BazelDeps: []report.ModuleKey{
			{Name: "mid", Version: "1.0.0"},
			{Name: "missing", Version: "9.9.9"},
			{Name: "stub", Version: "0"},
			{Name: "empty"},
		},
	})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{
		Name:    "mid",
		Version: "1.0.0",
		BazelDeps: []report.ModuleKey{
			{Name: "leaf", Version: "1.0.0"},
		},
	})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "leaf", Version: "1.0.0"})

	got, err := svc.Closure(ctx, "root", "1.0.0")
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}

	nodes := map[string]bool{}
	for _, n := range got.Nodes {
		nodes[nodeKey(n.Name, n.Version)] = n.External
	}
	wantNodes := map[string]bool{
		"root@1.0.0":    false,
		"mid@1.0.0":     false,
		"leaf@1.0.0":    false,
		"missing@9.9.9": true,
	}
	if len(nodes) != len(wantNodes) {
		t.Fatalf("nodes = %#v, want %#v", nodes, wantNodes)
	}
	for key, wantExternal := range wantNodes {
		if gotExternal, ok := nodes[key]; !ok || gotExternal != wantExternal {
			t.Fatalf("node %s external = %v, present = %v; want external %v", key, gotExternal, ok, wantExternal)
		}
	}
	if _, ok := nodes["stub@0"]; ok {
		t.Fatalf("stub dep should not appear in closure: %#v", nodes)
	}

	edges := map[string]bool{}
	for _, e := range got.Edges {
		edges[e.From+"->"+e.To] = true
	}
	for _, want := range []string{
		"root@1.0.0->mid@1.0.0",
		"root@1.0.0->missing@9.9.9",
		"mid@1.0.0->leaf@1.0.0",
	} {
		if !edges[want] {
			t.Fatalf("missing edge %s in %#v", want, edges)
		}
	}
}

func TestServiceReverseDeps_DeduplicatesConsumers(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "target", Version: "1.0.0"})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{
		Name:    "consumer",
		Version: "1.0.0",
		BazelDeps: []report.ModuleKey{
			{Name: "target", Version: "1.0.0"},
			{Name: "target", Version: "1.0.0"},
		},
	})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{
		Name:      "other",
		Version:   "2.0.0",
		BazelDeps: []report.ModuleKey{{Name: "target", Version: "1.0.0"}},
	})

	got, err := svc.ReverseDeps(ctx, "target", "1.0.0")
	if err != nil {
		t.Fatalf("ReverseDeps: %v", err)
	}
	if got.Module != "target" || got.Version != "1.0.0" {
		t.Fatalf("coordinate = %s@%s, want target@1.0.0", got.Module, got.Version)
	}
	if len(got.Deps) != 2 {
		t.Fatalf("deps = %#v, want two unique consumers", got.Deps)
	}
	seen := map[string]bool{}
	for _, dep := range got.Deps {
		key := dep.Name + "@" + dep.Version
		if seen[key] {
			t.Fatalf("duplicate reverse dep %s in %#v", key, got.Deps)
		}
		seen[key] = true
	}
	for _, want := range []string{"consumer@1.0.0", "other@2.0.0"} {
		if !seen[want] {
			t.Fatalf("missing reverse dep %s in %#v", want, got.Deps)
		}
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "canopy.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func writeServiceReport(t *testing.T, ctx context.Context, svc *Service, r *report.ModuleReport) {
	t.Helper()
	if err := svc.store.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport(%s@%s): %v", r.Name, r.Version, err)
	}
}
