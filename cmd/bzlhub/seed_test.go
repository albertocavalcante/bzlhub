package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

func TestSeed_InsertsCanonicalModules(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, err := seedRequests(context.Background(), s, defaultSeedSet, "seed-bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Inserted != len(defaultSeedSet) {
		t.Errorf("inserted = %d, want %d", got.Inserted, len(defaultSeedSet))
	}
	if got.Skipped != 0 {
		t.Errorf("skipped = %d on first run, want 0", got.Skipped)
	}

	// Every inserted row should be in state=pending.
	rows, _ := s.ListRequests(context.Background(), store.RequestQuery{
		States: []store.RequestState{store.RequestStatePending},
		Limit:  100,
	})
	if len(rows) != len(defaultSeedSet) {
		t.Errorf("pending rows = %d, want %d", len(rows), len(defaultSeedSet))
	}
}

func TestSeed_Idempotent(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, _ = seedRequests(context.Background(), s, defaultSeedSet, "seed-bot@example.com")
	got, err := seedRequests(context.Background(), s, defaultSeedSet, "seed-bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Inserted != 0 {
		t.Errorf("second-run inserted = %d, want 0", got.Inserted)
	}
	if got.Skipped != len(defaultSeedSet) {
		t.Errorf("second-run skipped = %d, want %d", got.Skipped, len(defaultSeedSet))
	}
}

func TestSeed_SkipsAfterDenial(t *testing.T) {
	// A previously-denied (m, v) doesn't get re-submitted. Operators
	// who actually want to retry should remove the denial row first.
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	subset := []seedEntry{{Module: "rules_python", Version: "1.5.0"}}
	id, _ := s.CreateRequest(ctx, store.Request{
		SubmitterSub: "x", AuthMethod: "test",
		Module: "rules_python", Version: "1.5.0",
	})
	_ = s.TransitionRequest(ctx, id, store.RequestStatePending, store.RequestStatePreflighting, nil)
	_ = s.TransitionRequest(ctx, id, store.RequestStatePreflighting, store.RequestStateDenied, &store.RequestFields{DenialReason: "x"})

	got, _ := seedRequests(ctx, s, subset, "seed-bot@example.com")
	if got.Inserted != 0 {
		t.Errorf("inserted after denial = %d, want 0", got.Inserted)
	}
	if got.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", got.Skipped)
	}
}

func TestSeed_ParseEntry(t *testing.T) {
	cases := map[string]seedEntry{
		"rules_go@0.50.0":         {Module: "rules_go", Version: "0.50.0"},
		"bazel_skylib@1.7.1":      {Module: "bazel_skylib", Version: "1.7.1"},
		"  rules_python@1.5.0  ":  {Module: "rules_python", Version: "1.5.0"},
	}
	for input, want := range cases {
		got, err := parseSeedEntry(input)
		if err != nil {
			t.Errorf("parseSeedEntry(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("parseSeedEntry(%q) = %+v, want %+v", input, got, want)
		}
	}
}

func TestSeed_ParseEntry_Invalid(t *testing.T) {
	bad := []string{"", "no-at-sign", "@noversion", "module@", "a@b@c"}
	for _, s := range bad {
		if _, err := parseSeedEntry(s); err == nil {
			t.Errorf("parseSeedEntry(%q) expected error", s)
		}
	}
}

// =================================================================
// --auto-approve: Plan 76 §2.7 demo populate
// =================================================================

func TestSeed_AutoApprove_PopulatesVersionsTable(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, err := seedRequestsWithOptions(context.Background(), s, defaultSeedSet,
		seedOptions{Submitter: "seed-bot@example.com", AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Inserted != len(defaultSeedSet) {
		t.Errorf("Inserted=%d, want %d", got.Inserted, len(defaultSeedSet))
	}
	if got.IndexedDirectly != len(defaultSeedSet) {
		t.Errorf("IndexedDirectly=%d, want %d", got.IndexedDirectly, len(defaultSeedSet))
	}

	rows, err := s.ListAllVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(defaultSeedSet) {
		t.Errorf("versions rows=%d, want %d (populated for /modules)", len(rows), len(defaultSeedSet))
	}
}

func TestSeed_AutoApprove_IdempotentOnVersions(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	opts := seedOptions{Submitter: "seed-bot@example.com", AutoApprove: true}
	_, _ = seedRequestsWithOptions(context.Background(), s, defaultSeedSet, opts)
	got, err := seedRequestsWithOptions(context.Background(), s, defaultSeedSet, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Inserted != 0 {
		t.Errorf("Inserted on re-run=%d, want 0", got.Inserted)
	}
	if got.Skipped != len(defaultSeedSet) {
		t.Errorf("Skipped on re-run=%d, want %d", got.Skipped, len(defaultSeedSet))
	}
	rows, _ := s.ListAllVersions(context.Background())
	if len(rows) != len(defaultSeedSet) {
		t.Errorf("versions rows after re-run=%d, want %d (idempotent)", len(rows), len(defaultSeedSet))
	}
}

func TestSeed_NoAutoApprove_LeavesVersionsEmpty(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	got, _ := seedRequestsWithOptions(context.Background(), s, defaultSeedSet,
		seedOptions{Submitter: "seed-bot@example.com"})
	if got.IndexedDirectly != 0 {
		t.Errorf("IndexedDirectly=%d, want 0 (auto-approve off)", got.IndexedDirectly)
	}
	rows, _ := s.ListAllVersions(context.Background())
	if len(rows) != 0 {
		t.Errorf("versions rows=%d, want 0 (no direct populate without auto-approve)", len(rows))
	}
}

func TestDefaultSeedSet_Shape(t *testing.T) {
	// Pin the set to 12 canonical Bazel rules per Plan 72 §C7.
	if len(defaultSeedSet) != 12 {
		t.Errorf("defaultSeedSet has %d entries, want 12", len(defaultSeedSet))
	}
	for _, e := range defaultSeedSet {
		if e.Module == "" || e.Version == "" {
			t.Errorf("entry missing fields: %+v", e)
		}
	}
}
