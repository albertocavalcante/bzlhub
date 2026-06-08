package publish

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setupBareRepo creates a bare repo + a working clone of it. Returns
// (workTree, bareRepoPath). The clone starts with one empty commit on
// "main" so we can fetch/reset/push against a valid tip.
func setupBareRepo(t *testing.T) (workTree, bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare = filepath.Join(root, "registry.git")
	workTree = filepath.Join(root, "worktree")

	// Newer git defaults to a non-"main" initial branch on macOS in
	// some setups. Pin it via --initial-branch on both ends and via a
	// post-clone symbolic-ref to be portable across versions.
	mustRun(t, "", "git", "init", "--bare", "--initial-branch=main", bare)
	mustRun(t, "", "git", "clone", "--quiet", bare, workTree)
	mustRun(t, workTree, "git", "symbolic-ref", "HEAD", "refs/heads/main")
	mustRun(t, workTree, "git", "-c", "user.email=init@example.com", "-c", "user.name=init",
		"commit", "--quiet", "--allow-empty", "-m", "init")
	mustRun(t, workTree, "git", "push", "--quiet", "-u", "origin", "main")

	return workTree, bare
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func runOutput(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitDirect_RoundTrip(t *testing.T) {
	workTree, bare := setupBareRepo(t)

	pub, err := NewGitDirect(GitDirectConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bzlhub-bot", Email: "bzlhub@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// .gitignore exists with blobs/ excluded.
	gi, err := os.ReadFile(filepath.Join(workTree, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gi, []byte("blobs/")) {
		t.Fatalf(".gitignore does not exclude blobs/: %s", gi)
	}

	ctx := context.Background()
	sink, err := pub.BeginBlob(ctx, "https://example.com/foo-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(sink, strings.NewReader("imagine a tarball")); err != nil {
		t.Fatal(err)
	}
	ref, err := sink.Close()
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := pub.Publish(ctx, PublishRequest{
		Module:      "foo",
		Version:     "1.0.0",
		SourceJSON:  []byte(`{"url":"https://example.com/foo-1.0.0.tar.gz","integrity":"` + ref.Integrity + `"}`),
		ModuleBazel: []byte("module(name=\"foo\", version=\"1.0.0\")\n"),
		Blob:        ref,
		SourceURL:   "https://example.com/foo-1.0.0.tar.gz",
		Requester:   Identity{Name: "Alberto Cavalcante", Email: "alberto@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Strategy != "git-direct" {
		t.Fatalf("strategy: %q", receipt.Strategy)
	}
	if receipt.Commit == "" {
		t.Fatalf("commit sha empty")
	}

	// The bare repo's main now points at our commit; verify by cloning
	// fresh and inspecting.
	check := filepath.Join(t.TempDir(), "check")
	mustRun(t, "", "git", "clone", "--quiet", bare, check)

	// File landed.
	if _, err := os.Stat(filepath.Join(check, "modules", "foo", "1.0.0", "source.json")); err != nil {
		t.Fatalf("source.json missing in remote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(check, "modules", "foo", "1.0.0", "MODULE.bazel")); err != nil {
		t.Fatalf("MODULE.bazel missing in remote: %v", err)
	}
	// blobs/ is gitignored, so the fresh clone should NOT contain it.
	if _, err := os.Stat(filepath.Join(check, "blobs")); !os.IsNotExist(err) {
		t.Fatalf("blobs/ should not be in remote: err=%v", err)
	}

	// Verify author / committer split.
	author := runOutput(t, check, "git", "log", "-1", "--pretty=%an <%ae>", "HEAD")
	if author != "Alberto Cavalcante <alberto@example.com>" {
		t.Fatalf("author: %q", author)
	}
	committer := runOutput(t, check, "git", "log", "-1", "--pretty=%cn <%ce>", "HEAD")
	if committer != "bzlhub-bot <bzlhub@example.com>" {
		t.Fatalf("committer: %q", committer)
	}

	// Subject is conventional-commits style.
	subject := runOutput(t, check, "git", "log", "-1", "--pretty=%s", "HEAD")
	if subject != "feat(foo): add version 1.0.0" {
		t.Fatalf("subject: %q", subject)
	}

	// Trailers present and machine-parseable.
	body := runOutput(t, check, "git", "log", "-1", "--pretty=%b", "HEAD")
	for _, want := range []string{
		"Source: https://example.com/foo-1.0.0.tar.gz",
		"Integrity: " + ref.Integrity,
		"Requested-by: Alberto Cavalcante <alberto@example.com>",
		"Published-via: bzlhub ",
		"Resolved-at:",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("commit body missing %q\nbody:\n%s", want, body)
		}
	}
	trailerCmd := exec.Command("git", "interpret-trailers", "--parse")
	trailerCmd.Dir = check
	trailerCmd.Stdin = strings.NewReader(body)
	parsed, err := trailerCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("interpret-trailers: %v\n%s", err, parsed)
	}
	if !strings.Contains(string(parsed), "Requested-by: Alberto Cavalcante <alberto@example.com>") {
		t.Fatalf("trailers not parseable:\n%s", parsed)
	}
}

func TestGitDirect_MissingRequester(t *testing.T) {
	workTree, _ := setupBareRepo(t)
	pub, err := NewGitDirect(GitDirectConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
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
		t.Fatalf("expected error for missing requester")
	}
	if !strings.Contains(err.Error(), "requester") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestGitDirect_RejectsNonGitTree(t *testing.T) {
	_, err := NewGitDirect(GitDirectConfig{
		WorkTree:    t.TempDir(), // empty dir, no .git
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
	})
	if err == nil {
		t.Fatal("expected error for non-git tree")
	}
	if !strings.Contains(err.Error(), "not a git working tree") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestGitDirect_RejectsEmptyBotIdentity(t *testing.T) {
	workTree, _ := setupBareRepo(t)
	_, err := NewGitDirect(GitDirectConfig{
		WorkTree: workTree,
		// BotIdentity zero.
	})
	if err == nil {
		t.Fatal("expected error for empty bot identity")
	}
}

func TestGitDirect_DoublePublish(t *testing.T) {
	workTree, bare := setupBareRepo(t)
	pub, err := NewGitDirect(GitDirectConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	requester := Identity{Name: "Alice", Email: "alice@example.com"}

	for _, v := range []string{"1.0.0", "1.1.0"} {
		sink, _ := pub.BeginBlob(ctx, "https://example.com/foo-"+v+".tar.gz")
		_, _ = sink.Write([]byte("payload " + v))
		ref, _ := sink.Close()
		if _, err := pub.Publish(ctx, PublishRequest{
			Module:     "foo",
			Version:    v,
			SourceJSON: []byte(`{}`),
			Blob:       ref,
			Requester:  requester,
		}); err != nil {
			t.Fatalf("publish %s: %v", v, err)
		}
	}

	// Two commits land on the remote main.
	check := filepath.Join(t.TempDir(), "check")
	mustRun(t, "", "git", "clone", "--quiet", bare, check)
	count := runOutput(t, check, "git", "rev-list", "--count", "HEAD")
	if count != "3" { // init + two publishes
		t.Fatalf("commit count: %q (want 3)", count)
	}
}

func TestGitDirect_ConcurrentPublishesSerialize(t *testing.T) {
	// Two publishes scheduled concurrently must both land (one after
	// the other) without losing either commit. The per-publisher mutex
	// guarantees they don't interleave git operations.
	workTree, bare := setupBareRepo(t)
	pub, err := NewGitDirect(GitDirectConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	requester := Identity{Name: "Alice", Email: "alice@example.com"}

	publish := func(version string) error {
		sink, err := pub.BeginBlob(ctx, "https://example.com/foo-"+version+".tar.gz")
		if err != nil {
			return err
		}
		_, _ = sink.Write([]byte("v" + version))
		ref, _ := sink.Close()
		_, err = pub.Publish(ctx, PublishRequest{
			Module:     "foo",
			Version:    version,
			SourceJSON: []byte(`{}`),
			Blob:       ref,
			Requester:  requester,
		})
		return err
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		wg.Go(func() {
			errs <- publish(v)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent publish: %v", err)
		}
	}

	check := filepath.Join(t.TempDir(), "check")
	mustRun(t, "", "git", "clone", "--quiet", bare, check)
	count := runOutput(t, check, "git", "rev-list", "--count", "HEAD")
	if count != "3" {
		t.Fatalf("commit count: %q (want 3)", count)
	}
}

func TestGitDirect_StaleWorktreeRecovers(t *testing.T) {
	// Exercise the start-of-attempt sync: a sibling commit lands on the
	// remote BEFORE we start publishing. syncToRemoteTip must fast-
	// forward our stale worktree to the sibling tip; publish then
	// succeeds on the first attempt without needing a retry.
	//
	// The retry-after-push-fail path (sibling lands BETWEEN our fetch
	// and our push) is the same code with worse timing; it's covered
	// by inspection rather than a deterministic test — exercising it
	// requires racing two pushes, which is flaky in CI.
	workTree, bare := setupBareRepo(t)
	pub, err := NewGitDirect(GitDirectConfig{
		WorkTree:    workTree,
		BotIdentity: Identity{Name: "bot", Email: "bot@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a sibling commit on the remote via a separate clone.
	sibling := filepath.Join(t.TempDir(), "sibling")
	mustRun(t, "", "git", "clone", "--quiet", bare, sibling)
	mustRun(t, sibling, "git", "-c", "user.email=other@example.com", "-c", "user.name=other",
		"commit", "--quiet", "--allow-empty", "-m", "sibling commit")
	mustRun(t, sibling, "git", "push", "--quiet", "origin", "main")
	ctx := context.Background()
	sink, _ := pub.BeginBlob(ctx, "https://example.com/foo-1.0.0.tar.gz")
	_, _ = sink.Write([]byte("payload"))
	ref, _ := sink.Close()
	receipt, err := pub.Publish(ctx, PublishRequest{
		Module:     "foo",
		Version:    "1.0.0",
		SourceJSON: []byte(`{}`),
		Blob:       ref,
		Requester:  Identity{Name: "Alice", Email: "alice@example.com"},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if receipt.Commit == "" {
		t.Fatal("no commit returned")
	}

	check := filepath.Join(t.TempDir(), "check")
	mustRun(t, "", "git", "clone", "--quiet", bare, check)
	count := runOutput(t, check, "git", "rev-list", "--count", "HEAD")
	if count != "3" { // init + sibling + our publish
		t.Fatalf("commit count: %q (want 3)", count)
	}
}

func TestIsNonFastForward(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"git push: non-fast-forward update", true},
		{"git push: Updates were rejected because the tip of your current branch is behind", true},
		{"git push: failed to push some refs; rejected (fetch first)", true},
		{"git push: connection refused", false},
		{"git push: permission denied", false},
	}
	for _, tc := range cases {
		got := isNonFastForward(errString(tc.msg))
		if got != tc.want {
			t.Fatalf("%q: got %v, want %v", tc.msg, got, tc.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestGitignoreHasEntry(t *testing.T) {
	cases := []struct {
		content string
		entry   string
		want    bool
	}{
		{"", "blobs/", false},
		{"blobs/\n", "blobs/", true},
		{"# comment\nblobs/\n", "blobs/", true},
		{"node_modules/\nblobs/\nvendor/\n", "blobs/", true},
		{"foo\nbar\n", "blobs/", false},
		{"# blobs/\n", "blobs/", false}, // commented out is not an entry
		{"  blobs/  \n", "blobs/", true},
	}
	for _, tc := range cases {
		got := gitignoreHasEntry([]byte(tc.content), tc.entry)
		if got != tc.want {
			t.Fatalf("hasEntry(%q, %q): got %v, want %v", tc.content, tc.entry, got, tc.want)
		}
	}
}
