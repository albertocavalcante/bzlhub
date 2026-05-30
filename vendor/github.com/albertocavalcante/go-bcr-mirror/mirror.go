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

	// Read HEAD up-front so lastSHA reflects reality from Open
	// onward, not just after the first Sync. Failure to resolve
	// HEAD is non-fatal — a brand-new git init has no HEAD commit
	// yet, which is a legitimate state Open should accept.
	head, _ := readHEAD(repo)

	m.stateMu.Lock()
	m.repo = repo
	m.lastSHA = head
	m.stateMu.Unlock()
	return nil
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
