package compat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// fakeSource is an in-memory ReportSource used to drive Analyze
// through known-shape diffs without standing up a real store.
type fakeSource struct {
	latest  map[string]string
	reports map[string]*report.ModuleReport // key: name@version
}

func (f *fakeSource) LatestVersion(_ context.Context, name string) (string, error) {
	return f.latest[name], nil
}
func (f *fakeSource) GetReport(_ context.Context, name, version string) (*report.ModuleReport, error) {
	return f.reports[name+"@"+version], nil
}

func TestAnalyze_EmptyInputReturnsErr(t *testing.T) {
	src := &fakeSource{}
	_, err := Analyze(context.Background(), src, `module(name = "x", version = "1.0")`, Options{})
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("want ErrEmptyInput, got %v", err)
	}
}

func TestAnalyze_NilSourceErrors(t *testing.T) {
	_, err := Analyze(context.Background(), nil, `bazel_dep(name="x", version="1")`, Options{})
	if err == nil {
		t.Fatal("expected error for nil source")
	}
}

func TestAnalyze_MissingFromCorpus(t *testing.T) {
	src := &fakeSource{}
	body := `module(name = "x", version = "1")
bazel_dep(name = "rules_go", version = "0.40.0")`
	r, err := Analyze(context.Background(), src, body, Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(r.Deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(r.Deps))
	}
	if r.Deps[0].InCorpus {
		t.Error("expected InCorpus=false")
	}
	if r.Summary.MissingFromCorpus != 1 {
		t.Errorf("MissingFromCorpus = %d, want 1", r.Summary.MissingFromCorpus)
	}
	if !strings.Contains(r.PlanMarkdown, "Not in canopy index") {
		t.Error("plan should call out missing modules")
	}
}

func TestAnalyze_AlreadyLatest(t *testing.T) {
	src := &fakeSource{
		latest: map[string]string{"rules_go": "0.40.0"},
		reports: map[string]*report.ModuleReport{
			"rules_go@0.40.0": {Name: "rules_go", Version: "0.40.0"},
		},
	}
	body := `module(name = "x", version = "1")
bazel_dep(name = "rules_go", version = "0.40.0")`
	r, err := Analyze(context.Background(), src, body, Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.Summary.AlreadyLatest != 1 {
		t.Errorf("AlreadyLatest = %d, want 1", r.Summary.AlreadyLatest)
	}
	if !r.Deps[0].SameVersion {
		t.Error("expected SameVersion=true")
	}
	if r.Deps[0].BreakingCount != 0 {
		t.Error("expected zero breaking")
	}
}

func TestAnalyze_BreakingBump(t *testing.T) {
	fromR := &report.ModuleReport{
		Name:    "rules_go",
		Version: "0.40.0",
		Rules: []report.RuleSpec{
			{Name: "go_binary"},
			{Name: "go_test"}, // removed in to
		},
	}
	toR := &report.ModuleReport{
		Name:    "rules_go",
		Version: "0.50.0",
		Rules: []report.RuleSpec{
			{Name: "go_binary"},
		},
	}
	src := &fakeSource{
		latest: map[string]string{"rules_go": "0.50.0"},
		reports: map[string]*report.ModuleReport{
			"rules_go@0.40.0": fromR,
			"rules_go@0.50.0": toR,
		},
	}
	body := `module(name = "x", version = "1")
bazel_dep(name = "rules_go", version = "0.40.0")`
	r, err := Analyze(context.Background(), src, body, Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.Summary.BreakingDeps != 1 {
		t.Fatalf("BreakingDeps = %d, want 1", r.Summary.BreakingDeps)
	}
	if r.Deps[0].BreakingCount == 0 {
		t.Fatal("expected nonzero breaking count")
	}
	if !strings.Contains(r.PlanMarkdown, "0.40.0") || !strings.Contains(r.PlanMarkdown, "0.50.0") {
		t.Errorf("plan missing version transition:\n%s", r.PlanMarkdown)
	}
}

func TestAnalyze_SortsByBreakingCountDesc(t *testing.T) {
	bigBreak := &report.ModuleReport{
		Name:    "big",
		Version: "1.0",
		Rules: []report.RuleSpec{
			{Name: "r1"}, {Name: "r2"}, {Name: "r3"},
		},
	}
	bigBreakTo := &report.ModuleReport{Name: "big", Version: "2.0"}

	smallBreak := &report.ModuleReport{
		Name:    "small",
		Version: "1.0",
		Rules:   []report.RuleSpec{{Name: "r1"}},
	}
	smallBreakTo := &report.ModuleReport{Name: "small", Version: "2.0"}

	src := &fakeSource{
		latest: map[string]string{"big": "2.0", "small": "2.0"},
		reports: map[string]*report.ModuleReport{
			"big@1.0":   bigBreak,
			"big@2.0":   bigBreakTo,
			"small@1.0": smallBreak,
			"small@2.0": smallBreakTo,
		},
	}
	body := `module(name = "x", version = "1")
bazel_dep(name = "small", version = "1.0")
bazel_dep(name = "big",   version = "1.0")
`
	r, err := Analyze(context.Background(), src, body, Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.Deps[0].Name != "big" {
		t.Errorf("expected big first (more breaking), got %q", r.Deps[0].Name)
	}
}

func TestAnalyze_DevDependencyDefaultsHidden(t *testing.T) {
	src := &fakeSource{}
	body := `module(name = "x", version = "1")
bazel_dep(name = "rules_go", version = "0.40.0", dev_dependency = True)
bazel_dep(name = "prod_dep", version = "1.0.0")
`
	r, err := Analyze(context.Background(), src, body, Options{})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(r.Deps) != 1 || r.Deps[0].Name != "prod_dep" {
		t.Errorf("expected prod_dep only, got %+v", r.Deps)
	}
	// And with the flag, both surface.
	r2, err := Analyze(context.Background(), src, body, Options{IncludeDevDependencies: true})
	if err != nil {
		t.Fatalf("Analyze (with dev): %v", err)
	}
	if len(r2.Deps) != 2 {
		t.Errorf("expected 2 deps with dev flag, got %d", len(r2.Deps))
	}
}
