package httpstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// Info describes a path on the upstream store without reading
// its body. The Backend's Stat method populates this via HEAD;
// callers (especially cache implementations) use it to decide
// whether a cached entry is still valid before paying the cost
// of a GET.
//
// Empty fields signal "the upstream didn't expose this header"
// rather than "the value is empty" — operators should treat
// missing ETag as "no conditional-GET available" not "empty
// ETag matches an empty cache entry".
type Info struct {
	// Size is the response's Content-Length in bytes. Zero
	// when the upstream omitted Content-Length (chunked
	// transfer-encoding, or a transformer in front).
	Size int64

	// LastModified is the parsed Last-Modified header. Zero
	// when absent. RFC 7232 §2.2 format.
	LastModified time.Time

	// ETag is the entity tag exactly as the upstream returned
	// it — wrapping double-quotes preserved if present, weak
	// `W/` prefix preserved if present. Conditional GETs send
	// this verbatim in If-None-Match.
	ETag string
}

// infoFromResponse extracts Info from a successful HEAD/GET
// response. Internal helper shared between Stat and any future
// cache-validation paths.
func infoFromResponse(resp *http.Response) Info {
	info := Info{
		Size: resp.ContentLength, // -1 if unknown
		ETag: resp.Header.Get("ETag"),
	}
	if info.Size < 0 {
		info.Size = 0
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, perr := http.ParseTime(lm); perr == nil {
			info.LastModified = t
		}
	}
	return info
}

// ---- BCR read methods --------------------------------------------

// ReadMetadataJSON reads modules/<module>/metadata.json. Returns
// ErrModuleNotFound on 404. Other transport / non-2xx responses
// surface as ErrUpstreamStatus wrapped with the status code.
func (b *Backend) ReadMetadataJSON(ctx context.Context, module string) ([]byte, error) {
	body, err := b.getBytes(ctx, path.Join("modules", module, "metadata.json"))
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, module)
		}
		return nil, err
	}
	return body, nil
}

// ReadSourceJSON reads modules/<module>/<version>/source.json.
// Returns ErrVersionNotFound on 404.
func (b *Backend) ReadSourceJSON(ctx context.Context, module, version string) ([]byte, error) {
	body, err := b.getBytes(ctx, path.Join("modules", module, version, "source.json"))
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s@%s", ErrVersionNotFound, module, version)
		}
		return nil, err
	}
	return body, nil
}

// ReadModuleBazel reads modules/<module>/<version>/MODULE.bazel.
// Returns ErrVersionNotFound on 404.
func (b *Backend) ReadModuleBazel(ctx context.Context, module, version string) ([]byte, error) {
	body, err := b.getBytes(ctx, path.Join("modules", module, version, "MODULE.bazel"))
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s@%s", ErrVersionNotFound, module, version)
		}
		return nil, err
	}
	return body, nil
}

// ReadBazelRegistryJSON reads the BCR root marker at
// <BaseURL>/bazel_registry.json. Returns ErrRegistryJSONNotFound
// on 404 — almost certainly a misconfigured BaseURL pointing at
// a non-BCR tree.
func (b *Backend) ReadBazelRegistryJSON(ctx context.Context) ([]byte, error) {
	body, err := b.getBytes(ctx, "bazel_registry.json")
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s", ErrRegistryJSONNotFound, b.baseURL.String())
		}
		return nil, err
	}
	return body, nil
}

// ReadOverlay reads modules/<module>/<version>/overlay/<path>.
// Overlay paths are relative-nested under the overlay directory
// — callers MAY pass "subdir/file" and the library joins it
// with path.Join. Returns ErrOverlayNotFound on 404.
//
// Callers are responsible for validating overlayPath against
// their own path-traversal discipline — the library does NOT
// reject leading "../" because the kernel-enforced safety it
// would imply is a different layer (callers wrap this method).
func (b *Backend) ReadOverlay(ctx context.Context, module, version, overlayPath string) ([]byte, error) {
	body, err := b.getBytes(ctx, path.Join("modules", module, version, "overlay", overlayPath))
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s@%s/%s", ErrOverlayNotFound, module, version, overlayPath)
		}
		return nil, err
	}
	return body, nil
}

