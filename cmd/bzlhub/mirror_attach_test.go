package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// TestAttachMirror_LogsLastSyncReadErrWhenCorrupt asserts the
// consistency contract: every code path that opens a Mirror
// reuses attachMirror, so a corrupt LAST_SYNC surfaces as a WARN
// log regardless of which CLI verb the operator ran (serve, sync
// run, drift refresh, status). The previous shape had the warning
// only in serve.go — `bzlhub drift refresh` silently recovered.
func TestAttachMirror_LogsLastSyncReadErrWhenCorrupt(t *testing.T) {
	ctx := t.Context()

	// Materialise a remote + clone via the library, then corrupt
	// the persisted LAST_SYNC by hand.
	remote := makeAttachTestRemote(t)
	mirrorDir := filepath.Join(t.TempDir(), "mirror")
	m := bcrmirror.New(mirrorDir, "file://"+remote)
	if _, err := m.Clone(ctx, bcrmirror.CloneOptions{Branch: "master"}); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mirrorDir, ".git", "LAST_SYNC"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}

	// Fresh Service + fresh Mirror (the corrupt-file path runs at
	// Open).
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cs := bzlhub.New(s)
	bk, err := backend.NewFromRoot(ctx, mirrorDir)
	if err != nil {
		t.Fatal(err)
	}

	// Capture the slog output via a TextHandler writing to a buffer.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	attachMirror(cs, bk, log)

	got := buf.String()
	if !strings.Contains(got, "LAST_SYNC unreadable") {
		t.Errorf("expected LAST_SYNC warning in log; got: %s", got)
	}
}

// TestAttachMirror_QuietOnHealthyMirror asserts the no-warn path:
// a Mirror with a valid LAST_SYNC (or no file at all) should not
// produce a WARN log.
func TestAttachMirror_QuietOnHealthyMirror(t *testing.T) {
	ctx := t.Context()
	remote := makeAttachTestRemote(t)
	mirrorDir := filepath.Join(t.TempDir(), "mirror")
	m := bcrmirror.New(mirrorDir, "file://"+remote)
	if _, err := m.Clone(ctx, bcrmirror.CloneOptions{Branch: "master"}); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cs := bzlhub.New(s)
	bk, err := backend.NewFromRoot(ctx, mirrorDir)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	attachMirror(cs, bk, log)

	if strings.Contains(buf.String(), "LAST_SYNC unreadable") {
		t.Errorf("unexpected LAST_SYNC warning on healthy mirror; got: %s", buf.String())
	}
}

// TestAttachMirror_FileBackedBackendIsNoOp asserts attachMirror
// tolerates a File backend (no Mirror) without panicking.
func TestAttachMirror_FileBackedBackendIsNoOp(t *testing.T) {
	dir := t.TempDir() // not a git repo
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cs := bzlhub.New(s)
	bk := backend.NewFile(dir)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	attachMirror(cs, bk, log) // should not panic; should not log

	if buf.Len() != 0 {
		t.Errorf("File backend produced log output: %s", buf.String())
	}
}

func makeAttachTestRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wt, _ := repo.Worktree()
	_, _ = wt.Add("README.md")
	_, _ = wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	})
	return dir
}

