package understory

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	bazel "github.com/albertocavalcante/bazel-highlight-go/highlight"
	"github.com/albertocavalcante/starlark-highlight-go/highlight"
	starlark "github.com/albertocavalcante/starlark-highlight-go/starlark"
)

// hlRegistry is the dialect set understory exposes via /api/highlight.
//
// Bazel is registered first because its Match() claims .bzl on top
// of BUILD/MODULE.bazel/WORKSPACE, and Bazel-flavored tokens (with
// builtins, labels, attributes) strictly subsume pure-Starlark
// classification. Pure-Starlark stays registered for .star files
// that the Bazel dialect doesn't claim.
//
// Adding more dialects later (Buck2, Copybara) is a one-line
// registration; the wire shape downstream doesn't change because
// dialect-specific kinds are namespaced (bazel.label, buck2.target).
var hlRegistry = highlight.NewRegistry().
	Register(bazel.Dialect{}).
	Register(starlark.Dialect{})

// highlightResponse is the wire shape returned by GET /api/highlight.
//
// Tokens are sorted by (start_line, start_char) and never overlap —
// the highlight library guarantees both. Empty `tokens` is a valid
// "no highlighting available" response (e.g. a non-Starlark file
// requested by a client that calls /api/highlight unconditionally).
//
// Wire indexing: 0-based lines and chars, matching SCIP's
// Occurrence convention. The underlying library emits 1-based lines
// (Go's natural convention); we shift at the boundary so client
// merge code can assume one indexing scheme across /api/occurrences
// and /api/highlight.
type highlightResponse struct {
	Tokens []wireToken `json:"tokens"`
}

type wireToken struct {
	Kind      string            `json:"kind"`
	StartLine int               `json:"start_line"`
	StartChar int               `json:"start_char"`
	EndLine   int               `json:"end_line"`
	EndChar   int               `json:"end_char"`
	// Meta passes through dialect-specific data verbatim (e.g.
	// bazel-highlight-go emits {name, url, description} on
	// bazel.builtin tokens). Renderers ignore unknown keys; the
	// omitempty keeps the JSON tight for kinds that don't use it.
	Meta map[string]string `json:"meta,omitempty"`
}

func toWire(toks []highlight.Token) []wireToken {
	out := make([]wireToken, len(toks))
	for i, t := range toks {
		out[i] = wireToken{
			Kind:      string(t.Kind),
			StartLine: t.StartLine - 1, // 1-based (lib) → 0-based (wire)
			StartChar: t.StartChar,
			EndLine:   t.EndLine - 1,
			EndChar:   t.EndChar,
			Meta:      t.Meta,
		}
	}
	return out
}

// highlightHandler is /api/highlight?file=<path>. Returns a token
// stream the UI can overlay with SCIP occurrences.
//
// Why this lives in understory rather than canopy: understory already
// owns the "serve source bytes from --source-root" surface. Highlight
// is a natural sibling — same path-safety, same source-root gate, same
// cache lifetime (the source bytes are content-addressed by canopy
// before understory ever sees them, so the highlight result is valid
// for the life of that coordinate's unpack).
//
// Why grammar logic lives in starlark-highlight-go rather than here:
// the classification map "Starlark Token → highlight.Kind" is grammar-
// coupled, not transport-coupled. Any future Bazel tool that wants
// highlighting (a linter, a TUI, an LSP, stardoc) can import that lib
// without dragging in understory's SCIP surface.
//
// Responses:
//   - 200 + {"tokens":[...]} for a supported dialect that parses cleanly.
//   - 200 + {"tokens":[]}    for an unsupported dialect (.txt, .py, etc.)
//     — keeps the client call site simple: always fetch, render whatever.
//   - 400 on missing/invalid file param (matches /api/source).
//   - 404 on file resolution failure.
//   - 503 when sourceRoot is nil — same gate as /api/source.
//
// On parse error, tokens collected before the error are still
// returned (highlight.Tokenize returns partial results); we log the
// error but don't surface a 500 because partial highlighting beats
// nothing.
func highlightHandler(idx *Index, sourceRoot *os.Root) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireMethodGET(w, r) {
			return
		}
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
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil || info.IsDir() {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}

		src, err := io.ReadAll(f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read failed")
			return
		}

		tokens, terr := hlRegistry.Tokenize(file, src)
		if errors.Is(terr, highlight.ErrUnsupportedDialect) {
			// Not Starlark-shaped — return empty tokens so the client
			// can call this unconditionally on every file open.
			tokens = []highlight.Token{}
		}
		// Source bytes in the codenav cache are content-addressed
		// (canopy unpacks tarballs by SHA into stable dirs), so the
		// highlight result is valid for the life of that unpack.
		// Aggressive caching is correct here; clients that need a
		// re-tokenize can purge by changing the URL.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		// Always emit `tokens:[]` instead of `tokens:null`, even when
		// tokens is nil from a parse error before any emission.
		wire := toWire(tokens)
		if wire == nil {
			wire = []wireToken{}
		}
		_ = json.NewEncoder(w).Encode(highlightResponse{Tokens: wire})
	}
}
