package httpstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
)

// WriteOptions configures a write call's request shape. Zero-value
// is valid for most uses (no conditional-write, default
// content-type chosen by the method).
type WriteOptions struct {
	// IfMatch, when non-empty, is sent as the If-Match request
	// header — RFC 7232 conditional write semantics. The upstream
	// MUST reject the request with 412 Precondition Failed if
	// the resource's current ETag doesn't match.
	//
	// Use this when concurrent publishers may race for the same
	// path: read the current ETag via Stat, hold it, perform the
	// write with IfMatch=<read ETag>. A 412 (ErrConflict) tells
	// you someone else won; retry against the new ETag.
	IfMatch string

	// ContentType overrides the default Content-Type the method
	// would send. Methods choose sensible defaults
	// (application/json for *JSON methods, application/octet-stream
	// for Blob/Patch/Overlay, text/plain for MODULE.bazel). Set
	// this if your upstream requires something specific (e.g.
	// Artifactory's content-type detection for the storage API).
	ContentType string
}

// ---- BCR write methods -------------------------------------------

// WriteMetadataJSON writes modules/<module>/metadata.json. Returns
// ErrConflict on 412/409 (typically an IfMatch race), ErrUnauthorized
// on 401, ErrForbidden on 403, ErrUpstreamStatus on other non-2xx.
//
// On success: the corresponding cache entry is invalidated
// (write-invalidate semantics — the next read re-fetches the
// freshly-written body from upstream so the ETag matches what the
// upstream actually returned, not what the client sent).
func (b *Backend) WriteMetadataJSON(ctx context.Context, module string, body []byte, opts WriteOptions) error {
	return b.writeBytes(ctx,
		b.contentPath(path.Join("modules", module, "metadata.json")),
		body, opts, "application/json")
}

// WriteSourceJSON writes modules/<module>/<version>/source.json.
// See WriteMetadataJSON for error + cache semantics.
func (b *Backend) WriteSourceJSON(ctx context.Context, module, version string, body []byte, opts WriteOptions) error {
	return b.writeBytes(ctx,
		b.contentPath(path.Join("modules", module, version, "source.json")),
		body, opts, "application/json")
}

// WriteModuleBazel writes modules/<module>/<version>/MODULE.bazel.
// See WriteMetadataJSON for error + cache semantics.
func (b *Backend) WriteModuleBazel(ctx context.Context, module, version string, body []byte, opts WriteOptions) error {
	return b.writeBytes(ctx,
		b.contentPath(path.Join("modules", module, version, "MODULE.bazel")),
		body, opts, "text/plain; charset=utf-8")
}

// WritePatch writes modules/<module>/<version>/patches/<patchName>.
// See WriteMetadataJSON for error + cache semantics.
func (b *Backend) WritePatch(ctx context.Context, module, version, patchName string, body []byte, opts WriteOptions) error {
	return b.writeBytes(ctx,
		b.contentPath(path.Join("modules", module, version, "patches", patchName)),
		body, opts, "text/x-diff")
}

// WriteOverlay writes modules/<module>/<version>/overlay/<path>.
// See WriteMetadataJSON for error + cache semantics.
//
// Callers are responsible for validating overlayPath against
// their own path-traversal discipline — the library does NOT
// reject leading "../" (kernel-enforced safety belongs in a
// wrapping layer).
func (b *Backend) WriteOverlay(ctx context.Context, module, version, overlayPath string, body []byte, opts WriteOptions) error {
	return b.writeBytes(ctx,
		b.contentPath(path.Join("modules", module, version, "overlay", overlayPath)),
		body, opts, "application/octet-stream")
}

// WriteBazelRegistryJSON writes the BCR root marker at
// <BaseURL>/bazel_registry.json. See WriteMetadataJSON for error
// + cache semantics.
func (b *Backend) WriteBazelRegistryJSON(ctx context.Context, body []byte, opts WriteOptions) error {
	return b.writeBytes(ctx, b.contentPath("bazel_registry.json"), body, opts, "application/json")
}

