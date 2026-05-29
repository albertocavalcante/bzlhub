package store_test

import (
	"context"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/store"
)

// TestHasSourceIndex_DefaultsFalse verifies the additive migration's
// default kicks in: a freshly-inserted versions row reports false
// until the ingest path (or backfill) explicitly flips it.
func TestHasSourceIndex_DefaultsFalse(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "fresh", Version: "1"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	has, err := s.GetHasSourceIndex(ctx, "fresh", "1")
	if err != nil {
		t.Fatalf("GetHasSourceIndex: %v", err)
	}
	if has {
		t.Error("freshly-inserted row should have has_source_index = false by default")
	}
}

// TestHasSourceIndex_RoundTrip covers the basic write+read contract.
func TestHasSourceIndex_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := s.SetHasSourceIndex(ctx, "m", "1", true); err != nil {
		t.Fatalf("SetHasSourceIndex(true): %v", err)
	}
	got, err := s.GetHasSourceIndex(ctx, "m", "1")
	if err != nil || !got {
		t.Errorf("after Set(true): got=%v err=%v want=true,nil", got, err)
	}

	// Flipping back to false must persist.
	if err := s.SetHasSourceIndex(ctx, "m", "1", false); err != nil {
		t.Fatalf("SetHasSourceIndex(false): %v", err)
	}
	got, _ = s.GetHasSourceIndex(ctx, "m", "1")
	if got {
		t.Error("after Set(false): got true, want false")
	}
}

// TestHasSourceIndex_MissingRowReturnsFalse: GetHasSourceIndex on a
// nonexistent (module, version) returns false + nil error (caller
// treats it the same as "no SCIP available").
func TestHasSourceIndex_MissingRowReturnsFalse(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	has, err := s.GetHasSourceIndex(ctx, "nope", "0")
	if err != nil {
		t.Errorf("missing row should not error, got %v", err)
	}
	if has {
		t.Error("missing row should report false")
	}
}

// TestHasSourceIndex_SetRequiresNonEmptyKey guards the cheap
// caller-violation check.
func TestHasSourceIndex_SetRequiresNonEmptyKey(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.SetHasSourceIndex(ctx, "", "1", true); err == nil {
		t.Error("empty module name should error")
	}
	if err := s.SetHasSourceIndex(ctx, "m", "", true); err == nil {
		t.Error("empty version should error")
	}
}

// TestSearch_ProjectsHasSourceIndex covers the SQL projection: the
// search hit carries the cached flag, picked up from versions via
// the LEFT JOIN.
func TestSearch_ProjectsHasSourceIndex(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := &report.ModuleReport{
		Name:    "rules_x",
		Version: "1.0.0",
		Rules: []report.RuleSpec{
			{Name: "x_library", Provenance: report.Provenance{File: "defs.bzl", StartRow: 1}},
		},
	}
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	// Default: flag is false → hit reflects false.
	res, err := s.Search(ctx, api.Query{Text: "x_library"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	if res.Hits[0].HasSourceIndex {
		t.Error("hit should reflect default false flag")
	}

	// Flip flag → re-search → flag mirrors.
	if err := s.SetHasSourceIndex(ctx, "rules_x", "1.0.0", true); err != nil {
		t.Fatalf("SetHasSourceIndex: %v", err)
	}
	res, _ = s.Search(ctx, api.Query{Text: "x_library"})
	if !res.Hits[0].HasSourceIndex {
		t.Error("hit should reflect updated true flag")
	}
}
