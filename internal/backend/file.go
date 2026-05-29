package backend

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// File is a Backend that serves the BCR-shape layout directly from a
// filesystem root. The cheapest, most portable backend; the default for
// self-hosters who don't want to run anything else.
//
// Expected layout under Root:
//
//	bazel_registry.json
//	modules/
//	  <name>/
//	    metadata.json
//	    <version>/
//	      MODULE.bazel
//	      source.json
//	      patches/<filename>
//	      overlay/<path>
//	blobs/<key>     (optional; for tarballs canopy mirrors)
type File struct {
	Root string
}

// NewFile constructs a File backend rooted at the given directory.
func NewFile(root string) *File { return &File{Root: root} }

func (f *File) GetBazelRegistryJSON(_ context.Context) (io.ReadCloser, error) {
	return f.open("bazel_registry.json")
}

func (f *File) GetMetadata(_ context.Context, module string) (io.ReadCloser, error) {
	return f.open(filepath.Join("modules", module, "metadata.json"))
}

func (f *File) GetModuleBazel(_ context.Context, module, version string) (io.ReadCloser, error) {
	return f.open(filepath.Join("modules", module, version, "MODULE.bazel"))
}

func (f *File) GetSourceJSON(_ context.Context, module, version string) (io.ReadCloser, error) {
	return f.open(filepath.Join("modules", module, version, "source.json"))
}

func (f *File) GetPatch(_ context.Context, module, version, filename string) (io.ReadCloser, error) {
	if strings.ContainsAny(filename, "/\\") {
		return nil, ErrNotFound
	}
	return f.open(filepath.Join("modules", module, version, "patches", filename))
}

func (f *File) GetOverlay(_ context.Context, module, version, path string) (io.ReadCloser, error) {
	// Overlay paths may have subdirectories, but they must stay under
	// overlay/. Reject path traversal.
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return nil, ErrNotFound
	}
	return f.open(filepath.Join("modules", module, version, "overlay", clean))
}

func (f *File) GetBlob(_ context.Context, key string) (io.ReadCloser, error) {
	if strings.ContainsAny(key, "/\\") {
		return nil, ErrNotFound
	}
	return f.open(filepath.Join("blobs", key))
}

func (f *File) open(rel string) (io.ReadCloser, error) {
	r, err := os.Open(filepath.Join(f.Root, rel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return r, err
}
