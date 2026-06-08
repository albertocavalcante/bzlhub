package bcrmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
)

// Mirror represents an on-disk clone of a BCR-shape git repository.
// Constructed via New; populated via Clone (initial) or Open (attach
// to an existing clone).
//
// Mirror methods are safe for concurrent read calls. Write operations
// (Clone, Sync) are not concurrency-safe with themselves; the caller
// orchestrates.
type Mirror struct {
	// Path is the on-disk root of the clone. The directory must
	// be writable when Clone is called; Open requires it to
	// exist.
	Path string

	// Remote is the upstream git URL (https://github.com/... or
	// ssh://...). Empty string is allowed for Open on an
	// already-cloned mirror where the remote is set in the git
	// config.
	Remote string

	// unexported state; guarded by stateMu for the rare
	// concurrent inspection.
	stateMu  sync.RWMutex
	lastSync time.Time
	lastSHA  string

	// repo is the lazily-opened go-git repository handle.
	repo *git.Repository

	// lastSyncReadErr is the error from Open's LAST_SYNC parse, or
	// nil. Exposed via LastSyncReadErr so callers can log
	// hand-editing recovery without failing Open itself.
	lastSyncReadErr error

	// root is an os.Root rooted at Mirror.Path. All read-side
	// filesystem operations (read.go's metadata / source.json /
	// patch reads) go through it so a malicious upstream commit
	// can't trick the Mirror into following a symlink that
	// escapes the mirror directory. nil until Open succeeds.
	root *os.Root
}

// New constructs a Mirror bound to path and remote. Does not touch
// disk; Clone or Open does that.
//
// path is the on-disk root where the clone lives (or will live).
// remote is the upstream URL; can be empty when the caller intends to
// Open an already-cloned mirror with the remote already configured.
func New(path, remote string) *Mirror {
	return &Mirror{
		Path:   path,
		Remote: remote,
	}
}

// Open attaches to an existing on-disk clone at Mirror.Path.
//
// Returns ErrNoMirror when Path doesn't exist or doesn't contain a
// valid git repository. Returns a wrapped error when go-git's open
// fails for any other reason (corruption, permissions, etc.).
//
// When Mirror.Remote is non-empty, Open verifies that the on-disk
// clone's "origin" remote URL matches Mirror.Remote — protects
// against an operator silently swapping the upstream out from under
// canopy. Mismatch returns a wrapped error.
//
// Safe to call multiple times; subsequent calls re-attach to disk.
func (m *Mirror) Open(ctx context.Context) error {
	if m.Path == "" {
		return fmt.Errorf("bcrmirror.Open: empty Path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := os.Stat(m.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNoMirror, m.Path)
		}
		return fmt.Errorf("bcrmirror.Open: stat %s: %w", m.Path, err)
	}

	repo, err := git.PlainOpen(m.Path)
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return fmt.Errorf("%w: %s (not a git repository)", ErrNoMirror, m.Path)
		}
		return fmt.Errorf("bcrmirror.Open: open %s: %w", m.Path, err)
	}

	// Verify remote URL matches when Remote was supplied.
	if m.Remote != "" {
		if err := verifyRemoteURL(repo, m.Remote); err != nil {
			return err
		}
	}

	// HEAD failure is non-fatal — a fresh git init has none yet.
	head, _ := readHEAD(repo)
	persisted, syncErr := readLastSyncFile(m.Path)

	root, err := os.OpenRoot(m.Path)
	if err != nil {
		return fmt.Errorf("bcrmirror.Open: OpenRoot %s: %w", m.Path, err)
	}

	m.stateMu.Lock()
	if m.root != nil {
		_ = m.root.Close()
	}
	m.repo = repo
	m.lastSHA = head
	m.lastSync = persisted
	m.lastSyncReadErr = syncErr
	m.root = root
	m.stateMu.Unlock()
	return nil
}

// Close releases the underlying os.Root file descriptor. Optional
// — Mirror works without Close (the fd lives until process exit),
// but long-running daemons that open many Mirrors should call it.
func (m *Mirror) Close() error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.root == nil {
		return nil
	}
	err := m.root.Close()
	m.root = nil
	return err
}

// LastSyncReadErr returns the error from Open's LAST_SYNC parse, or
// nil. A non-nil value means an existing LAST_SYNC file was found
// but couldn't be decoded (truncated, hand-edited, wrong format) —
// Open still succeeded and lastSync was seeded to zero; the next
// Sync will overwrite the file. Callers SHOULD log a warning so
// the recovery isn't silent.
func (m *Mirror) LastSyncReadErr() error {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.lastSyncReadErr
}

// verifyRemoteURL inspects the "origin" remote on repo and confirms
// its first URL matches expected. Returns a wrapped error describing
// the mismatch otherwise.
func verifyRemoteURL(repo *git.Repository, expected string) error {
	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("bcrmirror.Open: read 'origin' remote: %w", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return fmt.Errorf("bcrmirror.Open: 'origin' has no URLs configured")
	}
	if urls[0] != expected {
		return fmt.Errorf(
			"bcrmirror.Open: 'origin' URL mismatch: on-disk %q, expected %q",
			urls[0], expected)
	}
	return nil
}

// LastSync returns the wall-clock time of this Mirror's most
// recent successful upstream contact (Clone or Sync, including
// up-to-date probes). Zero before any sync. Safe for concurrent
// reads.
func (m *Mirror) LastSync() time.Time {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.lastSync
}

// requireOpenRepo returns the go-git repo handle if Open has been
// called, otherwise an ErrNoMirror-wrapped error. Used by read +
// drift operations that require an opened mirror.
func (m *Mirror) requireOpenRepo() (*git.Repository, error) {
	m.stateMu.RLock()
	repo := m.repo
	m.stateMu.RUnlock()
	if repo == nil {
		return nil, fmt.Errorf("%w: Open must be called first (Mirror.Path=%s)", ErrNoMirror, m.Path)
	}
	return repo, nil
}
