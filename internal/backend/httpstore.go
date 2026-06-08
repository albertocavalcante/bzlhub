package backend

import (
	"bytes"
	"context"
	"errors"
	"io"

	httpstore "github.com/albertocavalcante/go-bcr-httpstore"
)

// HTTPStore is a Backend backed by an HTTP-served BCR-shape tree
// via the go-bcr-httpstore library. Substrate-agnostic: nginx,
// S3/R2/MinIO/GCS, Artifactory generic repos, Forgejo raw,
// GitHub raw — anything that speaks HTTP and serves the BCR
// on-disk shape.
//
// httpstore's read-side error sentinels translate to
// backend.ErrNotFound so handlers render 404 consistently with
// other Backend impls. Streaming reads (GetBlob) pass through
// directly so large tarballs aren't buffered into memory.
//
// The caller supplies an already-constructed *httpstore.Backend;
// the library's NewOptions wiring (BaseURL, Auth, *http.Client,
// Layout) stays load-bearing and the adapter doesn't smuggle
// defaults past it.
type HTTPStore struct {
	store *httpstore.Backend
}

// Store exposes the underlying httpstore.Backend for callers that
// need its API directly (Layout introspection, AuthName for
// audit logs, etc.).
func (h *HTTPStore) Store() *httpstore.Backend { return h.store }

// NewHTTPStore constructs a Backend wrapping an already-built
// *httpstore.Backend. The library's New is the right place for
// validation; the adapter trusts what it's given.
func NewHTTPStore(s *httpstore.Backend) *HTTPStore { return &HTTPStore{store: s} }

func (h *HTTPStore) GetBazelRegistryJSON(ctx context.Context) (io.ReadCloser, error) {
	data, err := h.store.ReadBazelRegistryJSON(ctx)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (h *HTTPStore) GetMetadata(ctx context.Context, module string) (io.ReadCloser, error) {
	data, err := h.store.ReadMetadataJSON(ctx, module)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (h *HTTPStore) GetModuleBazel(ctx context.Context, module, version string) (io.ReadCloser, error) {
	data, err := h.store.ReadModuleBazel(ctx, module, version)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (h *HTTPStore) GetSourceJSON(ctx context.Context, module, version string) (io.ReadCloser, error) {
	data, err := h.store.ReadSourceJSON(ctx, module, version)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (h *HTTPStore) GetPatch(ctx context.Context, module, version, filename string) (io.ReadCloser, error) {
	data, err := h.store.ReadPatch(ctx, module, version, filename)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (h *HTTPStore) GetOverlay(ctx context.Context, module, version, path string) (io.ReadCloser, error) {
	data, err := h.store.ReadOverlay(ctx, module, version, path)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// GetBlob streams directly — no buffering. The httpstore.Backend
// returns an io.ReadCloser bound to the HTTP response body;
// the caller (handler) closes it.
func (h *HTTPStore) GetBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := h.store.ReadBlob(ctx, key)
	if err != nil {
		return nil, translateHTTPStoreErr(err)
	}
	return rc, nil
}

// translateHTTPStoreErr maps the library's read-side sentinels to
// backend.ErrNotFound for everything a Bazel-side 404 would cover;
// other errors (transport, 5xx, malformed config) pass through so
// handlers can render 502/503 appropriately.
func translateHTTPStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, httpstore.ErrModuleNotFound),
		errors.Is(err, httpstore.ErrVersionNotFound),
		errors.Is(err, httpstore.ErrPatchNotFound),
		errors.Is(err, httpstore.ErrOverlayNotFound),
		errors.Is(err, httpstore.ErrBlobNotFound),
		errors.Is(err, httpstore.ErrRegistryJSONNotFound),
		errors.Is(err, httpstore.ErrIndexUnreadable):
		return ErrNotFound
	default:
		return err
	}
}

// Compile-time guard: HTTPStore satisfies Backend.
var _ Backend = (*HTTPStore)(nil)
