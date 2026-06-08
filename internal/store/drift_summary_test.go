package store

import (
	"context"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// driftSeedVersion is a tiny helper shared by this file's tests:
// writes a single minimal (name, version) row via the canonical
// WriteReport path. Mirrors the pattern in has_source_index_test.go.
func driftSeedVersion(t *testing.T, s *Store, name, version string) {
	t.Helper()
	if err := s.WriteReport(context.Background(),
		&report.ModuleReport{Name: name, Version: version}); err != nil {
		t.Fatalf("WriteReport(%s@%s): %v", name, version, err)
	}
}

// TestDriftSummary_MigrationAddsColumn asserts the boot-time
// migration is idempotent: Open() runs ensureColumn for
// drift_summary_json; a second Open() against the same DB is a
// no-op (no error, column unchanged). This is the same pattern
// has_source_index and tarball_size use; the test guards the
// composition.
func TestDriftSummary_MigrationAddsColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := t.TempDir() + "/bzlhub.db"

	// First open: creates schema + applies migration.
	s1, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second open against the same DB: migration is idempotent.
	s2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("second Open (idempotency check): %v", err)
	}
	defer func() { _ = s2.Close() }()

	// And the column is actually queryable.
	if _, err := s2.db.ExecContext(ctx,
		`SELECT drift_summary_json FROM versions WHERE 1=0`); err != nil {
		t.Errorf("drift_summary_json not queryable after migration: %v", err)
	}
}

// TestDriftSummary_DefaultIsEmptyObject asserts a freshly-written
// version row reports drift_summary_json = '{}' before any
// SetDriftSummary call. The default matches has_source_index's
// "absent means default-value" semantics and the api.DriftSummary
// zero value will round-trip through it.
func TestDriftSummary_DefaultIsEmptyObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/bzlhub.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	driftSeedVersion(t, s, "foo", "1.0.0")

	got, err := s.GetDriftSummary(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetDriftSummary: %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("default drift JSON = %q, want %q", got, "{}")
	}
}

// TestDriftSummary_RoundTrip asserts SetDriftSummary then
// GetDriftSummary returns the bytes verbatim. Store layer is
// JSON-shape-agnostic; the api layer (C11) owns the canonical
// shape. Caller hands us bytes; we hand them back.
func TestDriftSummary_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/bzlhub.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	driftSeedVersion(t, s, "foo", "1.0.0")

	payload := []byte(`{"status":"behind","behind":4,"latest_upstream":"1.9.0"}`)
	if err := s.SetDriftSummary(ctx, "foo", "1.0.0", payload); err != nil {
		t.Fatalf("SetDriftSummary: %v", err)
	}

	got, err := s.GetDriftSummary(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetDriftSummary: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("round-trip = %q, want %q", got, payload)
	}
}

// TestDriftSummary_SetEmptyClearsToDefault asserts the "clear"
// semantics: passing nil or empty resets the row to '{}'. Lets
// callers express "we used to have drift data, throw it away" via
// the same setter, no separate Delete method.
func TestDriftSummary_SetEmptyClearsToDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/bzlhub.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	driftSeedVersion(t, s, "foo", "1.0.0")

	// First, set something.
	if err := s.SetDriftSummary(ctx, "foo", "1.0.0", []byte(`{"status":"behind"}`)); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	// Then clear with nil.
	if err := s.SetDriftSummary(ctx, "foo", "1.0.0", nil); err != nil {
		t.Fatalf("clear Set: %v", err)
	}

	got, err := s.GetDriftSummary(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("GetDriftSummary: %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("after clear, drift = %q, want %q", got, "{}")
	}
}

// TestDriftSummary_GetMissingRowReturnsDefault asserts the absent
// row case: GetDriftSummary on a (name, version) that doesn't
// exist returns the default '{}' with no error. Mirrors
// GetHasSourceIndex's "caller violation isn't our problem" policy.
func TestDriftSummary_GetMissingRowReturnsDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/bzlhub.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.GetDriftSummary(ctx, "nonexistent", "0.0.0")
	if err != nil {
		t.Fatalf("GetDriftSummary on missing row: %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("missing-row drift = %q, want %q", got, "{}")
	}
}
