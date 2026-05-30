package canopy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/albertocavalcante/canopy/internal/store"
)

// makeFileRemote materialises a local "remote" registry on disk —
// one BCR-shape commit's worth of files plus the .git that allows
// bcrmirror.Clone to fetch via `file://`. Returns the on-disk path.
func makeFileRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "modules", "foo"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "modules", "foo", "metadata.json"),
		[]byte(`{"versions":["1.0.0"]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
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
	return dir
}

// TestSyncBootstrap_ClonesIntoEmptyPath asserts the happy path: a
// fresh target dir + a reachable remote → a populated clone, a
// non-zero HEAD SHA in the receipt, and one audit event with
// OK=true.
func TestSyncBootstrap_ClonesIntoEmptyPath(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	remote := makeFileRemote(t)
	target := filepath.Join(t.TempDir(), "mirror")

	rec, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	})
	if err != nil {
		t.Fatalf("SyncBootstrap: %v", err)
	}
	if rec.SHA == "" {
		t.Errorf("receipt.SHA is empty; expected hex")
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Errorf("expected %s/.git to exist after bootstrap: %v", target, err)
	}

	// Audit row should be present with OK=true. Read directly from
	// the store — this is the wire shape /api/history serves, so a
	// failing assertion here surfaces a real operator-visible
	// regression.
	events, err := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected one audit event after bootstrap; got none")
	}
	ev := events[0]
	if ev.Kind != "sync_bootstrap_success" {
		t.Errorf("audit Kind = %q; want sync_bootstrap_success", ev.Kind)
	}
	if !ev.OK {
		t.Errorf("audit OK = false; want true")
	}
}

// TestSyncBootstrap_RefusesOnExistingCloneWithoutReinit asserts the
// idempotent contract: when the target already holds a clone, the
// caller must opt into a destructive reinit. Bare second call → an
// ErrAlreadyBootstrapped sentinel; the existing clone is untouched.
func TestSyncBootstrap_RefusesOnExistingCloneWithoutReinit(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	remote := makeFileRemote(t)
	target := filepath.Join(t.TempDir(), "mirror")

	if _, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}

	// Capture the pre-state inode so we can prove the second call
	// didn't even touch the working tree.
	info, err := os.Stat(filepath.Join(target, ".git"))
	if err != nil {
		t.Fatalf("Stat pre-state: %v", err)
	}
	preModTime := info.ModTime()

	_, err = svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	})
	if !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Errorf("second bootstrap err = %v; want ErrAlreadyBootstrapped", err)
	}

	info2, err := os.Stat(filepath.Join(target, ".git"))
	if err != nil {
		t.Fatalf("Stat post-state: %v", err)
	}
	if !info2.ModTime().Equal(preModTime) {
		t.Errorf("second bootstrap mutated .git mod time: before=%v after=%v", preModTime, info2.ModTime())
	}
}

// TestSyncBootstrap_ReinitWipesAndReclones asserts the destructive
// path: with Reinit=true on an existing clone, the function wipes
// the target and clones fresh. The receipt SHA changes if the
// remote's HEAD advanced (here it doesn't, but the call must
// succeed and the clone must be intact).
func TestSyncBootstrap_ReinitWipesAndReclones(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	remote := makeFileRemote(t)
	target := filepath.Join(t.TempDir(), "mirror")

	if _, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
	}); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}

	// Operator litters a stray file the second clone must wipe.
	if err := os.WriteFile(filepath.Join(target, "STRAY.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile stray: %v", err)
	}

	rec, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file://" + remote,
		MirrorPath: target,
		Branch:     "master",
		Reinit:     true,
	})
	if err != nil {
		t.Fatalf("Reinit bootstrap: %v", err)
	}
	if rec.SHA == "" {
		t.Errorf("Reinit receipt.SHA is empty")
	}

	if _, err := os.Stat(filepath.Join(target, "STRAY.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Reinit didn't wipe stray file: stat err = %v; want ErrNotExist", err)
	}
}

// TestSyncBootstrap_AuditOnFailure asserts a failed clone (bad
// remote URL) still produces an audit row, with OK=false and the
// error message captured. Operators auditing what happened need the
// failure record as much as the success record.
func TestSyncBootstrap_AuditOnFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	target := filepath.Join(t.TempDir(), "mirror")
	_, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote:     "file:///definitely/not/a/remote",
		MirrorPath: target,
		Branch:     "master",
	})
	if err == nil {
		t.Fatalf("SyncBootstrap with bad remote returned nil; expected error")
	}

	events, err := svc.store.ListAudit(ctx, store.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected one audit event on failure; got none")
	}
	ev := events[0]
	if ev.Kind != "sync_bootstrap_failure" {
		t.Errorf("audit Kind = %q; want sync_bootstrap_failure", ev.Kind)
	}
	if ev.OK {
		t.Errorf("audit OK = true; want false")
	}
	if ev.Error == "" {
		t.Errorf("audit Error is empty; expected the underlying error message")
	}
}

// TestSyncBootstrap_RequiresRemote asserts the explicit-failure
// contract on missing Remote.
func TestSyncBootstrap_RequiresRemote(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	_, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		MirrorPath: filepath.Join(t.TempDir(), "mirror"),
	})
	if err == nil {
		t.Errorf("SyncBootstrap with empty Remote returned nil; expected error")
	}
}

// TestSyncBootstrap_RequiresMirrorPath asserts the explicit-failure
// contract on missing MirrorPath.
func TestSyncBootstrap_RequiresMirrorPath(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	_, err := svc.SyncBootstrap(ctx, SyncBootstrapOptions{
		Remote: "file:///whatever",
	})
	if err == nil {
		t.Errorf("SyncBootstrap with empty MirrorPath returned nil; expected error")
	}
}
