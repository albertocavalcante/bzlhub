package bzlhub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// bootstrapMirrorFromRemote materialises a local file:// remote
// with one initial commit, clones it via bcrmirror, and returns
// (remotePath, mirrorPath, openedMirror). Helper used by sync_run
// tests that need a real fetch path (not just a synthetic on-disk
// Mirror like seedMirrorWithModules produces).
func bootstrapMirrorFromRemote(t *testing.T, modules map[string]string) (string, string, *bcrmirror.Mirror) {
	t.Helper()
	remote := t.TempDir()
	for name, metadata := range modules {
		modDir := filepath.Join(remote, "modules", name)
		if err := os.MkdirAll(modDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", modDir, err)
		}
		if err := os.WriteFile(filepath.Join(modDir, "metadata.json"),
			[]byte(metadata), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	repo, err := git.PlainInit(remote, false)
	if err != nil {
		t.Fatalf("PlainInit remote: %v", err)
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

	mirrorPath := filepath.Join(t.TempDir(), "mirror")
	m := bcrmirror.New(mirrorPath, "file://"+remote)
	if _, err := m.Clone(t.Context(), bcrmirror.CloneOptions{Branch: "master"}); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	return remote, mirrorPath, m
}

// advanceRemote bumps a module's upstream metadata (overwriting the
// versions list) and commits the change. Returns the new HEAD SHA.
func advanceRemote(t *testing.T, remote, module, newMetadata string) string {
	t.Helper()
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remote, "modules", module, "metadata.json"),
		[]byte(newMetadata), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add(filepath.Join("modules", module, "metadata.json")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	hash, err := wt.Commit("advance "+module, &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return hash.String()
}

// TestSyncRun_UpToDateSkipsDriftRefresh asserts the no-op-fast-path:
// when bcrmirror.Sync returns nothing to fetch, the verb writes a
// "sync_run_uptodate" audit row, leaves the drift cache alone, and
// returns UpToDate=true.
//
// Without this branch the verb would waste ~hundreds of ms walking
// every version row to confirm nothing changed.
func TestSyncRun_UpToDateSkipsDriftRefresh(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	_, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)

	// Seed a populated drift row so we can prove the refresh
	// path didn't run (a refresh would clobber it).
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})
	preMarker := []byte(`{"status":"yanked-upstream","latest_upstream":"sentinel"}`)
	if err := svc.store.SetDriftSummary(ctx, "foo", "1.0.0", preMarker); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec, err := svc.SyncRun(ctx, SyncRunOptions{})
	if err != nil {
		t.Fatalf("SyncRun: %v", err)
	}
	if !rec.UpToDate {
		t.Errorf("UpToDate = false; want true (nothing on remote to fetch)")
	}
	if rec.DriftRowsRewritten != 0 {
		t.Errorf("DriftRowsRewritten = %d; want 0 (no-op path)", rec.DriftRowsRewritten)
	}

	// Drift untouched — the sentinel survives.
	got, _ := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	if string(got) != string(preMarker) {
		t.Errorf("drift mutated on UpToDate path: got %q, want %q", got, preMarker)
	}

	events, _ := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 5})
	if len(events) == 0 || events[0].Kind != "sync_run_uptodate" {
		t.Errorf("first audit kind = %q; want sync_run_uptodate (events=%d)",
			firstKind(events), len(events))
	}
}

