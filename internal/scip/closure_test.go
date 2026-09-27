package scip

import (
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
)

// The closure the resolver consumes is keyed by REPO name, because that is what
// a load() statement names. ModuleKey.RepoName carries the alias when
// bazel_dep sets repo_name; keying by Name instead makes every renamed
// dependency miss.
func TestDirectClosure_KeysByRepoNameWhenSet(t *testing.T) {
	got := DirectClosure(&report.ModuleReport{
		BazelDeps: []report.ModuleKey{
			{Name: "rules_foo", Version: "1.2.3", RepoName: "foo"},
		},
	})

	want := bzlmod.Coordinate{Module: "rules_foo", Version: "1.2.3"}
	if got["foo"] != want {
		t.Errorf("closure[\"foo\"] = %+v, want %+v", got["foo"], want)
	}
	if _, keyedByModule := got["rules_foo"]; keyedByModule {
		t.Error("closure is also keyed by module name; a load() never names that, " +
			"so the extra key can only mask a miss")
	}
}

// No repo_name is the common case: the repo and module names coincide.
func TestDirectClosure_FallsBackToModuleName(t *testing.T) {
	got := DirectClosure(&report.ModuleReport{
		BazelDeps: []report.ModuleKey{{Name: "rules_python", Version: "0.40.0"}},
	})

	want := bzlmod.Coordinate{Module: "rules_python", Version: "0.40.0"}
	if got["rules_python"] != want {
		t.Errorf("closure[\"rules_python\"] = %+v, want %+v", got["rules_python"], want)
	}
}

// An entry with no version cannot produce a conformant symbol, so it must not
// reach the resolver at all. Dropping it there means the reference falls back
// to the unresolved placeholder, which is the honest outcome.
func TestDirectClosure_SkipsIncompleteDeps(t *testing.T) {
	got := DirectClosure(&report.ModuleReport{
		BazelDeps: []report.ModuleKey{
			{Name: "no_version"},
			{Version: "1.0.0"},
			{Name: "ok", Version: "1.0.0"},
		},
	})

	if len(got) != 1 {
		t.Errorf("closure has %d entries, want only the complete one: %+v", len(got), got)
	}
	if got["ok"].Module != "ok" {
		t.Errorf("the complete dep is missing: %+v", got)
	}
}

func TestDirectClosure_NilReportIsEmptyNotAPanic(t *testing.T) {
	if got := DirectClosure(nil); len(got) != 0 {
		t.Errorf("nil report produced %+v, want empty", got)
	}
}
