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

	"github.com/albertocavalcante/bigorna"
)

// GitPRPublisher commits module-version writes into a feature branch
// on a local git working tree, pushes the branch to the configured
// remote, and opens a pull request via Forge. The default base
// branch is "main"; the feature branch name follows the convention
// `canopy/add-<module>-<version>`.
//
// The worktree must already exist with .git initialized and a remote
// configured. As with GitDirectPublisher, the worktree is canopy-owned
// — local changes are discarded on each publish.
type GitPRPublisher struct {
	workTree   string
	baseBranch string
	remote     string
	bot        Identity
	repo       bigorna.Repo
	forge      bigorna.Forge
	fs         *FilesystemPublisher
	labels     []string

	// mu serializes Publish. The worktree is mutated branch-by-branch
	// and only one publish can be in-flight at a time.
	mu sync.Mutex
}

// GitPRConfig configures a GitPRPublisher.
type GitPRConfig struct {
	// WorkTree is the path to a git working clone of the registry repo.
	// Must already exist with .git/ and the configured remote present.
	WorkTree string

	// BaseBranch is the PR target. Default "main".
	BaseBranch string

	// Remote is the git remote name. Default "origin".
	Remote string

	// BotIdentity is the commit Committer. Author is the requester from
	// each PublishRequest. Required.
	BotIdentity Identity

	// Repo identifies the registry repository on the forge. Required.
	Repo bigorna.Repo

	// Forge is the API surface used to OpenPR. Required.
	Forge bigorna.Forge

	// Labels are applied to every opened PR (e.g., {"canopy/auto"}).
	// The forge impl is responsible for translating these to its
	// native marker — branch-prefix on Bitbucket DC, real labels on
	// GitHub.
	Labels []string
}