// WriteBlob streams an opaque blob to <BaseURL>/blobs/<key>. Unlike
// the other write methods this takes io.Reader (and length, since
// some upstreams insist on Content-Length up front) rather than
// []byte — blobs are typically source tarballs in the tens-to-
// hundreds of MiB, and buffering the full body in memory is
// wasteful.
//
// length must equal the number of bytes that will be read from
// body. Pass -1 to defer to the http.Client's chunked-transfer
// behaviour; some upstreams (notably Artifactory and many S3-shape
// stores) reject chunked uploads and require an explicit
// Content-Length.
//
// Bypasses the cache by design (mirror of ReadBlob bypass).
//
// Returns ErrConflict / ErrUnauthorized / ErrForbidden /
// ErrUpstreamStatus per the standard mapping.
func (b *Backend) WriteBlob(ctx context.Context, key string, body io.Reader, length int64, opts WriteOptions) error {
	if containsPathSeparator(key) {
		return fmt.Errorf("%w: %s (key contains path separators)", ErrBlobNotFound, key)
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	headers := buildWriteHeaders(contentType, opts.IfMatch)
	if length >= 0 {
		headers.Set("Content-Length", fmt.Sprintf("%d", length))
	}
	resp, u, err := b.do(ctx, http.MethodPut, b.contentPath("blobs/"+key), headers, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapWriteStatus(resp, u)
	// NOTE: blobs are not cached on read (streaming), so no
	// cache.Delete needed here.
}

// ---- internal: write helpers -------------------------------------

// writeBytes is the workhorse PUT for fixed-size bodies. Builds
// headers (content-type + optional If-Match), executes the request,
// maps the response to a typed sentinel, and on success invalidates
// the cache entry at relPath.
func (b *Backend) writeBytes(
	ctx context.Context,
	relPath string,
	body []byte,
	opts WriteOptions,
	defaultContentType string,
) error {
	contentType := opts.ContentType
	if contentType == "" {
		contentType = defaultContentType
	}
	headers := buildWriteHeaders(contentType, opts.IfMatch)
	headers.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp, u, err := b.do(ctx, http.MethodPut, relPath, headers, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := mapWriteStatus(resp, u); err != nil {
		return err
	}
	// Write-invalidate: drop any cached read of this path so the
	// next read re-fetches from upstream and stores the new ETag.
	if b.cache != nil {
		b.cache.Delete(ctx, relPath)
	}
	return nil
}

// buildWriteHeaders constructs the per-call header set for a write
// request: Content-Type plus optional If-Match for conditional
// writes.
func buildWriteHeaders(contentType, ifMatch string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	if ifMatch != "" {
		h.Set("If-Match", ifMatch)
	}
	return h
}

// mapWriteStatus translates an upstream response into the
// appropriate sentinel error. Success (200/201/204) returns nil.
// 412/409 → ErrConflict. 401 → ErrUnauthorized. 403 →
// ErrForbidden. Other non-2xx → ErrUpstreamStatus.
//
// Body is left unread; caller closes.
func mapWriteStatus(resp *http.Response, u *url.URL) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s %s", ErrUnauthorized, resp.Request.Method, u.String())
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s %s", ErrForbidden, resp.Request.Method, u.String())
	case http.StatusPreconditionFailed, http.StatusConflict:
		return fmt.Errorf("%w: %s %s -> %d %s",
			ErrConflict, resp.Request.Method, u.String(), resp.StatusCode, resp.Status)
	default:
		return fmt.Errorf("%w: %s %s -> %d %s",
			ErrUpstreamStatus, resp.Request.Method, u.String(), resp.StatusCode, resp.Status)
	}
}

// containsPathSeparator is shared with ReadBlob's defence.
// Lifted here so write.go doesn't have to import strings just for
// strings.ContainsAny.
func containsPathSeparator(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == '\\' {
			return true
		}
	}
	return false
}
