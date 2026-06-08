package bzlhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/api"
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
	ctx := t.Context()
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

// TestM2_BackfillRespectsCanceledContext asserts the per-row
// loop bails on context cancellation instead of trying every
// row with a known-doomed ctx. Operator who Ctrl-C's a slow boot
// should see the function return promptly, not after walking
// thousands of rows that each fail.
func TestM2_BackfillRespectsCanceledContext(t *testing.T) {
	svc := newTestService(t)
	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	for i := range 50 {
		writeServiceReport(t, t.Context(), svc, &report.ModuleReport{
			Name:    fmt.Sprintf("mod-%03d", i),
			Version: "1.0.0",
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n, err := svc.BackfillDriftSummary(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("rows-written = %d on cancelled ctx; want 0", n)
	}
}

// TestM2_RefreshRespectsCanceledContext — same contract for
// the refresh path.
func TestM2_RefreshRespectsCanceledContext(t *testing.T) {
	svc := newTestService(t)
	_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	for i := range 50 {
		writeServiceReport(t, t.Context(), svc, &report.ModuleReport{
			Name:    fmt.Sprintf("mod-%03d", i),
			Version: "1.0.0",
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n, err := svc.RefreshDriftSummary(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("rows-rewritten = %d on cancelled ctx; want 0", n)
	}
}

// TestM2_BackfillScalesToHundredsOfRows is a perf guard against
// the N+1 regression: backfill walks every (module, version) row
// via a single SQL query that returns the drift payload alongside
// the row. The old shape ran len(rows) + 1 queries; on a 200-row
// index that's enough to make the boot pass noticeably slow.
//
// This is a regression bound, not a benchmark — assert "well under
// 5 seconds" rather than a precise number, so flaky CI doesn't
// flip the test red on coincidental slowdowns.
func TestM2_BackfillScalesToHundredsOfRows(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)
	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)

	const N = 200
	for i := range N {
		writeServiceReport(t, ctx, svc, &report.ModuleReport{
			Name:    fmt.Sprintf("mod-%03d", i),
			Version: "1.0.0",
		})
	}

	start := time.Now()
	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("BackfillDriftSummary over %d rows took %s; want < 5s (N+1 regression?)", N, elapsed)
	}
}

// TestM2_LastSyncSurvivesProcessBoundary asserts the end-to-end
// LAST_SYNC story: SyncBootstrap (which holds its own Mirror
// instance) writes LAST_SYNC, then a fresh Service + fresh Mirror
// open the on-disk clone and Backfill stamps SyncedAt with the
// persisted timestamp — proving the file-backed handoff works
// without anything in the same process keeping it alive.
//
// Production flow: `bzlhub sync bootstrap` (process A) then
// `bzlhub serve --root <mirror>` (process B). This test simulates
// it without actually forking.
func TestM2_LastSyncSurvivesProcessBoundary(t *testing.T) {
	ctx := t.Context()

	// "Process A" — bootstrap. Service is throwaway: SyncBootstrap
	// doesn't UseMirror, so the Mirror it creates is dropped after
	// the call. Persistence happens via LAST_SYNC.
	procA := newTestService(t)
	remote := makeFileRemoteFor(t)
	target := filepath.Join(t.TempDir(), "mirror")
	if _, err := procA.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("SyncBootstrap: %v", err)
	}

	// "Process B" — open the clone in a fresh Mirror, wire it to a
	// fresh Service, run Backfill, observe SyncedAt landed.
	procB := newTestService(t)
	mirrorB := bcrmirror.New(target, "")
	if err := mirrorB.Open(ctx); err != nil {
		t.Fatalf("Mirror.Open in process B: %v", err)
	}
	procB.UseMirror(mirrorB)

	writeServiceReport(t, ctx, procB, &report.ModuleReport{Name: "foo", Version: "1.0.0"})
	if _, err := procB.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary in process B: %v", err)
	}

	got, _ := procB.store.GetDriftSummary(ctx, "foo", "1.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.SyncedAt.IsZero() {
		t.Errorf("SyncedAt is zero after cross-process flow; LAST_SYNC didn't survive Open in process B")
	}
}

// TestM2_DriftRefreshOverwritesBackfillVerdict is the second
// end-to-end stitch: after a Backfill, an explicit
// RefreshDriftSummary REWRITES the cached verdict even though
// Backfill is the "preserves populated rows" path. This is the
// "operator bootstraps then refreshes between serve boots"
// workflow that motivated the refresh verb.
func TestM2_DriftRefreshOverwritesBackfillVerdict(t *testing.T) {
	ctx := t.Context()
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
