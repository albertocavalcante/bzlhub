package canopy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
)

// seedMirrorWithModules materialises a fixture git repo on-disk
// populated with the supplied modules, commits the tree, and returns
// an Open'd bcrmirror.Mirror pointed at it. Each entry's value is
// the verbatim metadata.json bytes written under
// modules/<name>/metadata.json.
func seedMirrorWithModules(t *testing.T, modules map[string]string) *bcrmirror.Mirror {
	t.Helper()
	dir := t.TempDir()
	for name, metadata := range modules {
		modDir := filepath.Join(dir, "modules", name)
		if err := os.MkdirAll(modDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", modDir, err)
		}
		if err := os.WriteFile(filepath.Join(modDir, "metadata.json"), []byte(metadata), 0o644); err != nil {
			t.Fatalf("WriteFile %s/metadata.json: %v", name, err)
		}
	}

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatalf("AddGlob: %v", err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	m := bcrmirror.New(dir, "")
	if err := m.Open(context.Background()); err != nil {
		t.Fatalf("Mirror.Open: %v", err)
	}
	return m
}

// TestBackfillDriftSummary_WritesBehindWhenMirrorWired asserts the
// PR7 contract: when the Service has a Mirror, the seam computes
// drift status per local version row by comparing against the
// upstream metadata.json read from the mirror at HEAD. The
// behind-by-N path is the most load-bearing — the DriftChip's
// primary visual trigger.
func TestBackfillDriftSummary_WritesBehindWhenMirrorWired(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0","1.1.0","1.2.0"]}`,
	})
	svc.UseMirror(mirror)

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	n, err := svc.BackfillDriftSummary(ctx)
	if err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}
	if n < 1 {
		t.Errorf("rows-updated = %d; want >= 1 (one row pending compute)", n)
	}

	got, err := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var d api.DriftSummary
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Status != api.DriftStatusBehind {
		t.Errorf("Status = %q; want %q", d.Status, api.DriftStatusBehind)
	}
	if d.Behind != 2 {
		t.Errorf("Behind = %d; want 2 (1.1.0, 1.2.0)", d.Behind)
	}
	if d.LatestUpstream != "1.2.0" {
		t.Errorf("LatestUpstream = %q; want 1.2.0", d.LatestUpstream)
	}
	if d.ComputedAt.IsZero() {
		t.Errorf("ComputedAt is zero; want a recent timestamp")
	}
}

// TestBackfillDriftSummary_WritesInSyncWhenLocalMatches asserts the
// in-sync path also gets a positive write — Plan 21 honest-staleness:
// "we checked this at X, it was in-sync" carries real signal even
// though the UI hides the chip.
func TestBackfillDriftSummary_WritesInSyncWhenLocalMatches(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"bar": `{"versions":["2.0.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "bar", Version: "2.0.0"})

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "bar", "2.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.Status != api.DriftStatusInSync {
		t.Errorf("Status = %q; want %q", d.Status, api.DriftStatusInSync)
	}
	if d.ComputedAt.IsZero() {
		t.Errorf("ComputedAt is zero; in-sync rows still carry a computed timestamp")
	}
}

// TestBackfillDriftSummary_WritesLocalOnlyWhenUpstreamMissing asserts
// that local modules absent from the mirror are flagged local-only.
// MetadataAt returns ErrModuleNotFound for that case; the backfill
// translates to the local-only status.
func TestBackfillDriftSummary_WritesLocalOnlyWhenUpstreamMissing(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	// local-only/3.0.0 has no upstream presence.
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "internal-fork", Version: "3.0.0"})

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "internal-fork", "3.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.Status != api.DriftStatusLocalOnly {
		t.Errorf("Status = %q; want %q (module absent upstream)", d.Status, api.DriftStatusLocalOnly)
	}
}

// TestBackfillDriftSummary_WritesYankedWhenUpstreamYanked asserts
// the yanked-upstream signal. Plan 19 Idea A: yanked > behind; the
// security signal beats the freshness signal regardless of whether
// newer versions exist.
func TestBackfillDriftSummary_WritesYankedWhenUpstreamYanked(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"baz": `{"versions":["1.0.0","1.1.0"],"yanked_versions":{"1.0.0":"CVE-2026-XXXX"}}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "baz", Version: "1.0.0"})

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "baz", "1.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.Status != api.DriftStatusYankedUpstream {
		t.Errorf("Status = %q; want %q (1.0.0 is yanked even though 1.1.0 exists)", d.Status, api.DriftStatusYankedUpstream)
	}
}

// TestBackfillDriftSummary_DoesNotClobberPopulatedRows asserts the
// existing-data-preserves invariant carries through PR7. Plan 28 cut
// point #1 is explicit: refresh re-warms, it does not overwrite —
// even with a Mirror wired, an operator-or-sync-runner-supplied
// drift row stays intact on boot. The boot pass seeds; the sync
// runner (Plan 21 B4) updates.
func TestBackfillDriftSummary_DoesNotClobberPopulatedRows(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0","1.1.0","1.2.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	// Pre-seed an operator-supplied "yanked-upstream" verdict. PR7's
	// boot pass would otherwise overwrite this with "behind".
	seeded, _ := json.Marshal(api.DriftSummary{
		Status:         api.DriftStatusYankedUpstream,
		LatestUpstream: "operator-pinned",
	})
	if err := svc.store.SetDriftSummary(ctx, "foo", "1.0.0", seeded); err != nil {
		t.Fatalf("seed Set: %v", err)
	}

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	var d api.DriftSummary
	_ = json.Unmarshal(got, &d)
	if d.Status != api.DriftStatusYankedUpstream {
		t.Errorf("populated row clobbered: Status = %q; want %q", d.Status, api.DriftStatusYankedUpstream)
	}
	if d.LatestUpstream != "operator-pinned" {
		t.Errorf("populated row clobbered: LatestUpstream = %q; want %q", d.LatestUpstream, "operator-pinned")
	}
}

// TestBackfillDriftSummary_NoMirrorNoWrites asserts the File-backed
// install path. When Service.mirror == nil, the seam is a no-op
// regardless of pending rows. Operators on a hand-assembled BCR
// directory fall back to `canopy drift` for on-demand inspection.
func TestBackfillDriftSummary_NoMirrorNoWrites(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	// Deliberately do NOT call UseMirror.

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	n, err := svc.BackfillDriftSummary(ctx)
	if err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}
	if n != 0 {
		t.Errorf("no-mirror rows-updated = %d; want 0", n)
	}

	got, _ := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	if string(got) != "{}" {
		t.Errorf("no-mirror drift = %q; want {} (untouched)", got)
	}
}
