package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// apiDrift compares the mirror against an upstream BCR-shape registry.
// Query params:
//
//	upstream - registry URL (default: service.DefaultUpstream)
//	module   - optional single-module filter
//	workers  - concurrent upstream fetches (default 4)
func (h *handler) apiDrift(w http.ResponseWriter, r *http.Request) {
	opts := api.DriftOptions{
		Upstream: r.URL.Query().Get("upstream"),
		Module:   r.URL.Query().Get("module"),
		Workers:  atoiOrDefault(r.URL.Query().Get("workers"), 0),
	}
	rep, err := h.c.Drift(r.Context(), opts)
	if err != nil {
		// Drift returns a clean "not configured" error when --root wasn't passed.
		if strings.Contains(err.Error(), "drift not available") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// apiDiff returns the structured delta between two ModuleReports of
// the same module: GET /api/modules/{module}/diff?from=A&to=B[&upstream=URL].
//
// Without ?upstream, 404 if either version isn't in the index. With
// ?upstream provided, missing sides are fetched and analyzed on-the-fly
// from that BCR-shape registry without persisting; the response's
// from_source/to_source fields record which side came from where.
func (h *handler) apiDiff(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	q := r.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	upstream := q.Get("upstream")
	if module == "" || from == "" || to == "" {
		http.Error(w, "module, from, and to are required", http.StatusBadRequest)
		return
	}
	d, err := h.c.Diff(r.Context(), api.DiffOptions{
		Module:      module,
		FromVersion: from,
		ToVersion:   to,
		Upstream:    upstream,
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// apiDiffClosure surfaces the recursive bazel_dep closure diff:
// GET /api/modules/{module}/diff-closure?from=A&to=B&upstream=URL.
// Upstream is required (MVS resolution needs a registry to walk).
func (h *handler) apiDiffClosure(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	q := r.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	upstream := q.Get("upstream")
	if module == "" || from == "" || to == "" {
		http.Error(w, "module, from, and to are required", http.StatusBadRequest)
		return
	}
	if upstream == "" {
		http.Error(w, "upstream is required for closure diff (MVS resolution needs a registry)", http.StatusBadRequest)
		return
	}
	d, err := h.c.DiffClosure(r.Context(), api.DiffOptions{
		Module:      module,
		FromVersion: from,
		ToVersion:   to,
		Upstream:    upstream,
	})
	if err != nil {
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// apiHistory returns recent audit events filtered by ?kind=&source=&module=&limit=.
// `kind` may be repeated.
func (h *handler) apiHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := api.HistoryOptions{
		Kinds:  q["kind"],
		Source: q.Get("source"),
		Module: q.Get("module"),
		Limit:  atoiOrDefault(q.Get("limit"), 0),
	}
	out, err := h.c.History(r.Context(), opts)
	if err != nil {
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}
