package bundle

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Source abstracts "where does the bundle's content come from?"
// Implementations: FilesystemSource (v0.0.1, in-tree),
// BackendSource (v0.1.x, will live in this package once
// httpstore.Backend-based bundling lands).
//
// The interface is intentionally narrow — two methods, no BCR-
// format knowledge. The writer pattern-matches the path list to
// build the manifest's modules + blobs sections.
type Source interface {
	// List returns all BCR-shape relative paths under the source
	// (bazel_registry.json + modules/* + blobs/*). Order
	// unspecified — the writer sorts internally.
	List(ctx context.Context) ([]string, error)

	// Open returns the bytes at relPath as a streaming reader.
	// Caller closes. Returns ErrNotFound when the path isn't
	// present.
	Open(ctx context.Context, relPath string) (io.ReadCloser, error)
}

// FilesystemSource implements Source against a directory tree
// laid out in BCR shape. Designed for HQ-side workflows where
// canopy has already synced a mirror to a local directory.
type FilesystemSource struct {
	root string
}

// Compile-time guard.
var _ Source = (*FilesystemSource)(nil)

// NewFilesystemSource validates the root and returns a Source.
// Returns ErrInvalidBundle if bazel_registry.json or modules/
// is missing (the directory isn't BCR-shape).
//
// root MUST be an absolute path. Relative paths are rejected to
// prevent surprising joins at Open time.
func NewFilesystemSource(root string) (*FilesystemSource, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: root is required", ErrInvalidBundle)
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: root %q must be an absolute path", ErrInvalidBundle, root)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: stat root %q: %v", ErrInvalidBundle, root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: root %q is not a directory", ErrInvalidBundle, root)
	}
	// Minimal BCR-shape check: bazel_registry.json + modules/
	// must exist. Without them, this isn't a BCR-shape directory
	// and bundling would produce an empty / invalid bundle.
	if _, err := os.Stat(filepath.Join(root, "bazel_registry.json")); err != nil {
		return nil, fmt.Errorf("%w: missing bazel_registry.json under %q: %v",
			ErrInvalidBundle, root, err)
	}
	if info, err := os.Stat(filepath.Join(root, "modules")); err != nil {
		return nil, fmt.Errorf("%w: missing modules/ under %q: %v",
			ErrInvalidBundle, root, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%w: modules/ under %q is not a directory",
			ErrInvalidBundle, root)
	}
	return &FilesystemSource{root: root}, nil
}

// Root returns the source's root directory.
func (s *FilesystemSource) Root() string { return s.root }

// List walks the source tree and returns every BCR-shape relative
// path: bazel_registry.json, every file under modules/, every
// file under blobs/. Hidden files (dotfiles) and non-regular
// files (symlinks, devices) are skipped.
func (s *FilesystemSource) List(ctx context.Context) ([]string, error) {
	var paths []string
	walker := func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Skip dirs themselves; we only emit file paths.
		if d.IsDir() {
			return nil
		}
		// Skip non-regular files (symlinks, sockets, devices) —
		// defense against operator footguns and supply-chain
		// surprises.
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.root, absPath)
		if err != nil {
			return err
		}
		// Normalise separators for cross-platform sanity (tar paths
		// are always /-separated regardless of the source FS).
		rel = filepath.ToSlash(rel)
		// Skip hidden dotfiles at any level — common case is OS
		// junk (.DS_Store) creeping into the source tree.
		if hasDotPrefixSegment(rel) {
			return nil
		}
		// Only include BCR-shape paths: bazel_registry.json + paths
		// under modules/ + paths under blobs/.
		if rel == "bazel_registry.json" ||
			strings.HasPrefix(rel, "modules/") ||
			strings.HasPrefix(rel, "blobs/") {
			paths = append(paths, rel)
		}
		return nil
	}
	if err := filepath.WalkDir(s.root, walker); err != nil {
		return nil, fmt.Errorf("bundle: walk source %q: %w", s.root, err)
	}
	return paths, nil
}

// Open returns the bytes at relPath. Returns ErrNotFound when
// the file is absent.
//
// relPath is interpreted as a /-separated path relative to the
// source root; the implementation joins it with filepath.Join
// (which collapses any ../ traversal attempts against root, but
// callers SHOULD validate relPath at their layer if they don't
// trust the source).
func (s *FilesystemSource) Open(ctx context.Context, relPath string) (io.ReadCloser, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Normalise to the OS's separator before joining.
	clean := filepath.FromSlash(relPath)
	f, err := os.Open(filepath.Join(s.root, clean))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, relPath)
		}
		return nil, fmt.Errorf("bundle: open %s: %w", relPath, err)
	}
	return f, nil
}

// hasDotPrefixSegment reports whether any segment of the
// /-separated path starts with a dot. Used to skip OS junk
// (.DS_Store, .git/, etc.) at any level of the source tree.
func hasDotPrefixSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