// ReadPatch reads modules/<module>/<version>/patches/<patchName>.
// Returns ErrPatchNotFound on 404 — a separate sentinel from
// ErrVersionNotFound because a present version-dir with a missing
// patch is a different operator condition (patch was removed or
// renamed upstream) than the whole version being absent.
//
// patchName is taken verbatim; callers are responsible for
// validating it against their patch-name discipline.
func (b *Backend) ReadPatch(ctx context.Context, module, version, patchName string) ([]byte, error) {
	body, err := b.getBytes(ctx, path.Join("modules", module, version, "patches", patchName))
	if err != nil {
		if errors.Is(err, ErrUpstream404) {
			return nil, fmt.Errorf("%w: %s@%s/%s", ErrPatchNotFound, module, version, patchName)
		}
		return nil, err
	}
	return body, nil
}

// ReadBlob streams an opaque blob from <BaseURL>/blobs/<key>.
// Unlike the other read methods this returns io.ReadCloser
// rather than []byte — blobs are typically source tarballs in
// the tens-to-hundreds of MiB, and buffering the full body in
// memory is wasteful.
//
// CALLER MUST CLOSE the returned ReadCloser. Failing to close
// leaks the underlying HTTP connection.
//
// Bypasses the configured Cache by design — streaming responses
// don't fit a body-buffering cache.
//
// Returns ErrBlobNotFound on 404.
func (b *Backend) ReadBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	if strings.ContainsAny(key, `/\`) {
		// Defence at the boundary — blob keys are opaque
		// content-addresses; embedded path separators are
		// always wrong, never just absent.
		return nil, fmt.Errorf("%w: %s (key contains path separators)", ErrBlobNotFound, key)
	}
	resp, u, err := b.do(ctx, http.MethodGet, "blobs/"+key, nil, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, key)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: GET %s -> %d %s",
			ErrUpstreamStatus, u.String(), resp.StatusCode, resp.Status)
	}
	return resp.Body, nil
}

// ---- probes ------------------------------------------------------

// Stat probes a path with HEAD and returns Info describing it.
// Useful to caches that want to validate a stored entry without
// re-reading the body: stat the upstream, compare ETag /
// LastModified, decide.
//
// Bypasses the configured Cache by design — HEAD probes have no
// body to cache and aren't a natural fit for the body-cache
// shape.
//
// Returns ErrUpstream404-wrapped error on 404. Like the read methods,
// the caller's intent (provided as the relative path) determines
// which sentinel — Stat is path-generic.
func (b *Backend) Stat(ctx context.Context, relPath string) (Info, error) {
	resp, u, err := b.do(ctx, http.MethodHead, relPath, nil, nil)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Surface as the generic 404 sentinel — Stat is path-
		// generic so callers map to module/version/patch on
		// their own.
		return Info{}, fmt.Errorf("%w: HEAD %s", ErrUpstream404, u.String())
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return Info{}, fmt.Errorf("%w: HEAD %s -> %d %s",
			ErrUpstreamStatus, u.String(), resp.StatusCode, resp.Status)
	}
	return infoFromResponse(resp), nil
}

// Exists is a cheap HEAD-based predicate. Returns (true, nil) on
// 2xx, (false, nil) on 404, and (false, err) on transport / non-
// 2xx-non-404 conditions. Callers that need a richer answer use
// Stat instead.
//
// "Cheap" relative to ReadX — HEAD still costs one round trip
// per call. Don't loop Exists if a single ListVersions probe
// would tell you the same thing.
func (b *Backend) Exists(ctx context.Context, relPath string) (bool, error) {
	_, err := b.Stat(ctx, relPath)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrUpstream404):
		return false, nil
	default:
		return false, err
	}
}

// ---- lists -------------------------------------------------------

// ListModules returns every module name visible to the configured
// Layout. Empty list when the layout's index is missing — soft-
// failing here mirrors the plan-43 contract: an empty listing is
// surfacable to operators in the UI banner instead of crashing.
// Hard parse failures of the index are still surfaced as
// ErrIndexUnreadable.
func (b *Backend) ListModules(ctx context.Context) ([]string, error) {
	return b.layout.ListModules(ctx, b)
}

// ListVersions returns every version of a module visible to the
// Layout. ErrModuleNotFound if the layout knows the module is
// absent.
func (b *Backend) ListVersions(ctx context.Context, module string) ([]string, error) {
	return b.layout.ListVersions(ctx, b, module)
}
