package canopy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
)

// TestServiceListModules_DriftZeroWhenUnpopulated guards the
// silent-default contract Plan 22 PR 3 asks for: every ModuleSummary
// in a freshly-ingested corpus carries an api.DriftSummary{} with
// Status == DriftStatusUnknown. The UI's chip palette reads that as
// "render no chip" — the listing page must not visually claim drift
// data exists when none was computed.
func TestServiceListModules_DriftZeroWhenUnpopulated(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	mods, err := svc.ListModules(ctx)
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	got := mods[0].Drift
	if got.Status != "" && got.Status != api.DriftStatusUnknown {
		t.Errorf("unpopulated drift status = %q, want zero or 'unknown'", got.Status)
	}
	if got.Behind != 0 || got.LatestUpstream != "" || !got.ComputedAt.IsZero() {
		t.Errorf("unpopulated DriftSummary not fully zero: %+v", got)
	}
}

// TestServiceListModules_DriftSurfacesPopulatedRow asserts the
// happy path: bytes written via Store.SetDriftSummary appear on
// ModuleSummary.Drift via the ListModules path. This is the
// integration contract Plan 22 PR 3 names by line — backend writes,
// API surfaces, UI consumes. Without this round-trip the inline
// drift badges (M2) have nothing to render.
func TestServiceListModules_DriftSurfacesPopulatedRow(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "bazel_skylib", Version: "1.7.1"})

	want := api.DriftSummary{
		Status:         api.DriftStatusBehind,
		Behind:         4,
		LatestUpstream: "1.9.0",
		ComputedAt:     time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal seed payload: %v", err)
	}
	if err := svc.store.SetDriftSummary(ctx, "bazel_skylib", "1.7.1", payload); err != nil {
		t.Fatalf("SetDriftSummary: %v", err)
	}

	mods, err := svc.ListModules(ctx)
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("got %d modules, want 1", len(mods))
	}
	got := mods[0].Drift
	if got.Status != want.Status {
		t.Errorf("Status: got %q, want %q", got.Status, want.Status)
	}
	if got.Behind != want.Behind {
		t.Errorf("Behind: got %d, want %d", got.Behind, want.Behind)
	}
	if got.LatestUpstream != want.LatestUpstream {
		t.Errorf("LatestUpstream: got %q, want %q", got.LatestUpstream, want.LatestUpstream)
	}
	if !got.ComputedAt.Equal(want.ComputedAt) {
		t.Errorf("ComputedAt: got %v, want %v", got.ComputedAt, want.ComputedAt)
	}
}

// TestServiceGetModule_DriftSurfacesPopulatedRow mirrors the
// ListModules test for the per-module GetModule path. Hover-card
// requests reach this code path; the chip popover wants the same
// shape ListModules does.
func TestServiceGetModule_DriftSurfacesPopulatedRow(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "rules_go", Version: "0.50.0"})

	want := api.DriftSummary{
		Status: api.DriftStatusYankedUpstream,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal seed payload: %v", err)
	}
	if err := svc.store.SetDriftSummary(ctx, "rules_go", "0.50.0", payload); err != nil {
		t.Fatalf("SetDriftSummary: %v", err)
	}

	got, err := svc.GetModule(ctx, "rules_go")
	if err != nil {
		t.Fatalf("GetModule: %v", err)
	}
	if got.Drift.Status != api.DriftStatusYankedUpstream {
		t.Errorf("GetModule Drift.Status = %q, want %q", got.Drift.Status, api.DriftStatusYankedUpstream)
	}
}
