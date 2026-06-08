package bzlhub

import (
	"encoding/json"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// TestRefreshDriftSummary_RewritesPopulatedRows asserts the contract
// that distinguishes Refresh from Backfill: Refresh deliberately
// overwrites rows that already carry drift data, while Backfill
// preserves them (Plan 28 cut point #1). The operator runs
// `bzlhub drift refresh` precisely when they want fresh data over
// any prior verdict — typically right after `bzlhub sync bootstrap`,
// before `bzlhub serve` would naturally re-warm at next boot.
func TestRefreshDriftSummary_RewritesPopulatedRows(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0","1.1.0","1.2.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	// Pre-seed a stale verdict that Backfill would honour. Refresh
	// should overwrite it with the freshly-computed "behind".
	stale, _ := json.Marshal(api.DriftSummary{
		Status:         api.DriftStatusYankedUpstream,
		LatestUpstream: "stale-pin",
	})
	if err := svc.store.SetDriftSummary(ctx, "foo", "1.0.0", stale); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n, err := svc.RefreshDriftSummary(ctx)
	if err != nil {
		t.Fatalf("RefreshDriftSummary: %v", err)
	}
	if n < 1 {
		t.Errorf("rows-updated = %d; want >= 1", n)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.Status != api.DriftStatusBehind {
		t.Errorf("Status = %q; want %q (refresh should overwrite stale verdict)", d.Status, api.DriftStatusBehind)
	}
	if d.LatestUpstream != "1.2.0" {
		t.Errorf("LatestUpstream = %q; want 1.2.0 (refresh recomputed)", d.LatestUpstream)
	}
}

// TestRefreshDriftSummary_WritesEvenWhenNoPriorData asserts the
// other half: rows whose drift is the default '{}' state get
// written too. Refresh subsumes Backfill's behaviour for the
// fresh-row case + adds the overwrite for populated rows.
func TestRefreshDriftSummary_WritesEvenWhenNoPriorData(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"bar": `{"versions":["2.0.0","2.1.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "bar", Version: "2.0.0"})

	n, err := svc.RefreshDriftSummary(ctx)
	if err != nil {
		t.Fatalf("RefreshDriftSummary: %v", err)
	}
	if n != 1 {
		t.Errorf("rows-updated = %d; want 1", n)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "bar", "2.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.Status != api.DriftStatusBehind {
		t.Errorf("Status = %q; want %q", d.Status, api.DriftStatusBehind)
	}
}

// TestRefreshDriftSummary_NoMirrorReturnsExplicitError asserts the
// File-backed install contract: refresh requires a git-aware
// Mirror. Without one, the operator gets a clear error rather than
// a silent no-op — `bzlhub drift refresh` against a File-backed
// install would otherwise look broken ("said it ran, no chips
// changed").
func TestRefreshDriftSummary_NoMirrorReturnsExplicitError(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)
	// Deliberately do NOT call UseMirror.

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	_, err := svc.RefreshDriftSummary(ctx)
	if err == nil {
		t.Errorf("RefreshDriftSummary without Mirror returned nil; expected explicit error")
	}
}