// TestSyncRun_AdvanceRefreshesDrift asserts the steady-state path:
// when the remote has new commits, Sync advances HEAD, the receipt
// captures From/ToSHA + commit count, and drift refresh runs
// against the new upstream metadata.
func TestSyncRun_AdvanceRefreshesDrift(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	preSHA, _ := mirror.SnapshotSHA(ctx)

	// Remote advances: foo gets versions 1.1.0 + 1.2.0.
	advanceRemote(t, remote, "foo", `{"versions":["1.0.0","1.1.0","1.2.0"]}`)

	rec, err := svc.SyncRun(ctx, SyncRunOptions{})
	if err != nil {
		t.Fatalf("SyncRun: %v", err)
	}
	if rec.UpToDate {
		t.Errorf("UpToDate = true; want false (remote advanced)")
	}
	if rec.FromSHA != preSHA {
		t.Errorf("FromSHA = %q; want %q (pre-sync HEAD)", rec.FromSHA, preSHA)
	}
	if rec.FromSHA == rec.ToSHA {
		t.Errorf("From == To = %q; want advance", rec.FromSHA)
	}
	if rec.Commits == 0 {
		t.Errorf("Commits = 0; want at least 1")
	}
	if rec.DriftRowsRewritten == 0 {
		t.Errorf("DriftRowsRewritten = 0; want at least 1 (refresh ran)")
	}

	// Drift now reflects upstream's new versions.
	summaries, _ := svc.ListModules(ctx)
	var d *api.DriftSummary
	for i := range summaries {
		if summaries[i].Name == "foo" {
			d = &summaries[i].Drift
			break
		}
	}
	if d == nil {
		t.Fatalf("foo missing from ListModules")
	}
	if d.Status != api.DriftStatusBehind {
		t.Errorf("Status = %q; want %q (sync_run picked up the new upstream)", d.Status, api.DriftStatusBehind)
	}
	if d.Behind != 2 {
		t.Errorf("Behind = %d; want 2", d.Behind)
	}

	events, _ := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 5})
	if len(events) == 0 || events[0].Kind != "sync_run_success" {
		t.Errorf("first audit kind = %q; want sync_run_success", firstKind(events))
	}
}

// TestSyncRun_NotFastForwardRefusedWithoutForce asserts the safety
// contract: when local + remote have diverged (the "operator hand-
// edited the mirror" case Plan 21 explicitly warns about), Sync
// refuses without explicit --force opt-in. Audit row captures the
// refusal so post-mortem reviewers see "operator tried, was refused".
func TestSyncRun_NotFastForwardRefusedWithoutForce(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote, mirrorPath, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)

	// Local diverges: commit a file locally.
	repo, err := git.PlainOpen(mirrorPath)
	if err != nil {
		t.Fatalf("PlainOpen local: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mirrorPath, "local.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add("local.txt"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := wt.Commit("local div", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	}); err != nil {
		t.Fatalf("Commit local: %v", err)
	}
	// Remote advances independently.
	advanceRemote(t, remote, "foo", `{"versions":["1.0.0","1.1.0"]}`)

	_, err = svc.SyncRun(ctx, SyncRunOptions{})
	if !errors.Is(err, bcrmirror.ErrNotFastForward) {
		t.Errorf("err = %v; want errors.Is(_, bcrmirror.ErrNotFastForward)", err)
	}

	events, _ := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 5})
	if len(events) == 0 || events[0].Kind != "sync_run_failure" {
		t.Errorf("first audit kind = %q; want sync_run_failure", firstKind(events))
	}
	if events[0].OK {
		t.Errorf("audit OK = true on failure; want false")
	}
}

// TestSyncRun_NotFastForwardSucceedsWithForce asserts Force=true
// permits the divergent pull and the verb completes cleanly.
func TestSyncRun_NotFastForwardSucceedsWithForce(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote, mirrorPath, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	// Diverge local + remote (same shape as the previous test).
	repo, _ := git.PlainOpen(mirrorPath)
	_ = os.WriteFile(filepath.Join(mirrorPath, "local.txt"), []byte("x"), 0o644)
	wt, _ := repo.Worktree()
	_, _ = wt.Add("local.txt")
	_, _ = wt.Commit("local div", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	})
	advanceRemote(t, remote, "foo", `{"versions":["1.0.0","1.1.0"]}`)

	rec, err := svc.SyncRun(ctx, SyncRunOptions{Force: true})
	if err != nil {
		t.Fatalf("SyncRun with Force: %v", err)
	}
	if rec.UpToDate {
		t.Errorf("UpToDate = true with Force; want false")
	}
	if rec.ToSHA == "" {
		t.Errorf("ToSHA empty after Force reset")
	}
}

