package understory

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// NewServer returns an http.Handler that serves the understory JSON API
// against the given *Index.
//
// Routes:
//
//   - GET /api/definition?symbol=...
//   - GET /api/references?symbol=...&include_definition=true|false
//   - GET /api/hover?symbol=...
//   - GET /api/symbol-at?file=...&line=...&char=...
//   - GET /api/files                                       (v0.2.1+)
//   - GET /api/occurrences?file=...                        (v0.2.1+)
//   - GET /api/source?file=...                             (v0.2.1+, 503 here)
//   - GET /api/healthz
//
// The handler is stateless except for the Index pointer. The Index is
// immutable after Open, so concurrent requests need no locking.
//
// Passing a nil *Index is permitted: /api/healthz still responds
// (with index_loaded:false), and the query endpoints return
// 503 Service Unavailable. This lets liveness probes run while a slow
// index is still loading.
//
// /api/source returns 503 because this constructor takes no source
// root. Use NewServerWithSource to enable source serving.
//
// Useful for consumers (canopy) that want to mount the API at a custom
// path or share their existing http.Server.
func NewServer(idx *Index) http.Handler {
	return NewServerWithSource(idx, nil)
}

// NewServerWithSource returns the same handler as NewServer but with
// /api/source backed by the given *os.Root. The os.Root is the
// kernel-enforced path-traversal-resistant directory handle (Go 1.24+);
// every read goes through it, so symlinks crafted to escape the root
// are refused at the syscall layer.
//
// Pass nil for sourceRoot to behave identically to NewServer.
//
// The handler does not close sourceRoot; the caller retains ownership
// (the CLI keeps it open for the server's lifetime; library consumers
// do the same).
func NewServerWithSource(idx *Index, sourceRoot *os.Root) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/definition", definitionHandler(idx))
	mux.HandleFunc("/api/references", referencesHandler(idx))
	mux.HandleFunc("/api/hover", hoverHandler(idx))
	mux.HandleFunc("/api/symbol-at", symbolAtHandler(idx))
	mux.HandleFunc("/api/files", filesHandler(idx))
	mux.HandleFunc("/api/occurrences", occurrencesHandler(idx))
	mux.HandleFunc("/api/source", sourceHandler(idx, sourceRoot))
	mux.HandleFunc("/api/highlight", highlightHandler(idx, sourceRoot))
	mux.HandleFunc("/api/healthz", healthzHandler(idx))
	mux.HandleFunc("/api/version", versionHandler())
	return mux
}

// jsonContentType is the single Content-Type every endpoint emits.
const jsonContentType = "application/json; charset=utf-8"

// writeJSON marshals v as JSON, sets the canonical Content-Type, and
// writes the response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	// Errors here are unrecoverable (client gone, write failure); the
	// header is already on the wire so we can't change status. Best-effort.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError is the standard 4xx/5xx response shape: {"error":"..."}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// requireMethodGET rejects anything other than GET with a 405. Keeps
// the API trivially curl-able; mutation endpoints are not in scope.
func requireMethodGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET")
	writeError(w, http.StatusMethodNotAllowed, "method not allowed; use GET")
	return false
}

// requireIndex returns true iff a non-nil Index is available. Otherwise
// the response is 503 and the caller should return.
func requireIndex(w http.ResponseWriter, idx *Index) bool {
	if idx == nil {
		writeError(w, http.StatusServiceUnavailable, "index not loaded")
		return false
	}
	return true
}

func definitionHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		if !requireIndex(w, idx) {
			return
		}
		symbol := r.URL.Query().Get("symbol")
		if symbol == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: symbol")
			return
		}
		loc, ok, err := idx.Definition(symbol)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]bool{"found": false})
			return
		}
		// Inline the Location fields plus the found flag. Reuses
		// Location's JSON tags so the wire shape stays consistent.
		writeJSON(w, http.StatusOK, struct {
			Found bool `json:"found"`
			Location
		}{Found: true, Location: loc})
	}
}

func referencesHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		if !requireIndex(w, idx) {
			return
		}
		q := r.URL.Query()
		symbol := q.Get("symbol")
		if symbol == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: symbol")
			return
		}
		includeDef := true
		if raw := q.Get("include_definition"); raw != "" {
			v, err := strconv.ParseBool(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid include_definition: must be true or false")
				return
			}
			includeDef = v
		}
		locs, err := idx.References(symbol, includeDef)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if locs == nil {
			locs = []Location{}
		}
		writeJSON(w, http.StatusOK, map[string][]Location{"locations": locs})
	}
}

func hoverHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		if !requireIndex(w, idx) {
			return
		}
		symbol := r.URL.Query().Get("symbol")
		if symbol == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: symbol")
			return
		}
		docs, err := idx.Hover(symbol)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if docs == nil {
			docs = []string{}
		}
		writeJSON(w, http.StatusOK, map[string][]string{"documentation": docs})
	}
}

func symbolAtHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		if !requireIndex(w, idx) {
			return
		}
		q := r.URL.Query()
		file := q.Get("file")
		if file == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: file")
			return
		}
		lineStr := q.Get("line")
		if lineStr == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: line")
			return
		}
		charStr := q.Get("char")
		if charStr == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: char")
			return
		}
		line, err := strconv.ParseInt(lineStr, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid line: "+err.Error())
			return
		}
		ch, err := strconv.ParseInt(charStr, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid char: "+err.Error())
			return
		}
		sym, err := idx.SymbolAtPos(file, int32(line), int32(ch))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"symbol": sym})
	}
}

// versionHandler returns the build metadata trio populated via
// -ldflags. Stable JSON shape: {"version": string, "commit": string,
// "built_at": string}. Mirrors the canopy /api/version contract so
// tooling can probe either app with the same parser.
func versionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"version":  Version,
			"commit":   Commit,
			"built_at": BuiltAt,
		})
	}
}

func healthzHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "ok",
			"index_loaded": idx != nil,
		})
	}
}

// ErrIndexNotLoaded is returned by future helpers that wrap the
// handlers; kept exported for consumers that wire their own routing.
var ErrIndexNotLoaded = errors.New("understory: index not loaded")

func filesHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		if !requireIndex(w, idx) {
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"files": idx.Files()})
	}
}

func occurrencesHandler(idx *Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		if !requireIndex(w, idx) {
			return
		}
		file := r.URL.Query().Get("file")
		if file == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: file")
			return
		}
		writeJSON(w, http.StatusOK, map[string][]Occurrence{
			"occurrences": idx.Occurrences(file),
		})
	}
}

// sourceHandler serves raw source bytes from sourceRoot.
//
// Responses:
//   - 200 + text/plain + bytes on success.
//   - 400 on missing or syntactically-invalid path (.., absolute, NUL).
//   - 404 on resolution failure (not found, symlink escape, etc.).
//   - 503 if sourceRoot is nil (server started without --source-root).
//
// Path safety is enforced by *os.Root: the OS refuses to resolve
// outside the root regardless of what the user passes. The pre-checks
// below give friendlier error messages but the *os.Root is the load-
// bearing protection.
func sourceHandler(idx *Index, sourceRoot *os.Root) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
		// idx is intentionally not required: a caller might want raw
		// source bytes even before the index finishes loading. The
		// nil-index pattern stays consistent with /api/healthz.
		_ = idx

		if sourceRoot == nil {
			writeError(w, http.StatusServiceUnavailable,
				"source serving disabled (server started without --source-root)")
			return
		}

		file := r.URL.Query().Get("file")
		if file == "" {
			writeError(w, http.StatusBadRequest, "missing required query parameter: file")
			return
		}
		if !validRelPath(file) {
			writeError(w, http.StatusBadRequest, "invalid path")
			return
		}

		f, err := sourceRoot.Open(file)
		if err != nil {
			// Includes "not found", "symlink escape refused", "not a file"
			// (when path is a directory), etc. Collapse all to 404 — the
			// distinction isn't useful to a UI client and leaking it
			// invites enumeration.
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		if info.IsDir() {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	}
}

// validRelPath does the cheap pre-checks before the os.Root call.
// The os.Root would catch all of these anyway; the value here is
// better error messages and a refusal at the request boundary instead
// of letting the kernel decide.
func validRelPath(p string) bool {
	if p == "" {
		return false
	}
	if strings.ContainsRune(p, 0) {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return false
	}
	cleaned := filepath.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return false
	}
	for part := range strings.SplitSeq(cleaned, string(filepath.Separator)) {
		if part == ".." {
			return false
		}
	}
	return true
}
