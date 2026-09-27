package moduledeps_test

import (
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/moduledeps"
)

// FromModuleBazel is deliberately lossless: it reports every bazel_dep exactly
// as declared, including repo_name and dev_dependency, and filters nothing.
//
// Callers filter, because they disagree about what to keep. The recursive
// ingest walk wants non-dev deps only (it is deciding what to fetch). The SCIP
// closure wants the root's dev deps but not a dependency's, and it needs
// repo_name because a load() names the repo. A parser that pre-filtered could
// not serve both, which is how a second parser gets written.
func TestFromModuleBazel_IsLossless(t *testing.T) {
	got, err := moduledeps.FromModuleBazel([]byte(`
module(name = "root", version = "1.0")
bazel_dep(name = "plain", version = "1.0")
bazel_dep(name = "rules_foo", version = "2.0", repo_name = "foo")
bazel_dep(name = "tooling", version = "3.0", dev_dependency = True)
`))
	if err != nil {
		t.Fatalf("FromModuleBazel: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d deps, want all 3 unfiltered: %+v", len(got), got)
	}

	byName := map[string]int{}
	for i, d := range got {
		byName[d.Name] = i
	}

	if d := got[byName["rules_foo"]]; d.RepoName != "foo" {
		t.Errorf("repo_name lost: %+v", d)
	}
	if d := got[byName["tooling"]]; !d.DevDependency {
		t.Errorf("dev_dependency lost: %+v", d)
	}
	if d := got[byName["plain"]]; d.RepoName != "" || d.DevDependency {
		t.Errorf("plain dep gained attributes it does not have: %+v", d)
	}
	if d := got[byName["plain"]]; d.Version != "1.0" {
		t.Errorf("version wrong: %+v", d)
	}
}

// Source order is preserved. Nothing depends on it today, but a parser that
// reorders silently makes any future order-sensitive caller wrong, and
// preserving it costs nothing.
func TestFromModuleBazel_PreservesSourceOrder(t *testing.T) {
	// The module() declaration is required -- gobzlmod rejects content without
	// one, which is correct: every MODULE.bazel in a registry has it.
	got, err := moduledeps.FromModuleBazel([]byte(`
module(name = "root", version = "1.0")
bazel_dep(name = "c", version = "1")
bazel_dep(name = "a", version = "1")
bazel_dep(name = "b", version = "1")
`))
	if err != nil {
		t.Fatalf("FromModuleBazel: %v", err)
	}
	want := []string{"c", "a", "b"}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("dep %d = %q, want %q (source order)", i, got[i].Name, name)
		}
	}
}

func TestFromModuleBazel_NoDepsIsEmptyNotAnError(t *testing.T) {
	got, err := moduledeps.FromModuleBazel([]byte(`module(name = "solo", version = "1.0")`))
	if err != nil {
		t.Fatalf("a module with no deps is valid: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestFromModuleBazel_RejectsUnparseableContent(t *testing.T) {
	if _, err := moduledeps.FromModuleBazel([]byte("bazel_dep(name =")); err == nil {
		t.Error("malformed MODULE.bazel was accepted; a silent empty dep list " +
			"reads as 'this module has no dependencies'")
	}
}
