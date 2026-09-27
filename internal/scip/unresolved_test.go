package scip

import (
	"testing"

	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
)

// An index with unresolved cross-module references is not broken -- it parses,
// it navigates within itself -- but it is INCOMPLETE, and today the only way to
// notice is to read symbols by hand.
//
// The information is already in the index: scip-starlark marks a load() it could
// not place with the "unresolved" package manager. Reading it back needs no new
// plumbing and no change to Generate.
func TestUnresolvedRepos_ReportsWhatCouldNotBePlaced(t *testing.T) {
	dir := writeModule(t, `load("@known//:defs.bzl", "a")
load("@missing_one//:defs.bzl", "b")
load("@missing_two//pkg:defs.bzl", "c")

def r(name):
    return [a(name), b(name), c(name)]
`)

	blob, err := Generate(dir, "root", "1.0",
		bzlmod.Closure{"known": {Module: "rules_known", Version: "1.0"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := UnresolvedRepos(blob)
	if err != nil {
		t.Fatalf("UnresolvedRepos: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d unresolved repos, want 2: %v", len(got), got)
	}
	// Sorted and deduplicated, so the output is stable enough to log or diff.
	if got[0] != "@missing_one//:defs.bzl" || got[1] != "@missing_two//pkg:defs.bzl" {
		t.Errorf("got %v, want the two raw load targets in sorted order", got)
	}
}

// A fully-resolved index reports nothing. Without this the function could return
// every reference and still look right on the test above.
func TestUnresolvedRepos_EmptyWhenEverythingResolved(t *testing.T) {
	dir := writeModule(t, `load("@known//:defs.bzl", "a")

def r(name):
    return a(name)
`)

	blob, err := Generate(dir, "root", "1.0",
		bzlmod.Closure{"known": {Module: "rules_known", Version: "1.0"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	got, err := UnresolvedRepos(blob)
	if err != nil {
		t.Fatalf("UnresolvedRepos: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a fully resolved index reported %v", got)
	}
}

// The same missing dependency loaded from several files is one problem, not
// several. An operator acts per dependency.
func TestUnresolvedRepos_DeduplicatesRepeatedTargets(t *testing.T) {
	dir := writeModule(t, `load("@missing//:defs.bzl", "a")
load("@missing//:defs.bzl", "b")

def r(name):
    return [a(name), b(name)]
`)

	blob, err := Generate(dir, "root", "1.0", nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, err := UnresolvedRepos(blob)
	if err != nil {
		t.Fatalf("UnresolvedRepos: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %v, want one entry for one missing dependency", got)
	}
}

func TestUnresolvedRepos_RejectsGarbage(t *testing.T) {
	if _, err := UnresolvedRepos([]byte("not a scip index")); err == nil {
		t.Error("unparseable bytes were accepted; an empty result would read as " +
			"'this index is fully resolved'")
	}
}
