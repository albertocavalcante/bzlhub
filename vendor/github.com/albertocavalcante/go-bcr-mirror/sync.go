package bcrmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// defaultCloneTimeout bounds Clone when CloneOptions.Timeout is zero.
// 30 minutes is the upper bound on a fresh BCR clone over a slow corp
// link (per the 2026-05-29 spike measuring 24s on home network).
const defaultCloneTimeout = 30 * time.Minute

// defaultSyncTimeout bounds Sync when SyncOptions.Timeout is zero.
const defaultSyncTimeout = 10 * time.Minute

// defaultBranch is what Clone uses when CloneOptions.Branch is empty.
const defaultBranch = "main"

// Clone performs the initial clone from Mirror.Remote into
// Mirror.Path.
//
// Idempotent: when Path already contains a valid clone of Remote,
// Clone returns ErrAlreadyCloned with the existing SHA carried in the
// receipt. Callers typically inspect the error type via errors.Is and
// proceed without treating it as a hard failure.
//
// On success, the Mirror is also attached to the freshly-cloned repo
// — no separate Open call is needed afterwards.
//
// Network bytes are an approximation derived from the resulting pack
// file size; go-git does not surface the exact transfer count.
func (m *Mirror) Clone(ctx context.Context, opts CloneOptions) (CloneReceipt, error) {
	var receipt CloneReceipt
	if m.Path == "" {
		return receipt, fmt.Errorf("bcrmirror.Clone: empty Path")
	}
	if m.Remote == "" {
		return receipt, fmt.Errorf("bcrmirror.Clone: empty Remote")
	}

	// Idempotent check: if Path is already a valid clone of Remote,
	// attach the Mirror to it and return ErrAlreadyCloned. The
	// Mirror is left in the same usable state as a fresh clone or
	// an explicit Open call, so callers can immediately use read
	// methods without an extra Open() round-trip.
	existingRepo, existingSHA, detectErr := m.detectExistingClone()
	if detectErr != nil {
		return receipt, detectErr
	}
	if existingRepo != nil {
		m.stateMu.Lock()
		m.repo = existingRepo
		m.lastSHA = existingSHA
		m.stateMu.Unlock()
		receipt.SHA = existingSHA
		return receipt, fmt.Errorf("%w: HEAD %s", ErrAlreadyCloned, existingSHA)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultCloneTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	branch := opts.Branch
	if branch == "" {
		branch = defaultBranch
	}

	cloneOpts := &git.CloneOptions{
		URL:           m.Remote,
		SingleBranch:  !opts.AllBranches,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		Depth:         opts.Depth,
	}

	start := time.Now()
	repo, err := git.PlainCloneContext(ctx, m.Path, false, cloneOpts)
	if err != nil {
		return receipt, fmt.Errorf("bcrmirror.Clone: git clone %s: %w", m.Remote, err)
	}

	headSHA, err := readHEAD(repo)
	if err != nil {
		return receipt, fmt.Errorf("bcrmirror.Clone: read HEAD post-clone: %w", err)
	}

	bytes, err := packfileBytes(m.Path)
	if err != nil {
		// Pack-file sizing is best-effort; clone succeeded, so the
		// missing byte count shouldn't fail the operation. Caller
		// gets duration + SHA without the bytes field.
		bytes = 0
	}

	receipt.SHA = headSHA
	receipt.Bytes = bytes
	receipt.Duration = time.Since(start)
	receipt.Sparse = len(opts.Sparse) > 0

	m.stateMu.Lock()
	m.repo = repo
	m.lastSync = time.Now().UTC()
	m.lastSHA = headSHA
	m.stateMu.Unlock()

	return receipt, nil
}

// Sync fetches updates from Mirror.Remote and fast-forwards Path's
// HEAD.
//
// On non-fast-forward (local diverged from remote), returns
// ErrNotFastForward and leaves the working tree untouched — the
// caller decides whether to force-pull via Force=true or surface
// the divergence to the operator.
//
// The receipt records the FromSHA, ToSHA, and commit count for the
// caller to feed an audit log. UpToDate=true when no new commits
// arrived.
//
// Mirror must be Open()ed (or freshly Clone()d) before Sync.
func (m *Mirror) Sync(ctx context.Context, opts SyncOptions) (SyncReceipt, error) {
	var receipt SyncReceipt
	repo, err := m.requireOpenRepo()
	if err != nil {
		return receipt, err
	}

	fromSHA, err := readHEAD(repo)
	if err != nil {
		return receipt, fmt.Errorf("bcrmirror.Sync: read HEAD pre-sync: %w", err)
	}
	receipt.FromSHA = fromSHA

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultSyncTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	wt, err := repo.Worktree()
	if err != nil {
		return receipt, fmt.Errorf("bcrmirror.Sync: worktree handle: %w", err)
	}

	start := time.Now()
	pullErr := wt.PullContext(ctx, &git.PullOptions{
		RemoteName: "origin",
		Force:      opts.Force,
	})

	switch {
	case pullErr == nil:
		// Pull succeeded with new commits.
	case errors.Is(pullErr, git.NoErrAlreadyUpToDate):
		// Nothing to do.
		receipt.ToSHA = fromSHA
		receipt.UpToDate = true
		receipt.Duration = time.Since(start)
		return receipt, nil
	case errors.Is(pullErr, git.ErrNonFastForwardUpdate):
		// go-git's PullOptions.Force only forces the fetch refspec
		// update; the worktree's own fast-forward check is
		// unconditional. To deliver Force=true's documented "reset
		// to remote" semantics we fetch + hard-reset by hand.
		if !opts.Force {
			return receipt, fmt.Errorf("%w (from=%s): set SyncOptions.Force=true to reset to remote", ErrNotFastForward, fromSHA)
		}
		if err := forceResetToRemote(ctx, repo, wt); err != nil {
			return receipt, fmt.Errorf("bcrmirror.Sync: force reset: %w", err)
		}
	default:
		return receipt, fmt.Errorf("bcrmirror.Sync: pull: %w", pullErr)
	}

	toSHA, err := readHEAD(repo)
	if err != nil {
		return receipt, fmt.Errorf("bcrmirror.Sync: read HEAD post-sync: %w", err)
	}

	commits, err := countCommitsBetween(repo, fromSHA, toSHA)
	if err != nil {
		// Don't fail the sync on a counting error — the advance
		// already landed. Caller gets the SHA delta + zero commit
		// count.
		commits = 0
	}

	receipt.ToSHA = toSHA
	receipt.Commits = commits
	receipt.Duration = time.Since(start)

	m.stateMu.Lock()
	m.lastSync = time.Now().UTC()
	m.lastSHA = toSHA
	m.stateMu.Unlock()

	return receipt, nil
}

// SnapshotSHA returns the working tree's current HEAD commit SHA.
// Cheap (one ref resolve); safe to call often.
//
// Returns ErrNoMirror when Open hasn't been called.
func (m *Mirror) SnapshotSHA(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := m.requireOpenRepo()
	if err != nil {
		return "", err
	}
	return readHEAD(repo)
}

// IsClean reports whether the working tree has no uncommitted
// changes. Callers can use this to gate Sync on a clean working
// tree before proceeding — a "false" return is the operator's
// signal that something is hand-editing the mirror.
//
// Returns ErrNoMirror when Open hasn't been called.
func (m *Mirror) IsClean(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	repo, err := m.requireOpenRepo()
	if err != nil {
		return false, err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("bcrmirror.IsClean: worktree: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("bcrmirror.IsClean: status: %w", err)
	}
	return status.IsClean(), nil
}

// detectExistingClone classifies the state of Mirror.Path for Clone's
// idempotent check.
//
//   - (repo, sha, nil) — Path holds a valid clone of Remote; Clone
//     attaches the Mirror to repo and returns ErrAlreadyCloned.
//   - (nil, "", nil) — Path doesn't exist or isn't a git repository;
//     Clone proceeds to clone normally.
//   - (nil, "", err) — Path holds something git-shaped that isn't
//     usable (corrupt repo, wrong remote URL, unreadable HEAD). The
//     error is surfaced so the operator sees the root cause instead
//     of a downstream "destination already exists" from
//     PlainCloneContext.
func (m *Mirror) detectExistingClone() (*git.Repository, string, error) {
	if _, err := os.Stat(m.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("bcrmirror.Clone: stat %s: %w", m.Path, err)
	}
	repo, err := git.PlainOpen(m.Path)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			// Path exists but isn't a git repo — let git.Clone
			// surface its own "destination already exists" error
			// if the dir is non-empty.
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("bcrmirror.Clone: open existing clone at %s: %w", m.Path, err)
	}
	if m.Remote != "" {
		if verifyErr := verifyRemoteURL(repo, m.Remote); verifyErr != nil {
			return nil, "", verifyErr
		}
	}
	sha, err := readHEAD(repo)
	if err != nil {
		return nil, "", fmt.Errorf("bcrmirror.Clone: read HEAD of existing clone: %w", err)
	}
	return repo, sha, nil
}

// forceResetToRemote implements "git reset --hard origin/<branch>"
// semantics for Sync(Force=true) when the local HEAD has diverged.
// The previous PullContext call already attempted a force-fetch under
// the hood, so the remote-tracking ref refs/remotes/origin/<branch>
// should already reflect the upstream tip; we just need to slam HEAD
// to it and reset the worktree.
//
// Caller has already validated repo + worktree handles.
func forceResetToRemote(ctx context.Context, repo *git.Repository, wt *git.Worktree) error {
	// Resolve the current branch name from HEAD's symbolic ref so
	// we know which remote-tracking ref to slam onto.
	headRef, err := repo.Head()
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	if !headRef.Name().IsBranch() {
		return fmt.Errorf("HEAD is not on a branch (detached at %s); refusing force-reset", headRef.Hash())
	}
	branchShort := headRef.Name().Short()
	remoteRefName := plumbing.NewRemoteReferenceName("origin", branchShort)

	// Refresh the remote-tracking ref. We don't strictly need this
	// (PullContext already fetched) but it costs little and makes
	// the helper safe to call independently.
	if err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Force:      true,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("refresh fetch: %w", err)
	}

	remoteRef, err := repo.Reference(remoteRefName, true)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", remoteRefName, err)
	}

	if err := wt.Reset(&git.ResetOptions{
		Mode:   git.HardReset,
		Commit: remoteRef.Hash(),
	}); err != nil {
		return fmt.Errorf("hard reset to %s: %w", remoteRef.Hash(), err)
	}
	return nil
}

