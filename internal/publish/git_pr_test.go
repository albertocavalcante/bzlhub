package publish

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/albertocavalcante/bigorna"
)

// fakeForge satisfies bigorna.Forge for tests. Records OpenPR calls and
// returns a configurable PR. The other methods aren't exercised in G3
// tests so they return zero values.
type fakeForge struct {
	mu       sync.Mutex
	opened   []bigorna.OpenPROpts
	nextPR   bigorna.PR
	openErr  error
}

func (f *fakeForge) OpenPR(_ context.Context, opts bigorna.OpenPROpts) (bigorna.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, opts)
	if f.openErr != nil {
		return bigorna.PR{}, f.openErr
	}
	return f.nextPR, nil
}

func (f *fakeForge) GetPR(_ context.Context, _ bigorna.Repo, _ int) (bigorna.PR, error) {
	return bigorna.PR{}, nil
}

func (f *fakeForge) ListOpenPRs(_ context.Context, _ bigorna.Repo, _ string) ([]bigorna.PR, error) {
	return nil, nil
}

func (f *fakeForge) Comment(_ context.Context, _ bigorna.Repo, _ int, _ string) error {
	return nil
}

func (f *fakeForge) ListNewCommits(_ context.Context, _ bigorna.Repo, _, _, _ string) ([]bigorna.Commit, string, bool, error) {
	return nil, "", false, nil
}

func (f *fakeForge) Health(_ context.Context) error { return nil }

func newFakeForge() *fakeForge {
	return &fakeForge{
		nextPR: bigorna.PR{
			Number: 42,
			URL:    "https://github.com/o/r/pull/42",
			State:  bigorna.PRStateOpen,
		},
	}
}

