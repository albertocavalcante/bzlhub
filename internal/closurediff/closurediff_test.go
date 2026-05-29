package closurediff

import (
	"context"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// TestShapeClosureDepsSurfacesAdded covers a closure that gained a dep.
func TestShapeClosureDepsSurfacesAdded(t *testing.T) {
	from := map[string]string{"root": "1.0.0", "a": "1.0.0"}
	to := map[string]string{"root": "2.0.0", "a": "1.0.0", "newcomer": "0.1.0"}
	d := shapeClosureDeps(from, to)
	if len(d.Added) != 1 || d.Added[0].Name != "newcomer" {
		t.Errorf("expected single added 'newcomer', got %+v", d.Added)
	}
	if len(d.Removed) != 0 {
		t.Errorf("expected no removed, got %+v", d.Removed)
	}
	if len(d.Changed) != 1 || d.Changed[0].Name != "root" {
		t.Fatalf("expected root in changed, got %+v", d.Changed)
	}
}

// TestShapeClosureDepsSurfacesRemoved covers a closure that dropped a dep.
func TestShapeClosureDepsSurfacesRemoved(t *testing.T) {
	from := map[string]string{"root": "1.0.0", "deprecated": "0.9.0", "kept": "1.0.0"}
	to := map[string]string{"root": "2.0.0", "kept": "1.0.0"}
	d := shapeClosureDeps(from, to)
	if len(d.Removed) != 1 || d.Removed[0].Name != "deprecated" {
		t.Errorf("expected single removed 'deprecated', got %+v", d.Removed)
	}
	if len(d.Added) != 0 {
		t.Errorf("expected no added, got %+v", d.Added)
	}
	if len(d.Changed) != 1 || d.Changed[0].Name != "root" {
		t.Errorf("expected root in changed, got %+v", d.Changed)
	}
}

// TestShapeClosureDepsSurfacesVersionChange covers an MVS-driven version bump.
func TestShapeClosureDepsSurfacesVersionChange(t *testing.T) {
	from := map[string]string{"root": "1.0.0", "a": "1.0.0"}
	to := map[string]string{"root": "1.1.0", "a": "2.0.0"} // a got bumped by MVS
	d := shapeClosureDeps(from, to)
	if len(d.Changed) != 2 {
		t.Fatalf("expected 2 changed (root + a), got %+v", d.Changed)
	}
	byName := map[string]ChangedClosureDep{}
	for _, c := range d.Changed {
		byName[c.Name] = c
	}
	if byName["a"].FromVersion != "1.0.0" || byName["a"].ToVersion != "2.0.0" {
		t.Errorf("expected a 1.0.0→2.0.0, got %+v", byName["a"])
	}
}

// TestShapeClosureDepsIdenticalIsEmpty: same closure both sides → no changes
// except the root coords (but with from==to we treat as empty here).
func TestShapeClosureDepsIdenticalSet(t *testing.T) {
	c := map[string]string{"root": "1.0.0", "a": "1.0.0", "b": "2.3.4"}
	d := shapeClosureDeps(c, c)
	if len(d.Added)+len(d.Removed)+len(d.Changed) != 0 {
		t.Errorf("expected empty diff for identical closures, got %+v", d)
	}
}

// TestComputeRollsUpBreakingFindings runs Compute end-to-end using a
// fake AnalyzeFunc + a fake walkClosure-equivalent (we exercise the
// post-walk plumbing by injecting an Options whose AnalyzeFunc returns
// crafted ModuleReports with known shapes that produce known breaking
// counts).
//
// To keep the test from needing a real upstream, we test the rollup
// logic by calling shapeClosureDeps + perModuleDiff manually below.
func TestPerModuleDiffPropagatesBreaking(t *testing.T) {
	// Set up a fake analyzer keyed on (name, version).
	fakes := map[string]*report.ModuleReport{
		"root@1.0.0": {
			Name: "root", Version: "1.0.0", CompatibilityLevel: 1,
			Rules: []report.RuleSpec{{Name: "kept"}, {Name: "doomed"}},
		},
		"root@2.0.0": {
			Name: "root", Version: "2.0.0", CompatibilityLevel: 2, // shift
			Rules: []report.RuleSpec{{Name: "kept"}}, // doomed removed
		},
	}
	analyze := func(_ context.Context, name, version string) (*report.ModuleReport, error) {
		return fakes[name+"@"+version], nil
	}
	d, err := perModuleDiff(context.Background(), "root", "1.0.0", "2.0.0", analyze)
	if err != nil {
		t.Fatalf("perModuleDiff: %v", err)
	}
	// We expect at least the compat shift + a rule_removed.
	gotKinds := map[string]bool{}
	for _, f := range d.Breaking {
		gotKinds[string(f.Kind)] = true
	}
	if !gotKinds["compat_level_shift"] {
		t.Errorf("expected compat_level_shift in breaking, got %v", gotKinds)
	}
	if !gotKinds["rule_removed"] {
		t.Errorf("expected rule_removed in breaking, got %v", gotKinds)
	}
}
