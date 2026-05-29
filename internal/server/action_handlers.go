package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/api/paths"
	"github.com/albertocavalcante/canopy/internal/compat"
	"github.com/albertocavalcante/canopy/internal/ratelimit"
)

const maxCompatCheckBody = 256 * 1024

// apiCompatCheck runs the A1 compatibility analyzer. Body is the
// raw MODULE.bazel text (Content-Type optional). Read-only: no
// auth gate today since the analyzer doesn't write or fetch.
// Future: token-bucket rate limit per source IP.
func (h *handler) apiCompatCheck(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	// Size cap is the first line of defense: bounds parser work
	// and audit-log payload size. Reading from a LimitReader means
	// we don't even buffer past the cap.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCompatCheckBody))
	if err != nil {
		http.Error(w, "compat-check: body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}

	// IncludeDevDependencies via querystring keeps the body pure
	// MODULE.bazel; the alternative (JSON-wrapped {"body": ..., ...})
	// blocks the convenient `curl --data-binary @MODULE.bazel`
	// pattern that operators expect.
	opts := api.CompatCheckOptions{
		IncludeDevDependencies: r.URL.Query().Get("dev") == "true",
	}
	res, err := h.c.CompatCheck(r.Context(), string(body), opts)
	if err != nil {
		// ErrEmptyInput is a 400 (caller-fixable); everything else
		// is a 500 (parse failure on otherwise-valid input).
		if errors.Is(err, compat.ErrEmptyInput) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "no bazel_dep declarations found in input",
			})
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiBump fetches one (module, version) from upstream, mirrors it,
// extracts an assay report, generates a SCIP index, and persists all
// three to canopy's store. Body: {"module": "...", "version": "...",
// "upstream": "..."}. Returns the produced ModuleReport on success.
//
// Same three gates as apiIngestRecursive: feature flag kill-switch +
// per-IP rate limit + global concurrency semaphore + SSRF guard on
// body.upstream. /api/bump is the endpoint the UI's "Ingest from BCR"
// button now hits (it produces a fully-queryable module in one shot,
// unlike /api/ingest-recursive which only populates the mirror tree).
func (h *handler) apiBump(w http.ResponseWriter, r *http.Request) {
	if !h.opts.Flags.IngestWriteEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "ingest writes are disabled on this canopy (CANOPY_INGEST_WRITE_ENABLED=false)",
		})
		return
	}

	release, err := h.ingestLimiter.Acquire(r.Context(), ratelimit.RemoteIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ratelimit.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		case errors.Is(err, ratelimit.ErrCapacityExhausted):
			w.Header().Set("Retry-After", "10")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		default:
			h.apiError(w, err)
		}
		return
	}
	defer release()

	var body struct {
		Module   string `json:"module"`
		Version  string `json:"version"`
		Upstream string `json:"upstream,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Module == "" || body.Version == "" {
		http.Error(w, "bad request: module and version are required", http.StatusBadRequest)
		return
	}

	// SSRF guard, same shape as apiIngestRecursive: drop client-
	// supplied upstream unless explicitly allowed by env.
	upstream := body.Upstream
	if !h.opts.Flags.IngestAllowCustomUpstream || upstream == "" {
		upstream = h.opts.Flags.RegistryURL
	}

	rep, err := h.c.Bump(r.Context(), api.BumpOptions{
		Module:   body.Module,
		Version:  body.Version,
		Upstream: upstream,
		Source:   sourceTag(r, "rest"),
	})
	if err != nil {
		// Surface the same 409 as drift when MirrorRoot is missing:
		// it's the same precondition.
		if strings.Contains(err.Error(), "bump not available") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		// Upstream-side errors (network, HTTP 404, integrity) -> 502.
		if strings.Contains(err.Error(), "resolve:") ||
			strings.Contains(err.Error(), "source.json") ||
			strings.Contains(err.Error(), "integrity") {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// apiIngestRecursive walks the bazel_dep closure starting from a root
// (module, version) and mirrors every reached module. Returns the
// walker's summary once the walk completes.
//
// Side-effect: each successfully-processed module publishes a
// module_indexed event so subscribers (UI, agents, other tabs) see
// the closure unfold in real time via /api/events.
//
// Three gates layered before the actual ingest call:
//  1. Feature-flag kill-switch (CANOPY_INGEST_WRITE_ENABLED).
//  2. Per-IP rate limit + global concurrency semaphore.
//  3. SSRF guard: body.Upstream is honored only when explicitly
//     allowed via CANOPY_INGEST_ALLOW_CUSTOM_UPSTREAM, otherwise the
//     server-configured RegistryURL is used. UI clients always go
//     through the default; CLI/MCP operators on the trusted box can
//     enable the override.
func (h *handler) apiIngestRecursive(w http.ResponseWriter, r *http.Request) {
	if !h.opts.Flags.IngestWriteEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "ingest writes are disabled on this canopy (CANOPY_INGEST_WRITE_ENABLED=false)",
		})
		return
	}

	release, err := h.ingestLimiter.Acquire(r.Context(), ratelimit.RemoteIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ratelimit.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		case errors.Is(err, ratelimit.ErrCapacityExhausted):
			w.Header().Set("Retry-After", "10")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		default:
			h.apiError(w, err)
		}
		return
	}
	defer release()

	var body struct {
		Module            string `json:"module"`
		Version           string `json:"version"`
		Upstream          string `json:"upstream,omitempty"`
		IncludeBazelTools bool   `json:"include_bazel_tools,omitempty"`
		BazelVersion      string `json:"bazel_version,omitempty"`
		Workers           int    `json:"workers,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Module == "" || body.Version == "" {
		http.Error(w, "bad request: module and version are required", http.StatusBadRequest)
		return
	}

	// SSRF guard. If the operator hasn't explicitly opted in to
	// caller-supplied upstreams, drop body.Upstream and fall through
	// to the server-configured RegistryURL. We don't error: silently
	// substituting is the right UX for the UI path, and CLI users
	// who hit this surprise will see the resolved registry in the
	// response's mirror metadata.
	upstream := body.Upstream
	if !h.opts.Flags.IngestAllowCustomUpstream || upstream == "" {
		upstream = h.opts.Flags.RegistryURL
	}

	res, err := h.c.IngestRecursive(r.Context(), api.IngestRecursiveOptions{
		Module:            body.Module,
		Version:           body.Version,
		Upstream:          upstream,
		IncludeBazelTools: body.IncludeBazelTools,
		BazelVersion:      body.BazelVersion,
		Workers:           body.Workers,
		Source:            sourceTag(r, "rest"),
	})
	if err != nil {
		if strings.Contains(err.Error(), "not available") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiIngestMissing walks the closure of (module, version) and bumps
// every external coordinate (referenced as a bazel_dep but not in
// the index). One-click "complete my dep tree" affordance the
// closure graph reveals as actionable.
//
// Gated by the same three layers as apiBump: write flag, rate
// limit, and SSRF guard semantics. Loops Service.IngestClosureMissing
// which serializes the bumps internally; a slow run streams progress
// to subscribed /api/events listeners via the per-Bump
// module_indexed events.
func (h *handler) apiIngestMissing(w http.ResponseWriter, r *http.Request) {
	if !h.opts.Flags.IngestWriteEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "ingest writes are disabled on this canopy (CANOPY_INGEST_WRITE_ENABLED=false)",
		})
		return
	}
	release, err := h.ingestLimiter.Acquire(r.Context(), ratelimit.RemoteIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ratelimit.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		case errors.Is(err, ratelimit.ErrCapacityExhausted):
			w.Header().Set("Retry-After", "10")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		default:
			h.apiError(w, err)
		}
		return
	}
	defer release()

	module, version := paths.ModuleVersion(r)
	res, err := h.c.IngestClosureMissing(r.Context(), module, version)
	if err != nil {
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// sourceTag returns the audit "source" string for a request. Clients
// can opt-in via the X-Canopy-Source header (e.g., "drift-ui");
// otherwise the caller's fallback (typically "rest") is used.
func sourceTag(r *http.Request, fallback string) string {
	if s := r.Header.Get("X-Canopy-Source"); s != "" {
		return s
	}
	return fallback
}
