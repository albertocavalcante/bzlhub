package forgewatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/albertocavalcante/bigorna"
)

// FileStore persists State to a single JSON file on disk. Keyed by
// (repo, branch), so a single file can hold state for multiple
// (repo, branch) pairs — useful when a canopy instance watches more
// than one branch / repo over its lifetime, even though the typical
// deployment watches just one.
//
// Writes are atomic via temp-then-rename so a crash mid-write doesn't
// corrupt the file. Reads tolerate a missing file (cold-start
// scenario) by returning the zero State + nil error.
//
// The mutex is per-instance, so concurrent Watchers using the same
// FileStore serialize their writes. Different files (one per
// Watcher) avoid contention entirely.
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore returns a FileStore rooted at the given JSON file.
// The file is created on first Save; the parent directory must
// already exist or Save will fail.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// fileShape is the on-disk structure: a map keyed by "owner/name/branch".
type fileShape map[string]State

func fileKey(repo bigorna.Repo, branch string) string {
	return repo.Owner + "/" + repo.Name + "/" + branch
}

// Load reads State for (repo, branch). A missing file returns the
// zero State + nil error (cold start); a malformed file returns the
// parse error so the operator can investigate rather than silently
// resetting.
func (s *FileStore) Load(_ context.Context, repo bigorna.Repo, branch string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("filestore: read %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return State{}, nil
	}
	var m fileShape
	if err := json.Unmarshal(data, &m); err != nil {
		return State{}, fmt.Errorf("filestore: parse %s: %w", s.path, err)
	}
	return m[fileKey(repo, branch)], nil
}

// Save writes State for (repo, branch). The file is read-modify-
// written atomically so concurrent (different-key) writes don't lose
// each other.
func (s *FileStore) Save(_ context.Context, repo bigorna.Repo, branch string, st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Read existing — preserve sibling-key entries.
	m := fileShape{}
	if data, err := os.ReadFile(s.path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &m) // best-effort; corrupt file → overwrite
	}
	m[fileKey(repo, branch)] = st

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("filestore: marshal: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".forgewatch-*.tmp")
	if err != nil {
		return fmt.Errorf("filestore: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("filestore: write temp: %w", err)
	}
	// Sync before close+rename so a power loss between rename and
	// the next checkpoint can't leave the published path pointing
	// at zero bytes. Same durability contract as internal/mirror.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("filestore: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("filestore: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("filestore: rename: %w", err)
	}
	cleanup = false
	return nil
}
