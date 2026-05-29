package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/store"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := &report.ModuleReport{
		Name:               "foo",
		Version:            "1.0.0",
		CompatibilityLevel: 1,
		Rules: []report.RuleSpec{
			{Name: "foo_binary", Doc: "build a foo binary", Provenance: report.Provenance{File: "defs.bzl", StartRow: 10}},
			{Name: "foo_test", Doc: "test a foo binary", Provenance: report.Provenance{File: "defs.bzl", StartRow: 30}},
		},
		Providers: []report.ProviderSpec{
			{Name: "FooInfo", Doc: "info from foo rules", Provenance: report.Provenance{File: "defs.bzl", StartRow: 1}},
		},
		Macros: []report.MacroSpec{
			{Name: "foo_macro", Doc: "convenience macro", Provenance: report.Provenance{File: "defs.bzl", StartRow: 50}},
		},
		Hermeticity: report.HermeticityProfile{
			Classes: []report.HermeticityClass{report.PureStarlark},
		},
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	// Search for the rule by name.
	res, err := s.Search(ctx, api.Query{Text: "foo_binary"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("Search foo_binary: no hits")
	}
	found := false
	for _, h := range res.Hits {
		if h.Module == "foo" && h.MatchName == "foo_binary" && h.MatchKind == "rule" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a rule hit for foo_binary; got %v", res.Hits)
	}

	// Snippet rendering should highlight the match.
	snippetSeen := false
	for _, h := range res.Hits {
		if h.Snippet != "" {
			snippetSeen = true
			break
		}
	}
	if !snippetSeen {
		t.Error("expected at least one hit with a snippet")
	}

	// Hermeticity filter: pure-starlark matches.
	res2, err := s.Search(ctx, api.Query{Text: "foo", Hermeticity: []report.HermeticityClass{report.PureStarlark}})
	if err != nil {
		t.Fatalf("Search filtered: %v", err)
	}
	if len(res2.Hits) == 0 {
		t.Error("filtered search: no hits, expected at least one")
	}

	// Hermeticity filter mismatch: requires-system-tools yields nothing.
	res3, err := s.Search(ctx, api.Query{Text: "foo", Hermeticity: []report.HermeticityClass{report.RequiresSystemTools}})
	if err != nil {
		t.Fatalf("Search mismatched filter: %v", err)
	}
	if len(res3.Hits) != 0 {
		t.Errorf("expected zero hits with mismatched filter; got %d", len(res3.Hits))
	}

	// GetReport round-trips.
	got, err := s.GetReport(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got.Name != r.Name || got.Version != r.Version || len(got.Rules) != 2 || len(got.Providers) != 1 {
		t.Errorf("GetReport round-trip mismatch: got %+v", got)
	}

	// ListVersions.
	vs, err := s.ListVersions(ctx, "foo")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(vs) != 1 || vs[0] != "1.0.0" {
		t.Errorf("ListVersions: got %v, want [1.0.0]", vs)
	}
}

func TestDuplicateRuleNamesAllowed(t *testing.T) {
	// rules_cc-style: same rule name in multiple files.
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := &report.ModuleReport{
		Name:    "rules_x",
		Version: "0.1.0",
		Rules: []report.RuleSpec{
			{Name: "cc_toolchain_config", Provenance: report.Provenance{File: "bsd.bzl"}},
			{Name: "cc_toolchain_config", Provenance: report.Provenance{File: "unix.bzl"}},
			{Name: "cc_toolchain_config", Provenance: report.Provenance{File: "windows.bzl"}},
		},
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport with duplicates: %v", err)
	}

	res, err := s.Search(ctx, api.Query{Text: "cc_toolchain_config"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 3 {
		t.Errorf("expected 3 hits (one per file); got %d", len(res.Hits))
	}
}

// TestForeignKeysEnabledOnEveryConnection: the cascade from versions →
// rules/providers in WriteReport is load-bearing for idempotent
// re-ingest. SQLite's `foreign_keys` pragma is per-CONNECTION, not
// per-database, so applying it once during schema migration doesn't
// stick — the pool hands out fresh connections without the pragma set.
//
// The production drift bug: ingesting rules_python twice produced 2x
// rows in fts_meta and 2x rows in rules; search returned duplicate
// hits, the UI's {#each ... as h (h.module+h.version+h.kind+h.name)}
// block hit `each_key_duplicate` and rendered nothing.
//
// This test forces multiple concurrent connections and asserts every
// one has foreign_keys=ON.
func TestForeignKeysEnabledOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "canopy.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Force the pool to allocate many connections by checking 32 conns
	// in parallel. If any one comes up with foreign_keys=OFF, the
	// cascade-delete in WriteReport silently leaves stale child rows.
	const N = 32
	errs := make(chan error, N)
	for i := range N {
		go func(idx int) {
			v, err := store.PragmaForeignKeys(ctx, s)
			if err != nil {
				errs <- err
				return
			}
			if v != 1 {
				errs <- fmt.Errorf("conn %d: foreign_keys=%d, want 1", idx, v)
				return
			}
			errs <- nil
		}(i)
	}
	for range N {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// TestSearch_HitIncludesFile: a Hit must carry the source file path for
// the matched definition so the UI can disambiguate same-named symbols
// (rules_cc-style: `cc_toolchain_config` defined once per platform in
// different .bzl files).
func TestSearch_HitIncludesFile(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := &report.ModuleReport{
		Name:    "rules_x",
		Version: "0.1.0",
		Rules: []report.RuleSpec{
			{Name: "my_rule", Provenance: report.Provenance{File: "defs.bzl", StartRow: 10}},
		},
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	res, err := s.Search(ctx, api.Query{Text: "my_rule"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected 1 hit; got %d", len(res.Hits))
	}
	if got := res.Hits[0].File; got != "defs.bzl" {
		t.Errorf("Hit.File = %q; want %q", got, "defs.bzl")
	}
}

// TestSearch_FileEmpty_ForMacroWithoutFile: macros without provenance
// still round-trip through search; File simply stays empty. The "module"
// FTS row also carries no file context — empty is the contract there.
func TestSearch_FileEmpty_ForMacroWithoutFile(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := &report.ModuleReport{
		Name:    "rules_x",
		Version: "0.1.0",
		Macros: []report.MacroSpec{
			{Name: "bare_macro"},
		},
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	res, err := s.Search(ctx, api.Query{Text: "bare_macro"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected 1 hit; got %d", len(res.Hits))
	}
	if got := res.Hits[0].File; got != "" {
		t.Errorf("Hit.File = %q; want empty", got)
	}
}

// TestWriteReport_Idempotent: re-writing the same module+version must
// REPLACE the prior ingest's rows, not accumulate.
func TestWriteReport_Idempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "canopy.db")
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := &report.ModuleReport{
		Name:    "foo",
		Version: "1.0.0",
		Rules: []report.RuleSpec{
			{Name: "foo_binary", Provenance: report.Provenance{File: "defs.bzl", StartRow: 10}},
		},
		Providers: []report.ProviderSpec{
			{Name: "FooInfo", Provenance: report.Provenance{File: "defs.bzl", StartRow: 1}},
		},
		Macros: []report.MacroSpec{
			{Name: "foo_macro", Provenance: report.Provenance{File: "defs.bzl", StartRow: 20}},
		},
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("first WriteReport: %v", err)
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("second WriteReport (same data): %v", err)
	}

	for _, q := range []string{"foo_binary", "FooInfo", "foo_macro"} {
		res, err := s.Search(ctx, api.Query{Text: q})
		if err != nil {
			t.Fatalf("Search %q: %v", q, err)
		}
		if len(res.Hits) != 1 {
			t.Errorf("Search %q: got %d hits after idempotent re-write; want exactly 1", q, len(res.Hits))
		}
	}
}
