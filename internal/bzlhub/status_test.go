package bzlhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// TestMirrorStatus_HappyPath asserts the full status shape after
// the operator workflow: bootstrap → ingest → drift refresh. Every
// counter and SHA on the status struct should reflect the state of
// the index + Mirror.
func TestMirrorStatus_HappyPath(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0","1.1.0","1.2.0"]}`,
		"bar": `{"versions":["0.5.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "bar", Version: "0.5.0"})

	if _, err := svc.BackfillDriftSummary(ctx); err != nil {
		t.Fatalf("BackfillDriftSummary: %v", err)
	}

	status, err := svc.MirrorStatus(ctx)
	if err != nil {
		t.Fatalf("MirrorStatus: %v", err)
	}

	// Module / version counters reflect the seeded index.
	if status.IndexedModules != 2 {
		t.Errorf("IndexedModules = %d; want 2", status.IndexedModules)
	}
	if status.IndexedVersions != 2 {
		t.Errorf("IndexedVersions = %d; want 2", status.IndexedVersions)
	}

	// Drift breakdown: foo@1.0.0 is behind, bar@0.5.0 is in-sync.
	if status.DriftByStatus[string(api.DriftStatusBehind)] != 1 {
		t.Errorf("DriftByStatus[behind] = %d; want 1", status.DriftByStatus[string(api.DriftStatusBehind)])
	}
	if status.DriftByStatus[string(api.DriftStatusInSync)] != 1 {
		t.Errorf("DriftByStatus[in-sync] = %d; want 1", status.DriftByStatus[string(api.DriftStatusInSync)])
	}

	// Mirror state reflects Clone's HEAD + LastSync.
	wantSHA, _ := mirror.SnapshotSHA(ctx)
	if status.MirrorHEAD != wantSHA {
		t.Errorf("MirrorHEAD = %q; want %q", status.MirrorHEAD, wantSHA)
	}
	if status.LastSync.IsZero() {
		t.Errorf("LastSync is zero; Clone should have populated it")
	}
	if status.MirrorPath == "" {
		t.Errorf("MirrorPath is empty")
	}
}

// TestMirrorStatus_NoMirrorOmitsMirrorFields asserts the File-
// backed install path: status still returns (so the operator gets
// indexed-modules count + drift breakdown), but the Mirror-
// specific fields are left at their zero values. Distinct from
// ErrNoMirrorForDrift — status is a read-only inspection, not a
// state-changing operation, so it doesn't gate on Mirror presence.
func TestMirrorStatus_NoMirrorOmitsMirrorFields(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)
	// No UseMirror.

	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	status, err := svc.MirrorStatus(ctx)
	if err != nil {
		t.Fatalf("MirrorStatus: %v", err)
	}
	if status.IndexedModules != 1 {
		t.Errorf("IndexedModules = %d; want 1", status.IndexedModules)
	}
	if status.MirrorHEAD != "" {
		t.Errorf("MirrorHEAD = %q; want empty (no Mirror attached)", status.MirrorHEAD)
	}
	if !status.LastSync.IsZero() {
		t.Errorf("LastSync = %v; want zero (no Mirror attached)", status.LastSync)
	}
	if status.MirrorPath != "" {
		t.Errorf("MirrorPath = %q; want empty (no Mirror attached)", status.MirrorPath)
	}
}

// TestMirrorStatus_JSONShape asserts the stable wire shape the
// `bzlhub status --format=json` consumers (monitoring scripts) will
// parse against. Field names are lock-checked here so a future
// rename surfaces immediately rather than after operators have
// scripts pinned to the old names.
func TestMirrorStatus_JSONShape(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	status, err := svc.MirrorStatus(ctx)
	if err != nil {
		t.Fatalf("MirrorStatus: %v", err)
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Round-trip into a map to check the wire field names.
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	required := []string{"indexed_modules", "indexed_versions", "drift_by_status", "pending_compute"}
	for _, key := range required {
		if _, ok := wire[key]; !ok {
			t.Errorf("JSON shape missing required key %q (got keys %v)", key, mapKeys(wire))
		}
	}
	// Optional keys present only when populated (Mirror attached).
	for _, key := range []string{"mirror_path", "mirror_head"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("JSON shape missing key %q when Mirror is wired (got keys %v)", key, mapKeys(wire))
		}
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestMirrorStatus_RespectsCanceledContext asserts the per-row
// loop bails on cancellation. Consistent with Backfill / Refresh
// — operators interrupting a slow query shouldn't have to wait
// out the full walk.
func TestMirrorStatus_RespectsCanceledContext(t *testing.T) {
	svc := newTestService(t)
	for i := range 20 {
		writeServiceReport(t, t.Context(), svc, &report.ModuleReport{
			Name:    fmt.Sprintf("mod-%03d", i),
			Version: "1.0.0",
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svc.MirrorStatus(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
	}
}

// TestMirrorHead_NoMirrorReturnsZero asserts the File-backed
// install branch: when no Mirror is attached, MirrorHead returns
// the zero values rather than panicking or invoking nil-pointer
// methods.
func TestMirrorHead_NoMirrorReturnsZero(t *testing.T) {
	svc := newTestService(t)
	sha, ts := svc.MirrorHead(t.Context())
	if sha != "" {
		t.Errorf("sha = %q; want empty when no Mirror", sha)
	}
	if !ts.IsZero() {
		t.Errorf("lastSync = %v; want zero when no Mirror", ts)
	}
}

// TestMirrorHead_PopulatesFromMirror asserts the wired branch:
// returns the Mirror's HEAD SHA and a non-zero LastSync after a
// real Clone via bootstrapMirrorFromRemote.
func TestMirrorHead_PopulatesFromMirror(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)

	wantSHA, _ := mirror.SnapshotSHA(ctx)
	gotSHA, gotSync := svc.MirrorHead(ctx)
	if gotSHA != wantSHA {
		t.Errorf("MirrorHead SHA = %q; want %q", gotSHA, wantSHA)
	}
	if gotSync.IsZero() {
		t.Errorf("MirrorHead lastSync is zero; expected the Clone timestamp")
	}
}

// TestMirrorStatus_DriftBreakdownIgnoresUnknownStatus asserts
// rows that predate drift compute (Status="" or "unknown") aren't
// tallied into the breakdown — only meaningful verdicts. Without
// this, an operator on a fresh install would see "unknown: 5"
// alongside the real verdicts, which is noise.
func TestMirrorStatus_DriftBreakdownIgnoresUnknownStatus(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	mirror := seedMirrorWithModules(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)

	// Two rows: one with a populated verdict, one with the
	// default '{}' (unknown).
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "bar", Version: "1.0.0"})
	seeded, _ := json.Marshal(api.DriftSummary{Status: api.DriftStatusInSync})
	_ = svc.store.SetDriftSummary(ctx, "foo", "1.0.0", seeded)
	// bar@1.0.0 stays at '{}'.

	status, err := svc.MirrorStatus(ctx)
	if err != nil {
		t.Fatalf("MirrorStatus: %v", err)
	}
	if status.DriftByStatus[string(api.DriftStatusInSync)] != 1 {
		t.Errorf("InSync count = %d; want 1", status.DriftByStatus[string(api.DriftStatusInSync)])
	}
	// Unknown rows ARE tallied separately so the operator sees
	// "5 rows pending compute" as actionable.
	if status.PendingCompute != 1 {
		t.Errorf("PendingCompute = %d; want 1 (bar@1.0.0 has default drift)", status.PendingCompute)
	}
}
