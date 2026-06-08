package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	bundle "github.com/albertocavalcante/go-bcr-bundle"
)

// Bundle is a Backend backed by a go-bcr-bundle archive. Designed
// for airgap deployments where canopy serves from a tar.gz
// bundle imported via canopy's `bundle import` flow rather than
// from a live HTTP store or mirror.
//
// The bundle library's read sentinels translate to backend.ErrNotFound
// for the not-found cases handlers map to HTTP 404. Streaming
// reads (GetBlob) pass through the library's io.ReadCloser
// directly so large source tarballs aren't buffered into memory.
//
// The caller supplies an already-opened *bundle.Bundle. The
// library's bundle.Open is the right place for integrity
// verification + (v0.2.0+) signature validation; this adapter
// trusts what it's given. canopy is responsible for the bundle's
// lifecycle — call b.Close() (which closes the underlying
// *bundle.Bundle and removes its extraction tempdir) when the
// canopy server shuts down.
type Bundle struct {
	b *bundle.Bundle
}

// NewBundle wraps an already-opened *bundle.Bundle. Returns the
// adapter; canopy must call Close() at shutdown to clean up the
// bundle's tempdir.
func NewBundle(b *bundle.Bundle) *Bundle { return &Bundle{b: b} }

// Inner exposes the underlying *bundle.Bundle for callers that
// need its API directly (Manifest, ExtractedDir for advanced
// http.FileServer-style serving, etc.).
func (a *Bundle) Inner() *bundle.Bundle { return a.b }

// Close releases the bundle's extraction tempdir. Safe to call
// multiple times.
func (a *Bundle) Close() error { return a.b.Close() }

func (a *Bundle) GetBazelRegistryJSON(ctx context.Context) (io.ReadCloser, error) {
	data, err := a.b.Read(ctx, "bazel_registry.json")
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *Bundle) GetMetadata(ctx context.Context, module string) (io.ReadCloser, error) {
	data, err := a.b.Read(ctx, path.Join("modules", module, "metadata.json"))
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *Bundle) GetModuleBazel(ctx context.Context, module, version string) (io.ReadCloser, error) {
	data, err := a.b.Read(ctx, path.Join("modules", module, version, "MODULE.bazel"))
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *Bundle) GetSourceJSON(ctx context.Context, module, version string) (io.ReadCloser, error) {
	data, err := a.b.Read(ctx, path.Join("modules", module, version, "source.json"))
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *Bundle) GetPatch(ctx context.Context, module, version, filename string) (io.ReadCloser, error) {
	data, err := a.b.Read(ctx, path.Join("modules", module, version, "patches", filename))
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *Bundle) GetOverlay(ctx context.Context, module, version, p string) (io.ReadCloser, error) {
	data, err := a.b.Read(ctx, path.Join("modules", module, version, "overlay", p))
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// GetBlob streams directly via the bundle library's ReadBlob.
// Large source tarballs flow through the io.ReadCloser without
// buffering; caller (handler) closes the reader.
//
// Blob keys are passed through verbatim. The library's
// ReadBlob defensively rejects keys containing path separators
// (ErrBlobNotFound) — the adapter trusts that defense.
func (a *Bundle) GetBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := a.b.ReadBlob(ctx, key)
	if err != nil {
		return nil, translateBundleErr(err)
	}
	return rc, nil
}

// translateBundleErr maps the library's read-side sentinels to
// backend.ErrNotFound. The library's path-generic ErrNotFound +
// content-addressable ErrBlobNotFound both surface as 404. Other
// errors (checksum mismatch, signature invalid, unsupported
// bundle, etc.) pass through — handlers may render those as 500
// since they indicate the bundle itself is corrupt or untrusted,
// not that the requested artifact is genuinely missing.
//
// Path-not-found inside the bundle uses the library's ErrNotFound;
// blob-key-not-found uses ErrBlobNotFound. Both translate to
// canopy's single ErrNotFound — the caller (HTTP handler) doesn't
// need to distinguish "no such module" from "no such blob" at the
// 404 level; the request path already disambiguates.
func translateBundleErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bundle.ErrNotFound),
		errors.Is(err, bundle.ErrBlobNotFound):
		return ErrNotFound
	default:
		return err
	}
}

// stripPathSeparators is a defensive helper for module + version
// path components — exposed here because canopy currently doesn't
// pre-validate module/version names everywhere. The bundle
// library's Read uses path.Join which collapses ../ safely against
// the extracted root, but a module name like "foo/bar" would land
// at modules/foo/bar/... and resolve as a sub-tree — operator-
// surprising. Reject early when we see embedded separators.
//
// Unused as of v0.0.1 — kept inline for the inevitable canopy-side
// hardening pass on module-name validation.
//
//nolint:unused // reserved for upcoming canopy module-name hardening
func stripPathSeparators(s string) (string, error) {
	if strings.ContainsAny(s, `/\`) {
		return "", fmt.Errorf("bundle adapter: name %q contains path separators", s)
	}
	return s, nil
}

// Compile-time guard: Bundle satisfies Backend.
var _ Backend = (*Bundle)(nil)
