package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
)

// BCRMirror is a Backend backed by a git-aware BCR clone via the
// go-bcr-mirror library. BCR-shape reads delegate to bcrmirror
// (which enforces name validation as a path-traversal boundary);
// files outside that shape (bazel_registry.json, blobs/, overlay/)
// read straight from Mirror.Path. bcrmirror's read-side errors
// translate to backend.ErrNotFound so handlers render 404, never
// 5xx. The caller supplies an already-Open'd Mirror.
type BCRMirror struct {
	mirror *bcrmirror.Mirror
}

// Mirror exposes the underlying bcrmirror.Mirror.
func (b *BCRMirror) Mirror() *bcrmirror.Mirror { return b.mirror }

// NewBCRMirror constructs a Backend wrapping an already-opened
// Mirror.
func NewBCRMirror(m *bcrmirror.Mirror) *BCRMirror { return &BCRMirror{mirror: m} }

func (b *BCRMirror) GetBazelRegistryJSON(_ context.Context) (io.ReadCloser, error) {
	return openFile(filepath.Join(b.mirror.Path, "bazel_registry.json"))
}

func (b *BCRMirror) GetMetadata(ctx context.Context, module string) (io.ReadCloser, error) {
	data, err := b.mirror.ReadModuleMetadata(ctx, module)
	if err != nil {
		return nil, translateBCRMirrorErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *BCRMirror) GetModuleBazel(ctx context.Context, module, version string) (io.ReadCloser, error) {
	data, err := b.mirror.ReadModuleBazel(ctx, module, version)
	if err != nil {
		return nil, translateBCRMirrorErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *BCRMirror) GetSourceJSON(ctx context.Context, module, version string) (io.ReadCloser, error) {
	data, err := b.mirror.ReadSourceJSON(ctx, module, version)
	if err != nil {
		return nil, translateBCRMirrorErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *BCRMirror) GetPatch(ctx context.Context, module, version, filename string) (io.ReadCloser, error) {
	data, err := b.mirror.ReadPatch(ctx, module, version, filename)
	if err != nil {
		return nil, translateBCRMirrorErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *BCRMirror) GetOverlay(_ context.Context, module, version, path string) (io.ReadCloser, error) {
	if err := validateBCRSegment(module); err != nil {
		return nil, ErrNotFound
	}
	if err := validateBCRSegment(version); err != nil {
		return nil, ErrNotFound
	}
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return nil, ErrNotFound
	}
	return openFile(filepath.Join(b.mirror.Path, "modules", module, version, "overlay", clean))
}

func (b *BCRMirror) GetBlob(_ context.Context, key string) (io.ReadCloser, error) {
	if strings.ContainsAny(key, "/\\") {
		return nil, ErrNotFound
	}
	return openFile(filepath.Join(b.mirror.Path, "blobs", key))
}

// translateBCRMirrorErr maps the library's read-side sentinels to
// backend.ErrNotFound; other errors pass through.
func translateBCRMirrorErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bcrmirror.ErrModuleNotFound),
		errors.Is(err, bcrmirror.ErrVersionNotFound),
		errors.Is(err, bcrmirror.ErrPatchNotFound),
		errors.Is(err, bcrmirror.ErrInvalidName),
		errors.Is(err, bcrmirror.ErrNoMirror):
		return ErrNotFound
	default:
		return err
	}
}

// validateBCRSegment mirrors the rejection rules bcrmirror's own
// validateNameSegment applies — empty, leading dot, path separator,
// ".." sequence, NUL byte. The library doesn't export the validator,
// and the overlay path doesn't go through bcrmirror's read API, so
// the checks live here too.
func validateBCRSegment(s string) error {
	if s == "" || strings.HasPrefix(s, ".") {
		return errors.New("invalid segment")
	}
	if strings.ContainsAny(s, `/\`) || strings.Contains(s, "..") {
		return errors.New("invalid segment")
	}
	if strings.ContainsRune(s, '\x00') {
		return errors.New("invalid segment")
	}
	return nil
}

func openFile(path string) (io.ReadCloser, error) {
	r, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return r, err
}
