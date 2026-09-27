package scip

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"
	"github.com/albertocavalcante/scip-kit/scipio"
)

// writeModule drops a .bzl file that loads a symbol from another repo.
func writeModule(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.bzl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func symbolsIn(t *testing.T, blob []byte) []string {
	t.Helper()
	idx, err := scipio.UnmarshalIndex(blob)
	if err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}
	var out []string
	for _, d := range idx.Documents {
		for _, o := range d.Occurrences {
			out = append(out, o.Symbol)
		}
	}
	for _, e := range idx.ExternalSymbols {
		out = append(out, e.Symbol)
	}
	return out
}

func contains(syms []string, want string) bool {
	for _, s := range syms {
		if s == want {
			return true
		}
	}
	return false
}

// Generate takes the closure as a parameter rather than deriving it from a
// ModuleReport. That is what lets the caller choose between the direct-only
// closure and the transitive one from FromStore -- the choice is visible at the
// call site instead of buried in a helper.
func TestGenerate_UsesTheSuppliedClosure(t *testing.T) {
	dir := writeModule(t, `load("@foo//:defs.bzl", "helper")

def my_rule(name):
    return helper(name)
`)

	blob, err := Generate(dir, "root_module", "1.0.0",
		bzlmod.Closure{"foo": {Module: "rules_foo", Version: "2.0.0"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	syms := symbolsIn(t, blob)
	want := "starlark bzlmod rules_foo 2.0.0 defs.bzl/helper#"
	if !contains(syms, want) {
		t.Errorf("the supplied closure was not used.\n  want %q\n  got  %v", want, syms)
	}
}

// A nil closure is legitimate: a tree with no resolvable dependencies still
// indexes, its cross-module references just stay unresolved.
func TestGenerate_NilClosureStillIndexes(t *testing.T) {
	dir := writeModule(t, "def my_rule(name):\n    return name\n")

	blob, err := Generate(dir, "root_module", "1.0.0", nil)
	if err != nil {
		t.Fatalf("Generate with a nil closure: %v", err)
	}
	syms := symbolsIn(t, blob)
	want := "starlark bzlmod root_module 1.0.0 rules.bzl/my_rule#"
	if !contains(syms, want) {
		t.Errorf("own symbols missing.\n  want %q\n  got  %v", want, syms)
	}
}

func TestGenerate_RequiresCoordinate(t *testing.T) {
	dir := writeModule(t, "def r():\n    pass\n")
	for name, args := range map[string][3]string{
		"no dir":     {"", "m", "1"},
		"no module":  {dir, "", "1"},
		"no version": {dir, "m", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Generate(args[0], args[1], args[2], nil); err == nil {
				t.Errorf("%s was accepted; want an error", name)
			}
		})
	}
}
