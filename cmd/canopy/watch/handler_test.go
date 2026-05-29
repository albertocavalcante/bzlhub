package watch

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildSyncHandler_SyncOnlyWithoutDB(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	h, cleanup, err := buildSyncHandler(context.Background(),
		watchConfig{worktree: "/tmp/w", remote: "origin", baseBranch: "main"},
		logger)
	if err != nil {
		t.Fatalf("buildSyncHandler: %v", err)
	}
	if h.canopyStore != nil {
		t.Error("canopyStore must stay nil in sync-only mode")
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even in sync-only mode")
	}
	cleanup() // must be safe to call

	if !strings.Contains(buf.String(), "sync-only mode") {
		t.Errorf("missing sync-only log line: %s", buf.String())
	}
}

func TestBuildSyncHandler_OpensDBWhenPathSet(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "canopy.db")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	h, cleanup, err := buildSyncHandler(context.Background(),
		watchConfig{worktree: "/tmp/w", remote: "origin", baseBranch: "main", dbPath: dbPath},
		logger)
	if err != nil {
		t.Fatalf("buildSyncHandler: %v", err)
	}
	if h.canopyStore == nil {
		t.Error("canopyStore must be populated when dbPath is set")
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil when db is open")
	}
	if !strings.Contains(buf.String(), "re-ingest enabled") {
		t.Errorf("missing re-ingest log line: %s", buf.String())
	}

	// Cleanup should close the store; calling it twice must not panic.
	cleanup()
	cleanup()
}

func TestBuildSyncHandler_BadDBPathSurfacesError(t *testing.T) {
	// Parent dir does not exist; sqlite cannot create the file.
	dbPath := filepath.Join(t.TempDir(), "missing", "nested", "canopy.db")

	h, cleanup, err := buildSyncHandler(context.Background(),
		watchConfig{worktree: "/tmp/w", remote: "origin", baseBranch: "main", dbPath: dbPath},
		discardLogger())
	if err == nil {
		t.Fatal("want error on unreachable dbPath")
	}
	if !strings.Contains(err.Error(), "open db") {
		t.Errorf("error should be wrapped with 'open db': %v", err)
	}
	if h != nil {
		t.Error("handler must be nil on failure")
	}
	if cleanup == nil {
		t.Error("cleanup must be non-nil even on error (caller defers it)")
	}
}

func TestBuildSyncHandler_FieldsPropagated(t *testing.T) {
	h, cleanup, err := buildSyncHandler(context.Background(),
		watchConfig{worktree: "/wt", remote: "upstream", baseBranch: "release"},
		discardLogger())
	if err != nil {
		t.Fatalf("buildSyncHandler: %v", err)
	}
	defer cleanup()
	if h.worktree != "/wt" || h.remote != "upstream" || h.branch != "release" {
		t.Errorf("handler fields not propagated from cfg: %+v", h)
	}
}
