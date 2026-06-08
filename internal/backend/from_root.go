package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
)

// NewFromRoot picks BCRMirror when <root>/.git is present, File
// otherwise. Returns an error when root doesn't exist — a silent
// fallback would manifest later as every read 404-ing. On the
// BCRMirror branch the Mirror is Open'd before return.
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
