package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestAllVersionsWithDrift_HappyPath asserts the bundled query
// returns drift_summary_json alongside the row in one trip.
// Eliminates the N+1 the bzlhub drift walkers had.
func TestAllVersionsWithDrift_HappyPath(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	driftSeedVersion(t, s, "foo", "1.0.0")
	driftSeedVersion(t, s, "bar", "0.5.0")

	want := []byte(`{"status":"behind","behind":4}`)
	if err := s.SetDriftSummary(ctx, "foo", "1.0.0", want); err != nil {
		t.Fatalf("SetDriftSummary: %v", err)
	}

	var collected []ModuleVersionDrift
	for r, err := range s.AllVersionsWithDrift(ctx) {
		if err != nil {
			t.Fatalf("AllVersionsWithDrift: %v", err)
		}
		collected = append(collected, r)
	}
	if len(collected) != 2 {
		t.Fatalf("collected %d; want 2", len(collected))
	}
	if collected[0].Module != "bar" || collected[0].Version != "0.5.0" {
		t.Errorf("[0] = (%q, %q); want (bar, 0.5.0)", collected[0].Module, collected[0].Version)
	}
	if string(collected[0].DriftRaw) != "{}" {
		t.Errorf("bar drift = %q; want default {}", collected[0].DriftRaw)
	}
	if collected[1].Module != "foo" {
		t.Errorf("[1].Module = %q; want foo", collected[1].Module)
	}
	if string(collected[1].DriftRaw) != string(want) {
		t.Errorf("foo drift = %q; want %q", collected[1].DriftRaw, want)
	}
}

// TestAllVersionsWithDrift_EmptyDB asserts the empty contract:
// stream yields zero rows, no error.
func TestAllVersionsWithDrift_EmptyDB(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var count int
	for _, err := range s.AllVersionsWithDrift(ctx) {
		if err != nil {
			t.Fatalf("err = %v; want nil", err)
		}
		count++
	}
	if count != 0 {
		t.Errorf("count = %d; want 0", count)
	}
}

// TestAllVersionsWithDrift_BreakStopsStream asserts the early-exit
// contract: breaking out of the range loop releases the underlying
// *sql.Rows. Without this, callers that don't iterate to completion
// would leak connections. Driven by the defer rows.Close() inside
// the iter.Seq2 body.
func TestAllVersionsWithDrift_BreakStopsStream(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for i := range 10 {
		driftSeedVersion(t, s, "mod", "1.0."+string(rune('0'+i)))
	}

	var seen int
	for _, err := range s.AllVersionsWithDrift(ctx) {
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		seen++
		if seen == 3 {
			break
		}
	}
	if seen != 3 {
		t.Errorf("seen = %d; want 3 (break should stop iteration)", seen)
	}

	// A second iteration must work after the first broke — proves
	// the connection was released.
	var second int
	for range s.AllVersionsWithDrift(ctx) {
		second++
	}
	if second != 10 {
		t.Errorf("second iteration = %d; want 10 (first iteration left connection open?)", second)
	}
}

// TestAllVersionsWithDrift_CancellationSurfacesAsError asserts ctx
// cancellation surfaces as a per-row error from the iterator (not
// silent termination), so callers checking `if err != nil` see it.
func TestAllVersionsWithDrift_CancellationSurfacesAsError(t *testing.T) {
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for i := range 5 {
		driftSeedVersion(t, s, "mod", "1.0."+string(rune('0'+i)))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var sawError bool
	for _, err := range s.AllVersionsWithDrift(ctx) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v; want context.Canceled", err)
			}
			sawError = true
			break
		}
	}
	if !sawError {
		t.Errorf("expected the iterator to surface context.Canceled")
	}
}