// TestSyncRun_SkipRefreshHonored asserts SyncRunOptions.SkipRefresh
// prevents the post-Sync drift recompute even on the advance path.
// Used by operators who want to inspect upstream changes before
// canopy rewrites drift verdicts in their index.
func TestSyncRun_SkipRefreshHonored(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	remote, _, mirror := bootstrapMirrorFromRemote(t, map[string]string{
		"foo": `{"versions":["1.0.0"]}`,
	})
	svc.UseMirror(mirror)
	writeServiceReport(t, ctx, svc, &report.ModuleReport{Name: "foo", Version: "1.0.0"})

	// Seed a stable drift verdict so we can prove refresh did NOT run.
	stable := []byte(`{"status":"yanked-upstream","latest_upstream":"marker"}`)
	if err := svc.store.SetDriftSummary(ctx, "foo", "1.0.0", stable); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Advance the remote so Sync DOES advance — otherwise UpToDate
	// path already skips refresh and we'd pass for the wrong reason.
	advanceRemote(t, remote, "foo", `{"versions":["1.0.0","1.1.0"]}`)

	rec, err := svc.SyncRun(ctx, SyncRunOptions{SkipRefresh: true})
	if err != nil {
		t.Fatalf("SyncRun: %v", err)
	}
	if rec.UpToDate {
		t.Fatalf("UpToDate = true; want false (advance happened)")
	}
	if rec.DriftRowsRewritten != 0 {
		t.Errorf("DriftRowsRewritten = %d; want 0 (refresh skipped)", rec.DriftRowsRewritten)
	}

	// Drift unchanged.
	got, _ := svc.store.GetDriftSummary(ctx, "foo", "1.0.0")
	if string(got) != string(stable) {
		t.Errorf("drift mutated with SkipRefresh=true: got %q, want %q", got, stable)
	}
}

// TestSyncRunAudit_CapturesRefreshError asserts the audit row
// surfaces drift-refresh failure alongside Sync success. Without
// this, an operator reading `bzlhub sync history` would see a
// green sync_run_success row while drift didn't actually advance.
// The slog.Warn alone isn't auditable.
func TestSyncRunAudit_CapturesRefreshError(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	// Direct call to the audit helper with a non-nil refresh error.
	// The producer's responsibility is "capture refreshErr in the
	// payload"; SyncRun's responsibility is to pass it. We test the
	// former here (the harder-to-trigger path); SyncRun's wiring is
	// covered by visual review + the AdvanceRefreshesDrift test.
	sr := bcrmirror.SyncReceipt{FromSHA: "aaa111", ToSHA: "bbb222", Commits: 3}
	svc.recordSyncRunAudit(ctx, "sync_run_success", time.Now(), sr, 0, nil, errors.New("store closed"))

	events, _ := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 1})
	if len(events) != 1 {
		t.Fatalf("got %d audit events; want 1", len(events))
	}
	var payload syncAuditPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.DriftRefreshError == "" {
		t.Errorf("payload missing drift_refresh_error (got %+v)", payload)
	}
	if !strings.Contains(payload.DriftRefreshError, "store closed") {
		t.Errorf("drift_refresh_error = %q; want underlying err to surface", payload.DriftRefreshError)
	}
}

// TestSyncRun_NoMirrorReturnsExplicitError asserts the File-backed
// install path mirrors RefreshDriftSummary's contract: explicit
// error, not silent no-op.
func TestSyncRun_NoMirrorReturnsExplicitError(t *testing.T) {
	ctx := t.Context()
	svc := newTestService(t)

	_, err := svc.SyncRun(ctx, SyncRunOptions{})
	if !errors.Is(err, ErrNoMirrorForDrift) {
		t.Errorf("err = %v; want ErrNoMirrorForDrift", err)
	}
}

// firstKind returns events[0].Kind when present, "" otherwise, for
// readable assertion messages.
func firstKind(events []store.AuditEvent) string {
	if len(events) == 0 {
		return ""
	}
	return events[0].Kind
}
