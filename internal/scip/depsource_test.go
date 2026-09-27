package scip

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// fakeModuleBazel serves MODULE.bazel content, the way a mirror or registry
// backend does.
type fakeModuleBazel struct {
	byKey map[string]string
	calls []string
}

func (f *fakeModuleBazel) GetModuleBazel(_ context.Context, module, version string) (io.ReadCloser, error) {
	f.calls = append(f.calls, module+"@"+version)
	body, ok := f.byKey[module+"@"+version]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func TestDepsFromStore_ReadsBazelDeps(t *testing.T) {
	src := DepsFromStore(&fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0": mod(dep("b", "2.0")),
	}})

	got, err := src(context.Background(), "a", "1.0")
	if err != nil {
		t.Fatalf("DepsFromStore: %v", err)
	}
	if len(got) != 1 || got[0].Name != "b" {
		t.Errorf("got %+v, want one dep on b", got)
	}
}

func TestDepsFromModuleBazel_ParsesRepoNameAndDevDeps(t *testing.T) {
	src := DepsFromModuleBazel(&fakeModuleBazel{byKey: map[string]string{
		"a@1.0": `
module(name = "a", version = "1.0")
bazel_dep(name = "rules_foo", version = "2.0", repo_name = "foo")
bazel_dep(name = "tooling", version = "3.0", dev_dependency = True)
`,
	}})

	got, err := src(context.Background(), "a", "1.0")
	if err != nil {
		t.Fatalf("DepsFromModuleBazel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d deps, want 2 (filtering is the walk's job): %+v", len(got), got)
	}
	var sawRename, sawDev bool
	for _, d := range got {
		sawRename = sawRename || d.RepoName == "foo"
		sawDev = sawDev || d.DevDependency
	}
	if !sawRename || !sawDev {
		t.Errorf("repo_name or dev_dependency lost through the source: %+v", got)
	}
}

// FirstOf is what makes ingest order stop mattering: the store answers cheaply
// for what is already ingested, and the registry-shaped source answers
// authoritatively for what is not.
func TestFirstOf_FallsThroughToTheNextSource(t *testing.T) {
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"ingested@1.0": mod(dep("from_store", "1.0")),
	}}
	backend := &fakeModuleBazel{byKey: map[string]string{
		"not_ingested@1.0": `
module(name = "not_ingested", version = "1.0")
bazel_dep(name = "from_backend", version = "1.0")
`,
	}}
	src := FirstOf(DepsFromStore(store), DepsFromModuleBazel(backend))

	got, err := src(context.Background(), "ingested", "1.0")
	if err != nil || len(got) != 1 || got[0].Name != "from_store" {
		t.Errorf("store hit: got %+v err=%v", got, err)
	}
	if len(backend.calls) != 0 {
		t.Errorf("the backend was consulted for a module the store had: %v", backend.calls)
	}

	got, err = src(context.Background(), "not_ingested", "1.0")
	if err != nil || len(got) != 1 || got[0].Name != "from_backend" {
		t.Errorf("fallthrough: got %+v err=%v", got, err)
	}
}

// When nothing can answer, the last error surfaces. The walk turns that into
// "stop descending"; it must not look like "this module has no dependencies".
func TestFirstOf_ErrorsWhenNoSourceCanAnswer(t *testing.T) {
	src := FirstOf(
		DepsFromStore(&fakeReports{}),
		DepsFromModuleBazel(&fakeModuleBazel{}),
	)
	if _, err := src(context.Background(), "nowhere", "1.0"); err == nil {
		t.Error("all sources failed but FirstOf reported success")
	}
}

func TestFirstOf_NoSourcesIsAnError(t *testing.T) {
	if _, err := FirstOf()(context.Background(), "x", "1"); err == nil {
		t.Error("FirstOf() with no sources reported success; it can answer nothing")
	}
}

// The property this whole change exists for: the closure must not depend on
// what has been ingested.
//
// Same dependency graph, two stores -- one with everything ingested, one empty.
// With an authoritative source chained behind the store, both produce the same
// closure. That is ingest order becoming irrelevant, stated as an assertion
// rather than as a doc comment.
func TestTransitiveClosure_IsIndependentOfWhatHasBeenIngested(t *testing.T) {
	// root -> a -> b, declared identically in both worlds.
	moduleBazel := map[string]string{
		"a@1.0": `
module(name = "a", version = "1.0")
bazel_dep(name = "b", version = "2.0")
`,
		"b@2.0": `module(name = "b", version = "2.0")`,
	}
	root := mod(dep("a", "1.0"))

	fullyIngested := FirstOf(
		DepsFromStore(&fakeReports{byKey: map[string]*report.ModuleReport{
			"a@1.0": mod(dep("b", "2.0")),
			"b@2.0": mod(),
		}}),
		DepsFromModuleBazel(&fakeModuleBazel{byKey: moduleBazel}),
	)
	// Nothing ingested at all: every answer has to come from the registry-shaped
	// source. This is the case that used to resolve nothing transitively.
	nothingIngested := FirstOf(
		DepsFromStore(&fakeReports{}),
		DepsFromModuleBazel(&fakeModuleBazel{byKey: moduleBazel}),
	)

	withStore, err := TransitiveClosure(context.Background(), fullyIngested, root)
	if err != nil {
		t.Fatalf("fully ingested: %v", err)
	}
	withoutStore, err := TransitiveClosure(context.Background(), nothingIngested, root)
	if err != nil {
		t.Fatalf("nothing ingested: %v", err)
	}

	if len(withStore) != len(withoutStore) {
		t.Fatalf("closure size depends on ingest state: %d vs %d\n  ingested: %+v\n  empty:    %+v",
			len(withStore), len(withoutStore), withStore, withoutStore)
	}
	for repo, coord := range withStore {
		if withoutStore[repo] != coord {
			t.Errorf("%q differs by ingest state: %+v vs %+v", repo, coord, withoutStore[repo])
		}
	}
	if withoutStore["b"].Version != "2.0" {
		t.Errorf("the transitive dep did not resolve from the authoritative source: %+v", withoutStore)
	}
}
