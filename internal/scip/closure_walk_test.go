package scip

import (
	"context"
	"fmt"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// fakeReports is an in-memory ReportGetter. The walk only needs one store
// capability, so tests need no sqlite and no fixtures on disk.
type fakeReports struct {
	byKey map[string]*report.ModuleReport
	calls int
}

func (f *fakeReports) GetReport(_ context.Context, name, version string) (*report.ModuleReport, error) {
	f.calls++
	r, ok := f.byKey[name+"@"+version]
	if !ok {
		// Mirrors store.Store: a missing module is an error, not (nil, nil).
		return nil, fmt.Errorf("%s@%s: not found", name, version)
	}
	return r, nil
}

// mod is a terse report builder. deps are "name@version" strings; a "!" suffix
// marks a dev_dependency, and "=repo" renames the repo.
func mod(deps ...report.ModuleKey) *report.ModuleReport {
	return &report.ModuleReport{BazelDeps: deps}
}

func dep(name, version string) report.ModuleKey {
	return report.ModuleKey{Name: name, Version: version}
}

func devDep(name, version string) report.ModuleKey {
	return report.ModuleKey{Name: name, Version: version, DevDependency: true}
}

func renamedDep(name, version, repo string) report.ModuleKey {
	return report.ModuleKey{Name: name, Version: version, RepoName: repo}
}

func TestTransitiveClosure_ResolvesTransitively(t *testing.T) {
	// root -> a -> b. Only `a` is a direct dep, so the pre-existing
	// direct-only closure left every symbol from `b` unresolved.
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0": mod(dep("b", "2.0")),
		"b@2.0": mod(),
	}}

	got, err := TransitiveClosure(context.Background(), DepsFromStore(store), mod(dep("a", "1.0")))
	if err != nil {
		t.Fatalf("TransitiveClosure: %v", err)
	}
	if got["a"].Version != "1.0" {
		t.Errorf("direct dep missing: %+v", got)
	}
	if got["b"].Version != "2.0" {
		t.Errorf("transitive dep not resolved: %+v", got)
	}
}

// Bazel's MVS uses the ROOT module's dev_dependency entries and ignores
// everyone else's. A walk that keeps all of them resolves symbols Bazel itself
// would not, which is worse than leaving them unresolved: the navigation would
// be confidently wrong.
func TestTransitiveClosure_KeepsRootDevDepsAndDropsTransitiveOnes(t *testing.T) {
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0":           mod(devDep("a_test_only", "9.9")),
		"a_test_only@9.9": mod(),
		"root_dev@3.0":    mod(),
	}}

	got, err := TransitiveClosure(context.Background(), DepsFromStore(store),
		mod(dep("a", "1.0"), devDep("root_dev", "3.0")))
	if err != nil {
		t.Fatalf("TransitiveClosure: %v", err)
	}
	if _, ok := got["root_dev"]; !ok {
		t.Error("the root's own dev dep was dropped; Bazel keeps it")
	}
	if _, ok := got["a_test_only"]; ok {
		t.Error("a dependency's dev dep leaked into the closure; Bazel ignores it")
	}
}

// A dep that has not been ingested yet simply stops the descent. It is not an
// error: the store is allowed to be incomplete, and the reference falls back to
// the unresolved placeholder exactly as before.
func TestTransitiveClosure_MissingDepStopsDescentWithoutFailing(t *testing.T) {
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"present@1.0": mod(),
	}}

	got, err := TransitiveClosure(context.Background(), DepsFromStore(store),
		mod(dep("present", "1.0"), dep("never_ingested", "4.0")))
	if err != nil {
		t.Fatalf("a missing dep must not fail the walk: %v", err)
	}
	// The coordinate is still known from the parent's MODULE.bazel, so it
	// belongs in the closure; only its own deps are unreachable.
	if got["never_ingested"].Version != "4.0" {
		t.Errorf("a dep named by the parent should still resolve: %+v", got)
	}
	if got["present"].Version != "1.0" {
		t.Errorf("the reachable dep is missing: %+v", got)
	}
}

