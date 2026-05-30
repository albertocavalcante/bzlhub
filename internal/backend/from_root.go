package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
)

// NewFromRoot constructs the right Backend for an on-disk BCR root
// based on what it finds there:
//
//   - <root>/.git exists → BCRMirror (the operator cloned the
//     registry; canopy gets drift-aware reads + a managed lifecycle
//     for the sync runner).
//   - <root> exists but isn't a git repo → File (the operator
//     hand-assembled a BCR tree; the simplest backend).
//   - <root> doesn't exist → error (operator misconfiguration; a
//     silent fallback would manifest later as every read 404-ing).
//
// Auto-detection (rather than an explicit flag) keeps the migration
// path zero-friction: an operator who clones the registry through
// PR8's sync bootstrap and points serve at the resulting directory
// gets the upgraded backend transparently. Operators who already
// pointed at a plain dir keep their existing behaviour.
//
// On the BCRMirror branch, the Mirror is Open()ed before return;
// callers receive a ready-to-use Backend.
func NewFromRoot(ctx context.Context, root string) (Backend, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("backend.NewFromRoot: %s: %w", root, err)
		}
		return nil, fmt.Errorf("backend.NewFromRoot: stat %s: %w", root, err)
	}

	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		m := bcrmirror.New(root, "")
		if err := m.Open(ctx); err != nil {
			return nil, fmt.Errorf("backend.NewFromRoot: open mirror at %s: %w", root, err)
		}
		return NewBCRMirror(m), nil
	}

	return NewFile(root), nil
}
