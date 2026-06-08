package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/api/paths"
	"github.com/albertocavalcante/bzlhub/internal/backend"
)

func (h *handler) bazelRegistry(w http.ResponseWriter, r *http.Request) {
	// When canopy was started with --mirror-base-url, synthesize a
	// bazel_registry.json that advertises ourselves as a mirror. We
	// ignore whatever's on disk because the disk file is allowed to
	// stay at "{}" (mirror config is deployment-time, not ingest-time).
	if h.opts.MirrorBaseURL != "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mirrors": []string{h.opts.MirrorBaseURL},
		})
		return
	}
	h.serve(w, r, "application/json", func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetBazelRegistryJSON(ctx)
	})
}

// mirrorServe handles Bazel's mirror requests for tarballs. The path
// after /m/ is the upstream URL with the scheme stripped (Bazel's
// rewriting rule); we look it up in the mirror index to find the
// content-addressed blob.
//
// 404 if the URL isn't in our mirror (e.g., a module ingested after
// build OR an upstream we never fetched). Bazel will fall back to the
// original upstream URL in that case.
func (h *handler) mirrorServe(w http.ResponseWriter, r *http.Request) {
	rest := chi.URLParam(r, "*")
	key := urlKeyFromMirrorRequest(rest, r.URL.RawQuery)
	blobHex, ok := h.mirrorIdx.Lookup(key)
	if !ok {
		http.Error(w, "not in mirror: "+key, http.StatusNotFound)
		return
	}
	h.serve(w, r, contentTypeFor(rest), func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetBlob(ctx, blobHex)
	})
}

func (h *handler) metadata(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	h.serve(w, r, "application/json", func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetMetadata(ctx, module)
	})
}

func (h *handler) moduleBazel(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	h.serve(w, r, "text/plain; charset=utf-8", func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetModuleBazel(ctx, module, version)
	})
}

func (h *handler) sourceJSON(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	h.serve(w, r, "application/json", func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetSourceJSON(ctx, module, version)
	})
}

func (h *handler) patch(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	filename := chi.URLParam(r, "filename")
	h.serve(w, r, "text/x-patch", func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetPatch(ctx, module, version, filename)
	})
}

func (h *handler) overlay(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	// chi's wildcard {*} matches everything after overlay/.
	path := chi.URLParam(r, "*")
	h.serve(w, r, contentTypeFor(path), func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetOverlay(ctx, module, version, path)
	})
}

func (h *handler) blob(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	h.serve(w, r, contentTypeFor(key), func(ctx context.Context) (io.ReadCloser, error) {
		return h.b.GetBlob(ctx, key)
	})
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request, contentType string, fetch func(context.Context) (io.ReadCloser, error)) {
	body, err := fetch(r.Context())
	if err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, backend.ErrUpstreamUnavailable) {
			// Bazel retries on 5xx; 503 + Retry-After is the right
			// signal when every federation upstream failed transiently.
			h.log.Warn("upstreams unavailable", "path", r.URL.Path, "err", err)
			w.Header().Set("Retry-After", "5")
			http.Error(w, "upstreams unavailable", http.StatusServiceUnavailable)
			return
		}
		h.log.Error("backend error", "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", contentType)
	if _, err := io.Copy(w, body); err != nil {
		h.log.Warn("response copy failed", "path", r.URL.Path, "err", err)
	}
}

// contentTypeFor picks a Content-Type for blob/overlay paths. The set is
// small because we only care about the few file shapes Bazel actually fetches.
func contentTypeFor(name string) string {
	switch {
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "application/gzip"
	case strings.HasSuffix(name, ".tar"):
		return "application/x-tar"
	case strings.HasSuffix(name, ".zip"):
		return "application/zip"
	case strings.HasSuffix(name, ".patch"), strings.HasSuffix(name, ".diff"):
		return "text/x-patch"
	case strings.HasSuffix(name, ".bazel"), strings.HasSuffix(name, ".bzl"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