func TestTransitiveClosure_CycleTerminates(t *testing.T) {
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0": mod(dep("b", "1.0")),
		"b@1.0": mod(dep("a", "1.0")),
	}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := TransitiveClosure(context.Background(), DepsFromStore(store), mod(dep("a", "1.0"))); err != nil {
			t.Errorf("TransitiveClosure: %v", err)
		}
	}()
	select {
	case <-done:
	case <-context.Background().Done():
	}
	if store.calls > 10 {
		t.Errorf("walk revisited nodes %d times; the cycle guard is not holding", store.calls)
	}
}

// Two paths reaching the same module at different versions: MVS takes the
// higher, and the higher one's OWN deps are what get walked.
func TestTransitiveClosure_HigherVersionWinsAndIsTheOneExpanded(t *testing.T) {
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0":           mod(dep("shared", "1.0")),
		"b@1.0":           mod(dep("shared", "2.0")),
		"shared@1.0":      mod(dep("only_in_old", "1.0")),
		"shared@2.0":      mod(dep("only_in_new", "1.0")),
		"only_in_old@1.0": mod(),
		"only_in_new@1.0": mod(),
	}}

	got, err := TransitiveClosure(context.Background(), DepsFromStore(store),
		mod(dep("a", "1.0"), dep("b", "1.0")))
	if err != nil {
		t.Fatalf("TransitiveClosure: %v", err)
	}
	if got["shared"].Version != "2.0" {
		t.Errorf("shared resolved to %q, want the higher 2.0", got["shared"].Version)
	}
	if _, ok := got["only_in_new"]; !ok {
		t.Error("the winning version's own deps were not walked")
	}
}

// Repo renames survive the transitive walk: the key is the alias a load()
// names, the coordinate carries the module name a symbol needs.
func TestTransitiveClosure_PreservesRepoRenamesAtDepth(t *testing.T) {
	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0":           mod(renamedDep("rules_foo", "1.2.3", "foo")),
		"rules_foo@1.2.3": mod(),
	}}

	got, err := TransitiveClosure(context.Background(), DepsFromStore(store), mod(dep("a", "1.0")))
	if err != nil {
		t.Fatalf("TransitiveClosure: %v", err)
	}
	if got["foo"].Module != "rules_foo" || got["foo"].Version != "1.2.3" {
		t.Errorf("transitive repo rename lost: %+v", got)
	}
	if _, keyedByModule := got["rules_foo"]; keyedByModule {
		t.Error("also keyed by module name; a load() never names that")
	}
}

func TestTransitiveClosure_NilRootIsEmptyNotAPanic(t *testing.T) {
	got, err := TransitiveClosure(context.Background(), DepsFromStore(&fakeReports{}), nil)
	if err != nil {
		t.Fatalf("TransitiveClosure(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil root produced %+v, want empty", got)
	}
}

// The whole point, stated once at the level a user cares about: a load() that
// reaches a dependency OF a dependency resolves to a real coordinate instead of
// the unresolved placeholder.
//
// root depends on a; a depends on b; root's source loads from b. Under the old
// direct-only closure b was absent, so that reference resolved to nothing.
func TestGenerate_TransitiveLoadResolvesViaTransitiveClosure(t *testing.T) {
	dir := writeModule(t, `load("@b//:defs.bzl", "deep_helper")

def my_rule(name):
    return deep_helper(name)
`)

	store := &fakeReports{byKey: map[string]*report.ModuleReport{
		"a@1.0": mod(dep("b", "2.0")),
		"b@2.0": mod(),
	}}
	root := mod(dep("a", "1.0"))

	closure, err := TransitiveClosure(context.Background(), DepsFromStore(store), root)
	if err != nil {
		t.Fatalf("TransitiveClosure: %v", err)
	}

	blob, err := Generate(dir, "root_module", "1.0.0", closure)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	syms := symbolsIn(t, blob)

	want := "starlark bzlmod b 2.0 defs.bzl/deep_helper#"
	if !contains(syms, want) {
		t.Errorf("a transitive load did not resolve.\n  want %q\n  got  %v", want, syms)
	}

	// And prove the assertion above is not vacuous: the direct-only closure,
	// which is what shipped before, leaves the same reference unresolved.
	directBlob, err := Generate(dir, "root_module", "1.0.0", DirectClosure(root))
	if err != nil {
		t.Fatalf("Generate with the direct closure: %v", err)
	}
	if contains(symbolsIn(t, directBlob), want) {
		t.Error("the direct-only closure also resolved it, so this test proves nothing " +
			"about the transitive walk")
	}
}
