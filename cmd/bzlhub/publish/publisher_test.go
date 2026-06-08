package publish

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bzlhub/internal/publish"
)

// stubForge satisfies bigorna.Forge with no-op methods. buildPublisher
// only needs a non-nil Forge to pass NewGitPR's validation; the tests
// never invoke any method.
type stubForge struct{}

func (stubForge) OpenPR(context.Context, bigorna.OpenPROpts) (bigorna.PR, error) {
	return bigorna.PR{}, nil
}
func (stubForge) GetPR(context.Context, bigorna.Repo, int) (bigorna.PR, error) {
	return bigorna.PR{}, nil
}
func (stubForge) ListOpenPRs(context.Context, bigorna.Repo, string) ([]bigorna.PR, error) {
	return nil, nil
}
func (stubForge) Comment(context.Context, bigorna.Repo, int, string) error { return nil }
func (stubForge) ListNewCommits(context.Context, bigorna.Repo, string, string, string) (
	[]bigorna.Commit, string, bool, error,
) {
	return nil, "", false, nil
}
func (stubForge) Health(context.Context) error { return nil }

func realGitWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return dir
}

func TestBuildPublisher_DryRunReturnsFilesystemAndCleansUp(t *testing.T) {
	pattern := filepath.Join(os.TempDir(), "canopy-publish-dryrun-*")
	before, _ := filepath.Glob(pattern)

	pub, cleanup, err := buildPublisher(publishConfig{dryRun: true})
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	if _, ok := pub.(*publish.FilesystemPublisher); !ok {
		t.Errorf("dry-run: want *FilesystemPublisher, got %T", pub)
	}

	after, _ := filepath.Glob(pattern)
	if len(after) != len(before)+1 {
		t.Fatalf("scratch dir not created: before=%d after=%d", len(before), len(after))
	}

	cleanup()

	post, _ := filepath.Glob(pattern)
	if len(post) != len(before) {
		t.Errorf("cleanup did not remove scratch dir: before=%d post=%d", len(before), len(post))
	}
}

func TestBuildPublisher_CommitModeReturnsGitDirect(t *testing.T) {
	cfg := publishConfig{
		commitMode: true,
		worktree:   realGitWorktree(t),
		baseBranch: "main",
		bot:        publish.Identity{Name: "canopy", Email: "c@example.test"},
	}
	pub, cleanup, err := buildPublisher(cfg)
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	defer cleanup() // must be safe even when no-op
	if _, ok := pub.(*publish.GitDirectPublisher); !ok {
		t.Errorf("commit mode: want *GitDirectPublisher, got %T", pub)
	}
}

func TestBuildPublisher_PRModeReturnsGitPR(t *testing.T) {
	cfg := publishConfig{
		worktree:    realGitWorktree(t),
		baseBranch:  "main",
		bot:         publish.Identity{Name: "canopy", Email: "c@example.test"},
		repo:        bigorna.Repo{Owner: "o", Name: "r"},
		forgeClient: stubForge{},
	}
	pub, cleanup, err := buildPublisher(cfg)
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	defer cleanup()
	if _, ok := pub.(*publish.GitPRPublisher); !ok {
		t.Errorf("PR mode: want *GitPRPublisher, got %T", pub)
	}
}

func TestBuildPublisher_NonNilCleanupAlways(t *testing.T) {
	// Even on the no-cleanup branches the helper must return a callable
	// func — runPublish does `defer cleanup()` unconditionally.
	cfg := publishConfig{
		commitMode: true,
		worktree:   realGitWorktree(t),
		baseBranch: "main",
		bot:        publish.Identity{Name: "canopy", Email: "c@example.test"},
	}
	_, cleanup, err := buildPublisher(cfg)
	if err != nil {
		t.Fatalf("buildPublisher: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil; runPublish defers it unconditionally")
	}
	cleanup() // must not panic
}
