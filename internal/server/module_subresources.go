package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/stardoc-go"

	"github.com/albertocavalcante/bzlhub/internal/api/paths"
)

// apiGetReverseDeps returns the modules-that-depend-on-this list:
// GET /api/modules/{m}/{v}/reverse-deps
//
// Symmetric counterpart to apiGetClosureGraph. Always returns
// 200 with possibly-empty Deps; "nothing depends on this in our
// index" is real information, not a 404.
func (h *handler) apiGetReverseDeps(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	res, err := h.c.ReverseDeps(r.Context(), module, version)
	if err != nil {
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiGetClosureGraph returns the transitive bazel_dep graph rooted
// at (module, version) by walking persisted ModuleReports.
// GET /api/modules/{m}/{v}/closure-graph
//
// External nodes (in some report's bazel_deps but not in the store)
// are marked External=true so the UI can render them in a muted
// style. Empty graph is returned for an unknown root (vs 404) so
// the UI can show "no closure data" cleanly without distinguishing
// "not ingested" from "no deps."
func (h *handler) apiGetClosureGraph(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	g, err := h.c.Closure(r.Context(), module, version)
	if err != nil {
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// apiGetExampleFiles returns the inlined contents of one example
// directory under the module's source tree:
// GET /api/modules/{m}/{v}/example-files?dir=example
//
// dir must be a single path component (one of the names surfaced via
// ModuleReport.Assets.ExampleDirs); the service rejects `..` etc.
// 400 on bad input, 5xx on materialize/walk failures.
func (h *handler) apiGetExampleFiles(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		http.Error(w, "missing required ?dir= query param", http.StatusBadRequest)
		return
	}
	res, err := h.c.ExampleFiles(r.Context(), module, version, dir)
	if err != nil {
		if strings.Contains(err.Error(), "invalid example dir") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiGetDocs renders the stored module's ModuleReport as Stardoc-shape
// Markdown via stardoc-go and writes it to the response with
// text/markdown content-type.
//
// Same payload as the `bzlhub export-docs` CLI - different transport.
// Designed for external integrators that want docs without an
// SSH+CLI step (curl + commit to docs site, fetch from CI for drift
// checks, etc.). Deterministic output makes caching trivial.
//
// ?include_private=true flips Stardoc's default (hidden) to surface
// underscore-prefixed symbols, mirroring the CLI flag.
//
// ?format= accepts "md" (default; matches the legacy /docs.md path)
// and is forward-compatible with a future JSON shape (returns 501
// Not Implemented for any other value).
func (h *handler) apiGetDocs(w http.ResponseWriter, r *http.Request) {
	switch f := r.URL.Query().Get("format"); f {
	case "", "md":
		// fall through to markdown rendering
	default:
		http.Error(w, fmt.Sprintf("unsupported format %q", f), http.StatusNotImplemented)
		return
	}
	module, version := paths.ModuleVersion(r)
	rep, err := h.c.GetModuleVersion(r.Context(), module, version)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	includePrivate := r.URL.Query().Get("include_private") == "true"
	md := stardoc.RenderWithOptions(rep, stardoc.Options{IncludePrivate: includePrivate})
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(md)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(md))
}

// apiGetScip serves the binary protobuf SCIP index for one module-version:
// GET /api/modules/{module}/{version}/scip
//
// Content-Type is application/vnd.sourcegraph.scip+protobuf - the same
// MIME Sourcegraph's `scip` CLI and IDE plugins expect. 404 when the
// blob wasn't generated for this pair (older ingest, or scip-bazel
// failure for that module).
func (h *handler) apiGetScip(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	blob, err := h.c.GetScipBlob(r.Context(), module, version)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sourcegraph.scip+protobuf")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(blob)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob)
}

// apiXRefs serves cross-module references for a SCIP symbol:
// GET /api/xrefs?symbol=<full-scip-symbol>&include_definition=<bool>
//
// Walks every indexed (module, version), groups occurrences of the
// queried symbol by module. Returns 400 if symbol is missing; there's
// no useful empty-symbol semantic and the failure mode is hard to spot
// in logs if we silently 200 with empty groups.
//
// include_definition defaults to false: the UI's "Used by other
// modules" panel is about consumers, not the defining occurrence (the
// regular references panel already shows that).
func (h *handler) apiXRefs(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "symbol is required"})
		return
	}
	includeDef := r.URL.Query().Get("include_definition") == "true"
	res, err := h.c.LookupXRefs(r.Context(), symbol, includeDef)
	if err != nil {
		h.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiGetConsumers serves the cross-corpus consumer view (Plan 07):
// every call site of the named rule/provider/macro/repo_rule/
// module_extension across canopy's indexed corpus.
//
// Path: GET /api/v1/modules/{module}/versions/{version}/consumers/{name}
// Query: ?include_self=true keeps the defining module's own occurrences
// (default behavior filters them out so the list shows TRUE consumers).
//
// 404 when:
//   - (module, version) isn't indexed
//   - the name doesn't resolve to any symbol in that module's
//     stored ModuleReport
func (h *handler) apiGetConsumers(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	includeSelf := paths.QueryBool(r, "include_self")
	res, err := h.c.LookupConsumers(r.Context(), module, version, name, includeSelf)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	writeJSONWithETag(w, r, http.StatusOK, res)
}
