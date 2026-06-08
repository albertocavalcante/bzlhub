package publish

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/albertocavalcante/bigorna"
)

// startGithubHealthServer spins up an httptest.Server that responds to
// GitHub's repo-health endpoint (GET /repos/<owner>/<name>) with the
// given status + body. Returns the base URL.
func startGithubHealthServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// gitWorktreeForPreflight creates a temp directory with a stub .git/
// child so preflight's "is this a git worktree?" check passes.
func gitWorktreeForPreflight(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return dir
}

func quietOutput() *publishOutput {
	return &publishOutput{stderr: &bytes.Buffer{}, stdout: &bytes.Buffer{}}
}

func TestPreflightPublish_HappyPath(t *testing.T) {
	base := startGithubHealthServer(t, http.StatusOK, `{"full_name":"o/r"}`)
	cfg := &publishConfig{
		forge:    "github",
		repo:     bigorna.Repo{Owner: "o", Name: "r"},
		baseURL:  base,
		token:    "ghp_abc",
		worktree: gitWorktreeForPreflight(t),
	}
	if err := preflightPublish(context.Background(), cfg, quietOutput()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if cfg.forgeClient == nil {
		t.Error("preflight must populate cfg.forgeClient on success")
	}
}

func TestPreflightPublish_HealthFailureWrapped(t *testing.T) {
	base := startGithubHealthServer(t, http.StatusUnauthorized, `{"message":"bad creds"}`)
	cfg := &publishConfig{
		forge:    "github",
		repo:     bigorna.Repo{Owner: "o", Name: "r"},
		baseURL:  base,
		token:    "ghp_abc",
		worktree: gitWorktreeForPreflight(t),
	}
	err := preflightPublish(context.Background(), cfg, quietOutput())
	if err == nil || !strings.Contains(err.Error(), "forge health check failed") {
		t.Fatalf("want wrapped health error, got %v", err)
	}
	if cfg.forgeClient != nil {
		t.Error("forgeClient must remain nil when health fails")
	}
}

func TestPreflightPublish_CommitModeSkipsForge(t *testing.T) {
	// No httptest server — proves the forge path is bypassed entirely.
	// An empty cfg.forge would error if buildForge were called.
	cfg := &publishConfig{
		commitMode: true,
		worktree:   gitWorktreeForPreflight(t),
	}
	if err := preflightPublish(context.Background(), cfg, quietOutput()); err != nil {
		t.Fatalf("commit-mode preflight: %v", err)
	}
	if cfg.forgeClient != nil {
		t.Error("forgeClient must stay nil in commit mode")
	}
}

func TestPreflightPublish_WorktreeNotGit(t *testing.T) {
	base := startGithubHealthServer(t, http.StatusOK, `{"full_name":"o/r"}`)
	cfg := &publishConfig{
		forge:    "github",
		repo:     bigorna.Repo{Owner: "o", Name: "r"},
		baseURL:  base,
		token:    "ghp_abc",
		worktree: t.TempDir(), // no .git/
	}
	err := preflightPublish(context.Background(), cfg, quietOutput())
	if err == nil || !strings.Contains(err.Error(), "not a git working tree") {
		t.Fatalf("want worktree error, got %v", err)
	}
}
