package forgewatch

import (
	"context"
	"sync"

	"github.com/albertocavalcante/bigorna"
)

// MemoryStore is an in-memory State store. Suitable for tests and
// transient workflows where restart-resumption isn't needed.
//
// Safe for concurrent use across goroutines, though the Watcher
// itself never invokes Load/Save concurrently from a single Run.
type MemoryStore struct {
	mu sync.Mutex
	m  map[memKey]State
}

type memKey struct {
	owner, name, branch string
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: make(map[memKey]State)}
}

// Load returns the State for (repo, branch). Missing entries return
// the zero State + nil error — the zero State is the documented
// "cold start" signal to the Watcher.
func (s *MemoryStore) Load(_ context.Context, repo bigorna.Repo, branch string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[memKey{repo.Owner, repo.Name, branch}], nil
}

// Save replaces the State for (repo, branch).
func (s *MemoryStore) Save(_ context.Context, repo bigorna.Repo, branch string, st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[memKey{repo.Owner, repo.Name, branch}] = st
	return nil
}
