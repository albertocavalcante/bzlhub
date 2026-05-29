package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GitDirectPublisher commits module-version writes into a local git
// working tree and pushes them to the configured remote branch. It
// is the simplest git-backed strategy: no PR, no forge API — just
// `git add` → `git commit` → `git push`. Suitable for dev environments
// and single-maintainer self-hosts where PR ceremony is overhead.
//
// The worktree must already exist with .git initialized and a remote
// configured. Bootstrap (clone, initial checkout) is the operator's
// responsibility.
//
// File writes happen via an embedded FilesystemPublisher; the on-disk
// shape is byte-for-byte identical to what `ingest --mirror-to`
// produces. Blobs go to <worktree>/blobs/ but are excluded from the
// commit via .gitignore — they're canopy's local serving cache, not
// part of the BCR shape.
type GitDirectPublisher struct {
	workTree string
	branch   string
	remote   string
	bot      Identity
	fs       *FilesystemPublisher

	// mu serializes Publish. Each publish is a self-contained
	// fetch → write → commit → push sequence; concurrent runs on
	// the same worktree would interleave git operations.
	mu sync.Mutex
}

// GitDirectConfig configures a GitDirectPublisher.
type GitDirectConfig struct {
	// WorkTree is the path to a git working clone of the registry repo.
	// Must already exist with .git/ and a configured remote.
	WorkTree string

	// Branch is the branch publishes target. Default "main".
	Branch string

	// Remote is the git remote name. Default "origin".
	Remote string

	// BotIdentity is the committer identity. Author comes from each
	// PublishRequest.Requester. Required.
	BotIdentity Identity
}

// NewGitDirect constructs a publisher rooted at cfg.WorkTree. The
// worktree must already be a git clone of the registry repo with the
// configured remote present.
func NewGitDirect(cfg GitDirectConfig) (*GitDirectPublisher, error) {
	if cfg.WorkTree == "" {
		return nil, errors.New("publish: GitDirect requires WorkTree")
	}
	if cfg.BotIdentity.IsZero() {
		return nil, errors.New("publish: GitDirect requires BotIdentity")
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.Remote == "" {
		cfg.Remote = "origin"
	}
	if st, err := os.Stat(filepath.Join(cfg.WorkTree, ".git")); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("publish: %s is not a git working tree", cfg.WorkTree)
	}
	fs, err := NewFilesystem(cfg.WorkTree)
	if err != nil {
		return nil, err
	}
	p := &GitDirectPublisher{
		workTree: cfg.WorkTree,
		branch:   cfg.Branch,
		remote:   cfg.Remote,
		bot:      cfg.BotIdentity,
		fs:       fs,
	}
	if err := ensureGitignoreBlobs(cfg.WorkTree); err != nil {
		return nil, fmt.Errorf("ensure .gitignore: %w", err)
	}
	return p, nil
}

func (p *GitDirectPublisher) BeginBlob(ctx context.Context, srcURL string) (BlobSink, error) {
	return p.fs.BeginBlob(ctx, srcURL)
}

// Publish materializes the request into the worktree, commits with the
// requester as Author and the bot as Committer, and pushes to the
// remote branch.
//
// On a non-fast-forward push, Publish performs one rebase-and-retry:
// re-fetches origin/<branch>, hard-resets the worktree to its tip,
// re-applies the file writes (idempotent), and re-commits. This handles
// the common case of a sibling commit landing between fetch and push
// without requiring caller intervention.
func (p *GitDirectPublisher) Publish(ctx context.Context, req PublishRequest) (Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Pre-validate before any network I/O so a malformed request fails
	// fast without a wasted fetch/reset round trip.
	if err := ValidateRequest(req); err != nil {
		return Receipt{}, err
	}
	if req.Requester.IsZero() {
		return Receipt{}, fmt.Errorf("%w: requester", ErrMissingRequiredField)
	}

	for attempt := range 2 {
		if err := p.syncToRemoteTip(ctx); err != nil {
			return Receipt{}, fmt.Errorf("sync to remote tip: %w", err)
		}
		if _, err := p.fs.Publish(ctx, req); err != nil {
			return Receipt{}, err
		}
		if err := p.stage(ctx, req.Module); err != nil {
			return Receipt{}, fmt.Errorf("git add: %w", err)
		}
		sha, err := p.commit(ctx, req)
		if err != nil {
			return Receipt{}, fmt.Errorf("git commit: %w", err)
		}
		pushErr := p.push(ctx)
		if pushErr == nil {
			return Receipt{
				Strategy:    "git-direct",
				DiskPath:    filepath.Join(p.workTree, "modules", req.Module, req.Version),
				Commit:      sha,
				Diff:        fmt.Sprintf("git-direct: %s@%s → %s/%s @ %s", req.Module, req.Version, p.remote, p.branch, shortSHA(sha)),
				PublishedAt: time.Now().UTC(),
			}, nil
		}
		if attempt == 1 || !isNonFastForward(pushErr) {
			return Receipt{}, fmt.Errorf("git push: %w", pushErr)
		}
		// Attempt 0, non-FF: loop will re-sync and retry.
	}
	// Unreachable — both attempts either return success or fail.
	return Receipt{}, errors.New("publish: exhausted retries")
}

// syncToRemoteTip fetches the remote branch and hard-resets the
// worktree to its tip. Local changes are discarded — the worktree is
// canopy-owned, not human-edited.
func (p *GitDirectPublisher) syncToRemoteTip(ctx context.Context) error {
	if err := runGit(ctx, p.workTree, "fetch", "--quiet", p.remote, p.branch); err != nil {
		return err
	}
	return runGit(ctx, p.workTree, "reset", "--hard", "--quiet", p.remote+"/"+p.branch)
}

// stage runs `git add` on the paths that may have changed for this
// publish. Explicit paths (not `git add -A`) so leftover untracked
// state from an earlier crash can't accidentally land in the commit.
// .gitignore is always present because the constructor creates it.
func (p *GitDirectPublisher) stage(ctx context.Context, module string) error {
	return runGit(ctx, p.workTree, "add", "--",
		"bazel_registry.json",
		filepath.Join("modules", module),
		".gitignore",
	)
}

func (p *GitDirectPublisher) commit(ctx context.Context, req PublishRequest) (string, error) {
	return commitWith(ctx, p.workTree, p.bot, req)
}

func (p *GitDirectPublisher) push(ctx context.Context) error {
	return runGit(ctx, p.workTree, "push", "--quiet", p.remote, p.branch)
}

// isNonFastForward inspects an error string for the patterns git
// emits when a push is rejected because the remote moved. Used only
// by GitDirectPublisher's push-rebase-retry; GitPRPublisher pushes
// to a fresh branch and doesn't race.
func isNonFastForward(err error) bool {
	s := err.Error()
	return strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "Updates were rejected") ||
		(strings.Contains(s, "rejected") && strings.Contains(s, "fetch first"))
}

// Compile-time check.
var _ Publisher = (*GitDirectPublisher)(nil)
