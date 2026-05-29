package canopy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
)

// TestBackfillDriftSummary_EmptyStoreReturnsZero asserts the no-op
// seam on an empty index. The function must not error on a fresh
// canopy boot; that would break startup for every new deployment.
func TestBackfillDriftSummary_EmptyStoreReturnsZero(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	n, err := svc.BackfillDriftSummary(ctx)
	if err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}
	if n != 0 {
		t.Errorf("rows-updated count = %d, want 0 (M1 has no drift source)", n)
	}
}

// TestBackfillDriftSummary_DoesNotWriteAtM1 asserts the M1 contract:
// even with rows present, the function does not mutate the cached
// drift. This guards against premature drift-cache writes landing in
// the seam before a real source is wired — a regression here would
// silently corrupt the "unknown by default" semantic the UI keys off.
func TestBackfillDriftSummary_DoesNotWriteAtM1(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	// Pre-state: drift is the default empty object.
	before, err := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("pre-Get: %v", err)
	}
	if string(before) != "{}" {
		t.Fatalf("pre-state drift = %q, want {} (default)", before)
	}

	n, err := svc.BackfillDriftSummary(ctx)
	if err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}
	if n != 0 {
		t.Errorf("rows-updated = %d, want 0 (M1 seam writes nothing)", n)
	}

	after, err := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("post-Get: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("seam mutated drift: before=%q, after=%q", before, after)
	}
}

// TestBackfillDriftSummary_PreservesAlreadyPopulatedRows asserts the
// seam is read-only AND idempotent against rows that ALREADY have
// drift data. A future M2/M4 expansion that adds write semantics
// must preserve this — never clobber operator-or-source-supplied
// drift data on boot. (Plan 28 cut points #1 still applies: M5+ pin
// refresh re-warms; it does not overwrite.)
func TestBackfillDriftSummary_PreservesAlreadyPopulatedRows(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "bazel_skylib", Version: "1.7.1"})

	want := api.DriftSummary{
		Status:         api.DriftStatusBehind,
		Behind:         4,
		LatestUpstream: "1.9.0",
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := svc.store.SetDriftSummary(ctx, "bazel_skylib", "1.7.1", payload); err != nil {
		t.Fatalf("seed Set: %v", err)
	}

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	got, err := svc.store.GetDriftSummary(ctx, "bazel_skylib", "1.7.1")
	if err != nil {
		t.Fatalf("post-Get: %v", err)
	}
	var d api.DriftSummary
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Status != api.DriftStatusBehind || d.Behind != 4 || d.LatestUpstream != "1.9.0" {
		t.Errorf("seam clobbered populated row: got %+v, want %+v", d, want)
	}
}
