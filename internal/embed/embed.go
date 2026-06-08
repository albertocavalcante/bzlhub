// Package embed embeds the compiled SvelteKit UI into the canopy binary
// via go:embed and exposes an http.Handler that serves it with a sane
// SPA fallback (any unmatched path returns index.html so the client
// router can take over).
package embed

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:ui
var assets embed.FS

// Handler returns an http.Handler that serves the embedded SvelteKit build.
// Assets are served verbatim; any unmatched non-asset path returns
// index.html so the SvelteKit client router can resolve it.
//
// If the embed is empty (e.g., the build directory wasn't copied in before
// `go build`), Handler returns a stub that responds with a 503 explaining
// how to rebuild. This keeps `go test`/`go build` working even when the
// UI hasn't been compiled.
func Handler() http.Handler {
	return HandlerWithTransform(nil)
}

// HandlerWithTransform is Handler with an optional transform applied to
// the index.html body before it's written. Asset responses pass through
// untouched (they're not text/html). The transform is used by the canopy
// server to inject per-URL SEO <head> tags into the SPA shell — see
// internal/server/headtags/. Pass nil for the no-op behaviour Handler
// gives you.
func HandlerWithTransform(transform func(req *http.Request, html []byte) []byte) http.Handler {
	sub, err := fs.Sub(assets, "ui")
	if err != nil || !hasIndex(sub) {
		return missingBuildHandler{}
	}
	fsrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If the request targets an asset that exists, serve it directly.
		if _, err := fs.Stat(sub, strings.TrimPrefix(path.Clean(r.URL.Path), "/")); err == nil {
			fsrv.ServeHTTP(w, r)
			return
		}
		// Otherwise, fall back to index.html (SPA routing).
		idx, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if transform != nil {
			idx = transform(r, idx)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(idx)
	})
}

func hasIndex(sub fs.FS) bool {
	_, err := fs.Stat(sub, "index.html")
	return err == nil
}

type missingBuildHandler struct{}

func (missingBuildHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><meta charset=utf-8>
<title>canopy UI not built</title>
<style>body{font-family:system-ui;max-width:40rem;margin:5rem auto;padding:0 1rem;color:#333}code{background:#f3f3f3;padding:.1rem .35rem;border-radius:3px;font-size:.9rem}</style>
<h1>canopy UI not built</h1>
<p>The embedded UI bundle is empty. To build it:</p>
<pre><code>cd ui && pnpm install && pnpm run build
cp -r ui/build/* internal/embed/ui/
go build -o canopy ./cmd/bzlhub</code></pre>
<p>Or run <code>./scripts/embed-ui.sh</code> if present.</p>`))
}