// readHEAD returns the resolved HEAD commit SHA as a hex string.
func readHEAD(repo *git.Repository) (string, error) {
	ref, err := repo.Head()
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// countCommitsBetween returns the number of commits in the half-open
// range (fromSHA, toSHA]. Used by Sync to populate the receipt.
func countCommitsBetween(repo *git.Repository, fromSHA, toSHA string) (int, error) {
	if fromSHA == toSHA {
		return 0, nil
	}
	if fromSHA == "" {
		// First sync — no prior reference; count is unknown.
		return 0, nil
	}
	toHash := plumbing.NewHash(toSHA)
	fromHash := plumbing.NewHash(fromSHA)
	iter, err := repo.Log(&git.LogOptions{From: toHash})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	count := 0
	// Sentinel sentinel: pointer-equality compared below. go-git's
	// ForEach returns the callback's value verbatim; stopErr is a
	// fresh non-wrapped error. == is clearer + equivalent to
	// errors.Is here.
	stopErr := errors.New("stop")
	walkErr := iter.ForEach(func(c *object.Commit) error {
		if c.Hash == fromHash {
			return stopErr
		}
		count++
		return nil
	})
	if walkErr != nil && walkErr != stopErr { //nolint:errorlint // see comment above
		return 0, walkErr
	}
	return count, nil
}

// packfileBytes sums the on-disk sizes of files under
// <path>/.git/objects/pack/. Best-effort approximation of network
// transfer size; not exact.
func packfileBytes(path string) (int64, error) {
	packDir := filepath.Join(path, ".git", "objects", "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total, nil
}
