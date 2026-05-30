package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestNewFromRoot_FileForPlainDir asserts a plain directory (no
// .git/) yields a File backend. The operator who points `canopy
// serve --root <dir>` at a hand-assembled BCR tree shouldn't be
// forced through the git-aware path.
func TestNewFromRoot_FileForPlainDir(t *testing.T) {
	dir := t.TempDir()

	bk, err := NewFromRoot(context.Background(), dir)
	if err != nil {
		t.Fatalf("NewFromRoot: %v", err)
	}
	if _, ok := bk.(*File); !ok {
		t.Errorf("NewFromRoot(plain dir) returned %T; want *File", bk)
	}
}

// TestNewFromRoot_MirrorForGitDir asserts a git-initialised root
// yields a BCRMirror. This is the "operator cloned the registry,
// then ran canopy serve --root <clone-path>" path.
func TestNewFromRoot_MirrorForGitDir(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	// Need at least one commit so the Mirror can read HEAD.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	bk, err := NewFromRoot(context.Background(), dir)
	if err != nil {
		t.Fatalf("NewFromRoot: %v", err)
	}
	if _, ok := bk.(*BCRMirror); !ok {
		t.Errorf("NewFromRoot(git dir) returned %T; want *BCRMirror", bk)
	}
}

// TestNewFromRoot_ErrorForNonexistent asserts a missing directory
// surfaces a useful error — not a silent fallback to one shape or
// the other. The operator's --root typo shouldn't manifest as
// "every read 404s."
func TestNewFromRoot_ErrorForNonexistent(t *testing.T) {
	_, err := NewFromRoot(context.Background(), "/this/path/does/not/exist")
	if err == nil {
		t.Fatalf("NewFromRoot on missing dir returned nil; expected error")
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "not exist") {
		t.Errorf("NewFromRoot err = %v; want a not-exist error", err)
	}
}
