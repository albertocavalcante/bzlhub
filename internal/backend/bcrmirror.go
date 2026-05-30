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
// go-bcr-mirror library. The library handles Clone, Sync, and
// drift-aware reads (LogChanges, MetadataAt); the adapter just
// re-shapes the read API into canopy's Backend contract.
//
// BCR-shape reads (Metadata, ModuleBazel, SourceJSON, Patch) are
// delegated to bcrmirror.Mirror, which enforces module/version/patch
// name validation as a load-bearing path-traversal boundary. Errors
// from bcrmirror — ErrModuleNotFound, ErrVersionNotFound,
// ErrPatchNotFound, ErrInvalidName — are translated to
// backend.ErrNotFound so HTTP handlers render 404 uniformly. Any 5xx
// surface would leak the rejection back to the caller as "something
// interesting happened here" and is therefore avoided.
//
// Files that sit outside the validated BCR-shape (bazel_registry.json
// at root, blobs/<key>, overlay paths) are read straight from
// Mirror.Path with the same byte-key + traversal hardening that the
// File backend uses, since bcrmirror's public API doesn't cover them.
//
// BCRMirror does NOT itself manage the Mirror lifecycle: the caller
// supplies an already-Open()ed Mirror. The canopy sync runner owns
// Clone + Sync; the HTTP server consumes reads.
type BCRMirror struct {
	mirror *bcrmirror.Mirror
}

// Mirror exposes the underlying bcrmirror.Mirror so callers (e.g.
// the canopy Service's drift backfill) can drive its drift-aware
// reads — LogChanges, MetadataAt — without dropping the Backend
// abstraction at the HTTP layer.
func (b *BCRMirror) Mirror() *bcrmirror.Mirror { return b.mirror }

// NewBCRMirror constructs a Backend wrapping an already-opened Mirror.
// Callers are expected to have called Open or Clone before handing
// the Mirror to NewBCRMirror; the read methods will surface
// bcrmirror.ErrNoMirror (translated to a wrapped ErrNotFound) if the
// Mirror is detached.
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
	// Overlay isn't part of bcrmirror's public read API (yet), but
	// it lives at a deterministic path under the working tree.
	// Apply the same traversal hardening File uses for overlay
	// paths, and validate the module/version segments via bcrmirror's
	// rules to keep the surface uniform.
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

// translateBCRMirrorErr maps the library's read-side sentinels into
// the backend contract. Anything that signals "not in the registry"
// (module/version/patch missing) OR "invalid name" (path-traversal
// attempt) becomes ErrNotFound. Other errors pass through with the
// original wrapping intact so server-side logs still see the root
// cause.
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

// validateBCRSegment is a thin shim that calls bcrmirror's validator
// via the cheapest public path: route through ReadModuleMetadata-like
// semantics. We can't reach the package-private validateNameSegment,
// so we replicate the same surface checks here for the overlay and
// any future non-API paths the adapter serves directly.
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
