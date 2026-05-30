package canopy

import (
	"context"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
)

// TestM2_DriftFlowsFromMirrorThroughListModules is the M2 end-to-
// end harness: it wires the same components a real deployment uses
// — bcrmirror.Mirror, Service.UseMirror, BackfillDriftSummary,
// ListModules — and asserts a "behind" verdict computed at the
// boot pass surfaces in the JSON shape /api/v1/modules returns.
//
// The unit tests for each PR validate their own seam in isolation:
// PR6 covers the adapter, PR7 covers the backfill writer, PR8
// covers the bootstrap audit. This test catches the regression
// nobody else would: the seams between those PRs slipping out of
// alignment (e.g. ModuleSummary's Drift field shape diverging from
// what api.DriftSummary marshals to, or ListModules forgetting to
// hydrate Drift from the cache column).
//
// If this test fails, something about M2 stack composition broke;
// individual PR tests would still report green.
func TestM2_DriftFlowsFromMirrorThroughListModules(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"rules_go": `{"versions":["0.52.0","0.53.0","0.54.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "rules_go", Version: "0.52.0"})

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	summaries, err := svc.ListModules(ctx)
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}

	var found *api.ModuleSummary
	for i := range summaries {
		if summaries[i].Name == "rules_go" {
			found = &summaries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("ListModules did not return rules_go (got %d entries)", len(summaries))
	}

	if found.Drift.Status != api.DriftStatusBehind {
		t.Errorf("Drift.Status = %q; want %q", found.Drift.Status, api.DriftStatusBehind)
	}
	if found.Drift.Behind != 2 {
		t.Errorf("Drift.Behind = %d; want 2 (0.53.0, 0.54.0)", found.Drift.Behind)
	}
	if found.Drift.LatestUpstream != "0.54.0" {
		t.Errorf("Drift.LatestUpstream = %q; want 0.54.0", found.Drift.LatestUpstream)
	}
	if found.Drift.ComputedAt.IsZero() {
		t.Errorf("Drift.ComputedAt is zero; want the backfill stamp")
	}
}

// TestM2_DriftRefreshOverwritesBackfillVerdict is the second
// end-to-end stitch: after a Backfill, an explicit
// RefreshDriftSummary REWRITES the cached verdict even though
// Backfill is the "preserves populated rows" path. This is the
// "operator bootstraps then refreshes between serve boots"
// workflow that motivated the refresh verb.
func TestM2_DriftRefreshOverwritesBackfillVerdict(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"rules_go": `{"versions":["0.52.0","0.53.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "rules_go", Version: "0.52.0"})

	// First, Backfill seeds a "behind by 1" verdict at the initial
	// pass.
	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	// Then the upstream advances — simulate by swapping the
	// Mirror for a richer one. The cache still holds the old
	// behind=1 verdict; only Refresh updates.
	newMirror := seedMirrorWithModules(t, map[string]string{
		"rules_go": `{"versions":["0.52.0","0.53.0","0.54.0","0.55.0"]}`,
	})
	svc.UseMirror(newMirror)

	if _, err := svc.RefreshDriftSummary(ctx); err != nil {
		t.Fatalf("RefreshDriftSummary: %v", err)
	}

	summaries, err := svc.ListModules(ctx)
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	var found *api.ModuleSummary
	for i := range summaries {
		if summaries[i].Name == "rules_go" {
			found = &summaries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("rules_go missing from ListModules")
	}
	if found.Drift.Behind != 3 {
		t.Errorf("Drift.Behind = %d after Refresh; want 3 (Refresh saw the new upstream tip)", found.Drift.Behind)
	}
	if found.Drift.LatestUpstream != "0.55.0" {
		t.Errorf("Drift.LatestUpstream = %q after Refresh; want 0.55.0", found.Drift.LatestUpstream)
	}
}
