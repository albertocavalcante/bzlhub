package publish

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/version"
)

// Tiny subprocess wrappers for `git` shared by GitDirectPublisher and
// GitPRPublisher. We shell out (no go-git) for simplicity and lockfile
// compatibility — see docs/plans/10-git-backend/05-github-impl.md.

// runGit runs `git <args...>` inside workTree, suppressing stdout
// and wrapping stderr into the returned error.
func runGit(ctx context.Context, workTree string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workTree
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// gitOutput runs `git <args...>` inside workTree and returns stdout
// (with trailing whitespace preserved — caller trims if needed).
func gitOutput(ctx context.Context, workTree string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workTree
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// commitWith builds a commit message from req, writes it to a tempfile
// outside the worktree, and runs `git commit` with the bot as
// Committer (-c user.name/email) and req.Requester as Author
// (--author). Returns the new HEAD SHA on success.
func commitWith(ctx context.Context, workTree string, bot Identity, req PublishRequest) (string, error) {
	msgFile, err := writeTempFile("", "bzlhub-commit-msg-*", []byte(buildCommitMessage(req)))
	if err != nil {
		return "", err
	}
	defer os.Remove(msgFile)

	args := []string{
		"-c", "user.name=" + bot.Name,
		"-c", "user.email=" + bot.Email,
		"commit",
		"--quiet",
		"--author=" + req.Requester.String(),
		"--file=" + msgFile,
	}
	if err := runGit(ctx, workTree, args...); err != nil {
		return "", err
	}
	sha, err := gitOutput(ctx, workTree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// buildCommitMessage produces the conventional-commits subject +
// provenance trailers used by every bzlhub-mediated commit.
func buildCommitMessage(req PublishRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "feat(%s): add version %s\n\n", req.Module, req.Version)
	if req.SourceURL != "" {
		fmt.Fprintf(&b, "Source: %s\n", req.SourceURL)
	}
	if req.Blob.Integrity != "" {
		fmt.Fprintf(&b, "Integrity: %s\n", req.Blob.Integrity)
	}
	if req.Blob.Bytes > 0 {
		fmt.Fprintf(&b, "Size: %d bytes\n", req.Blob.Bytes)
	}
	// Blank line before the trailer block so git's interpret-trailers
	// recognizes it as trailers, not as message body.
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Requested-by: %s\n", req.Requester.String())
	fmt.Fprintf(&b, "Published-via: bzlhub %s\n", version.Version)
	fmt.Fprintf(&b, "Resolved-at: %s\n", time.Now().UTC().Format(time.RFC3339))
	return b.String()
}

// writeTempFile creates dir/<pattern> via os.CreateTemp, writes content,
// closes, and returns the final path. Empty dir means os.TempDir.
func writeTempFile(dir, pattern string, content []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// shortSHA truncates a full SHA-1 for human-readable receipts.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// gitignoreHasEntry checks whether the given .gitignore content
// contains the entry as a real (non-comment, non-blank) line.
func gitignoreHasEntry(content []byte, entry string) bool {
	for line := range bytes.SplitSeq(content, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		if string(trimmed) == entry {
			return true
		}
	}
	return false
}