// NewGitPR constructs a publisher. The worktree must be a git clone
// of the registry repo with the configured remote configured.
func NewGitPR(cfg GitPRConfig) (*GitPRPublisher, error) {
	if cfg.WorkTree == "" {
		return nil, errors.New("publish: GitPR requires WorkTree")
	}
	if cfg.BotIdentity.IsZero() {
		return nil, errors.New("publish: GitPR requires BotIdentity")
	}
	if cfg.Repo.Owner == "" || cfg.Repo.Name == "" {
		return nil, errors.New("publish: GitPR requires Repo")
	}
	if cfg.Forge == nil {
		return nil, errors.New("publish: GitPR requires Forge")
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
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
	p := &GitPRPublisher{
		workTree:   cfg.WorkTree,
		baseBranch: cfg.BaseBranch,
		remote:     cfg.Remote,
		bot:        cfg.BotIdentity,
		repo:       cfg.Repo,
		forge:      cfg.Forge,
		fs:         fs,
		labels:     append([]string(nil), cfg.Labels...),
	}
	if err := ensureGitignoreBlobs(cfg.WorkTree); err != nil {
		return nil, fmt.Errorf("ensure .gitignore: %w", err)
	}
	return p, nil
}

func (p *GitPRPublisher) BeginBlob(ctx context.Context, srcURL string) (BlobSink, error) {
	return p.fs.BeginBlob(ctx, srcURL)
}

// Publish materializes the request on a fresh feature branch, pushes,
// and opens a PR via the Forge. Pre-flight gates enforce variant
// immutability — refuses if `modules/<module>/<version>/` already
// exists on the base branch HEAD.
func (p *GitPRPublisher) Publish(ctx context.Context, req PublishRequest) (Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := ValidateRequest(req); err != nil {
		return Receipt{}, err
	}
	if req.Requester.IsZero() {
		return Receipt{}, fmt.Errorf("%w: requester", ErrMissingRequiredField)
	}

	if err := p.syncToBase(ctx); err != nil {
		return Receipt{}, fmt.Errorf("sync to base: %w", err)
	}
	if exists, err := p.versionDirExists(req.Module, req.Version); err != nil {
		return Receipt{}, err
	} else if exists {
		return Receipt{}, fmt.Errorf("publish: %s@%s already exists on %s (variant immutability)",
			req.Module, req.Version, p.baseBranch)
	}

	branch := BranchName("add", req.Module, req.Version)
	if err := p.createBranch(ctx, branch); err != nil {
		return Receipt{}, fmt.Errorf("create branch %s: %w", branch, err)
	}
	if _, err := p.fs.Publish(ctx, req); err != nil {
		return Receipt{}, err
	}
	if err := p.stage(ctx, req.Module); err != nil {
		return Receipt{}, fmt.Errorf("git add: %w", err)
	}
	sha, err := commitWith(ctx, p.workTree, p.bot, req)
	if err != nil {
		return Receipt{}, fmt.Errorf("git commit: %w", err)
	}
	if err := runGit(ctx, p.workTree, "push", "--quiet", "-u", p.remote, branch); err != nil {
		return Receipt{}, fmt.Errorf("git push: %w", err)
	}

	pr, err := p.forge.OpenPR(ctx, bigorna.OpenPROpts{
		Repo:       p.repo,
		Title:      fmt.Sprintf("Add %s@%s", req.Module, req.Version),
		Body:       buildPRBody(req, sha),
		HeadBranch: branch,
		BaseBranch: p.baseBranch,
		Labels:     p.labels,
	})
	if err != nil {
		// The branch is pushed; opening the PR is what failed. Caller
		// can retry or open the PR manually. We don't auto-delete the
		// pushed branch — destructive cleanup on partial failure is
		// out of scope; this leaves recovery in the operator's hands.
		return Receipt{}, fmt.Errorf("open pr: %w", err)
	}
	return Receipt{
		Strategy:    "git-pr",
		DiskPath:    filepath.Join(p.workTree, "modules", req.Module, req.Version),
		Commit:      sha,
		PRURL:       pr.URL,
		PRNumber:    pr.Number,
		Diff:        fmt.Sprintf("git-pr: %s@%s → %s#%d (%s)", req.Module, req.Version, p.repo.Owner+"/"+p.repo.Name, pr.Number, shortSHA(sha)),
		PublishedAt: time.Now().UTC(),
	}, nil
}

// syncToBase fetches the base branch and hard-resets the worktree to
// its tip. Same contract as GitDirectPublisher.syncToRemoteTip.
func (p *GitPRPublisher) syncToBase(ctx context.Context) error {
	if err := runGit(ctx, p.workTree, "fetch", "--quiet", p.remote, p.baseBranch); err != nil {
		return err
	}
	return runGit(ctx, p.workTree, "reset", "--hard", "--quiet", p.remote+"/"+p.baseBranch)
}

// versionDirExists checks whether modules/<module>/<version>/ exists in
// the worktree (which is at base-branch HEAD after syncToBase).
func (p *GitPRPublisher) versionDirExists(module, version string) (bool, error) {
	path := filepath.Join(p.workTree, "modules", module, version)
	st, err := os.Stat(path)
	if err == nil {
		return st.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// createBranch creates a fresh local branch off the current HEAD. If
// a same-named branch exists locally, it's deleted first — the
// previous attempt is stale and we'd rather start clean.
func (p *GitPRPublisher) createBranch(ctx context.Context, branch string) error {
	// Best-effort delete; ignore errors (branch may not exist).
	_ = runGit(ctx, p.workTree, "branch", "-D", branch)
	return runGit(ctx, p.workTree, "checkout", "-q", "-b", branch)
}

// stage runs `git add` on the paths a publish may have touched. Same
// shape as GitDirectPublisher.stage — explicit paths, no `git add -A`,
// so leftover untracked debris from a crashed earlier run can't slip
// into the commit.
func (p *GitPRPublisher) stage(ctx context.Context, module string) error {
	return runGit(ctx, p.workTree, "add", "--",
		"bazel_registry.json",
		filepath.Join("modules", module),
		".gitignore",
	)
}

// BranchName builds the canopy branch convention. Action is "add",
// "yank", "auto-bump", or "request"; module + version are slug-safe.
//
// This convention is shared across forges. Bitbucket DC has no PR
// labels, so the branch prefix is its only marker for canopy-mediated
// PRs.
func BranchName(action, module, version string) string {
	slug := func(s string) string {
		// Module names use underscores; versions use dots; both are
		// already git-ref-safe. Just guard against accidental colon
		// or whitespace by replacing them.
		s = strings.ReplaceAll(s, ":", "-")
		s = strings.ReplaceAll(s, " ", "-")
		return s
	}
	return fmt.Sprintf("canopy/%s-%s-%s", action, slug(module), slug(version))
}

// buildPRBody is the minimal G3 PR body. Future phases (G5 surfaces
// + G7 PR-bot + annotations propagation) will replace this with the
// rich template embedded as an FS template; for now a few trailers in
// markdown are enough to demonstrate end-to-end.
func buildPRBody(req PublishRequest, headSHA string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Add `%s@%s`\n\n", req.Module, req.Version)
	fmt.Fprintf(&b, "Prepared by **canopy** at the request of **%s**.\n\n", req.Requester.String())
	if req.SourceURL != "" {
		fmt.Fprintf(&b, "- **Source:** %s\n", req.SourceURL)
	}
	if req.Blob.Integrity != "" {
		fmt.Fprintf(&b, "- **Integrity:** `%s`\n", req.Blob.Integrity)
	}
	if req.Blob.Bytes > 0 {
		fmt.Fprintf(&b, "- **Size:** %d bytes\n", req.Blob.Bytes)
	}
	fmt.Fprintf(&b, "- **Commit:** `%s`\n", shortSHA(headSHA))
	return b.String()
}

// ensureGitignoreBlobs ensures the worktree's .gitignore excludes
// blobs/ — canopy's local serving cache is not part of the BCR shape.
// Lives at package scope so both git publishers reuse it.
func ensureGitignoreBlobs(workTree string) error {
	path := filepath.Join(workTree, ".gitignore")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if gitignoreHasEntry(b, "blobs/") {
		return nil
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, []byte("# canopy: serving cache, not part of the BCR shape\nblobs/\n")...)
	return os.WriteFile(path, b, 0o644)
}

// Compile-time check.
var _ Publisher = (*GitPRPublisher)(nil)