func TestGitPR_RoundTrip(t *testing.T) {
	workTree, bare := setupBareRepo(t)
	ff := newFakeForge()

	pub, err := NewGitPR(GitPRConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bzlhub-bot", Email: "bzlhub@example.com"},
		Repo:        bigorna.Repo{Owner: "o", Name: "r"},
		Forge:       ff,
		Labels:      []string{"bzlhub/auto"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	sink, err := pub.BeginBlob(ctx, "https://example.com/foo-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	ref, err := sink.Close()
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := pub.Publish(ctx, PublishRequest{
		Module:     "foo",
		Version:    "1.0.0",
		SourceJSON: []byte(`{"url":"https://example.com/foo-1.0.0.tar.gz"}`),
		Blob:       ref,
		SourceURL:  "https://example.com/foo-1.0.0.tar.gz",
		Requester:  Identity{Name: "Alberto Cavalcante", Email: "alberto@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Strategy != "git-pr" {
		t.Fatalf("strategy: %q", receipt.Strategy)
	}
	if receipt.Commit == "" {
		t.Fatalf("commit empty")
	}

	// OpenPR called exactly once with expected opts.
	if len(ff.opened) != 1 {
		t.Fatalf("OpenPR calls: %d", len(ff.opened))
	}
	got := ff.opened[0]
	wantBranch := "bzlhub/add-foo-1.0.0"
	if got.HeadBranch != wantBranch {
		t.Fatalf("HeadBranch: %q, want %q", got.HeadBranch, wantBranch)
	}
	if got.BaseBranch != "main" {
		t.Fatalf("BaseBranch: %q", got.BaseBranch)
	}
	if got.Repo != (bigorna.Repo{Owner: "o", Name: "r"}) {
		t.Fatalf("Repo: %+v", got.Repo)
	}
	if got.Title != "Add foo@1.0.0" {
		t.Fatalf("Title: %q", got.Title)
	}
	if !strings.Contains(got.Body, "Alberto Cavalcante") || !strings.Contains(got.Body, ref.Integrity) {
		t.Fatalf("Body lacks requester or integrity:\n%s", got.Body)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "bzlhub/auto" {
		t.Fatalf("Labels: %v", got.Labels)
	}

	// Branch landed on bare; the file is there.
	check := filepath.Join(t.TempDir(), "check")
	mustRun(t, "", "git", "clone", "--quiet", "--branch="+wantBranch, bare, check)
	if _, err := os.Stat(filepath.Join(check, "modules", "foo", "1.0.0", "source.json")); err != nil {
		t.Fatalf("source.json missing on remote branch: %v", err)
	}
	// Main branch does NOT contain the new module (PR-only flow).
	mainCheck := filepath.Join(t.TempDir(), "main-check")
	mustRun(t, "", "git", "clone", "--quiet", "--branch=main", bare, mainCheck)
	if _, err := os.Stat(filepath.Join(mainCheck, "modules", "foo")); !os.IsNotExist(err) {
		t.Fatalf("main should not contain foo: err=%v", err)
	}
}

func TestGitPR_VariantImmutability(t *testing.T) {
	workTree, _ := setupBareRepo(t)
	ff := newFakeForge()

	// Pre-seed main with modules/foo/1.0.0/ so the immutability gate fires.
	dir := filepath.Join(workTree, "modules", "foo", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stage + commit + push so it lands on origin/main; the publisher
	// will hard-reset to that tip on its sync step.
	mustRun(t, workTree, "git", "-c", "user.email=seed@example.com", "-c", "user.name=seed",
		"add", "modules/foo/1.0.0")
	mustRun(t, workTree, "git", "-c", "user.email=seed@example.com", "-c", "user.name=seed",
		"commit", "--quiet", "-m", "seed foo@1.0.0")
	mustRun(t, workTree, "git", "push", "--quiet", "origin", "main")

	pub, err := NewGitPR(GitPRConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
		Repo:        bigorna.Repo{Owner: "o", Name: "r"},
		Forge:       ff,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = pub.Publish(context.Background(), PublishRequest{
		Module:     "foo",
		Version:    "1.0.0",
		SourceJSON: []byte(`{}`),
		Blob:       BlobRef{Integrity: "sha256-aa"},
		Requester:  Identity{Name: "Alice", Email: "alice@example.com"},
	})
	if err == nil {
		t.Fatalf("expected variant-immutability error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("wrong error: %v", err)
	}
	if len(ff.opened) != 0 {
		t.Fatalf("OpenPR should not have been called: %d", len(ff.opened))
	}
}

func TestGitPR_MissingForge(t *testing.T) {
	workTree, _ := setupBareRepo(t)
	_, err := NewGitPR(GitPRConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
		Repo:        bigorna.Repo{Owner: "o", Name: "r"},
		// Forge nil.
	})
	if err == nil {
		t.Fatal("expected error for missing Forge")
	}
}

func TestGitPR_MissingRepo(t *testing.T) {
	workTree, _ := setupBareRepo(t)
	_, err := NewGitPR(GitPRConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
		Forge:       newFakeForge(),
		// Repo zero.
	})
	if err == nil {
		t.Fatal("expected error for missing Repo")
	}
}

func TestGitPR_MissingRequester(t *testing.T) {
	workTree, _ := setupBareRepo(t)
	pub, err := NewGitPR(GitPRConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
		Repo:        bigorna.Repo{Owner: "o", Name: "r"},
		Forge:       newFakeForge(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pub.Publish(context.Background(), PublishRequest{
		Module:     "foo",
		Version:    "1.0.0",
		SourceJSON: []byte(`{}`),
		Blob:       BlobRef{Integrity: "sha256-aa"},
		// No Requester.
	})
	if err == nil {
		t.Fatal("expected error for missing requester")
	}
	if !errors.Is(err, ErrMissingRequiredField) {
		t.Fatalf("not ErrMissingRequiredField: %v", err)
	}
}

func TestGitPR_ForgeOpenPRFails(t *testing.T) {
	workTree, bare := setupBareRepo(t)
	ff := newFakeForge()
	ff.openErr = errors.New("simulated forge outage")

	pub, err := NewGitPR(GitPRConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
		Repo:        bigorna.Repo{Owner: "o", Name: "r"},
		Forge:       ff,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = pub.Publish(context.Background(), PublishRequest{
		Module:     "foo",
		Version:    "1.0.0",
		SourceJSON: []byte(`{}`),
		Blob:       BlobRef{Integrity: "sha256-aa"},
		Requester:  Identity{Name: "Alice", Email: "alice@example.com"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "simulated forge outage") {
		t.Fatalf("error doesn't surface forge cause: %v", err)
	}
	// Branch is still pushed (no auto-cleanup on partial failure).
	cmd := exec.Command("git", "ls-remote", "--heads", bare, "bzlhub/add-foo-1.0.0")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Fatalf("branch should be pushed even when OpenPR fails")
	}
}

func TestBranchName(t *testing.T) {
	cases := []struct {
		action, module, version, want string
	}{
		{"add", "foo", "1.0.0", "bzlhub/add-foo-1.0.0"},
		{"yank", "bazel_skylib", "1.7.1", "bzlhub/yank-bazel_skylib-1.7.1"},
		{"auto-bump", "rules_go", "0.48.0", "bzlhub/auto-bump-rules_go-0.48.0"},
		{"add", "weird:name", "1.0 0", "bzlhub/add-weird-name-1.0-0"},
	}
	for _, tc := range cases {
		got := BranchName(tc.action, tc.module, tc.version)
		if got != tc.want {
			t.Errorf("BranchName(%q,%q,%q) = %q, want %q",
				tc.action, tc.module, tc.version, got, tc.want)
		}
	}
}

func TestEnsureGitignoreBlobs(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGitignoreBlobs(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(b), "blobs/") {
		t.Fatalf(".gitignore lacks blobs/: %s", b)
	}
	// Second call is idempotent — no duplicate entry.
	if err := ensureGitignoreBlobs(dir); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(b2), "blobs/") != 1 {
		t.Fatalf("blobs/ duplicated: %s", b2)
	}
}

func TestBuildPRBody_HandlesEmptySHA(t *testing.T) {
	// buildPRBody is called with a SHA after `git commit`, but on
	// constructor paths or future callers it may be empty. The output
	// must still mention module@version so the title-vs-body relation
	// reads correctly in the forge UI.
	body := buildPRBody(PublishRequest{
		Module: "foo", Version: "1.0.0",
		Requester: Identity{Name: "A", Email: "a@x"},
	}, "")
	if !strings.Contains(body, "foo@1.0.0") {
		t.Fatalf("body lacks module@version: %s", body)
	}
}
