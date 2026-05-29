// Package ui contains the embedded SvelteKit web UI for understory.
//
// It is intentionally a separate package from pkg/understory: the
// //go:embed directive below bakes the SvelteKit bundle (~1.5 MB raw)
// into any binary that imports this package. Consumers that want the
// JSON API only (canopy, gvy) should import pkg/understory and leave
// this package alone.
//
// Build pipeline note: //go:embed cannot cross package boundaries
// (paths cannot escape with ".."), so this package contains its own
// web/build/ subtree at pkg/understory/ui/web/build/. A committed
// stub index.html lives there so `go build` works on a fresh clone
// with no Node toolchain. The SvelteKit project at the repo's
// top-level web/ directory must be configured (via adapter-static's
// `pages`/`assets` options in svelte.config.js) to emit into
// pkg/understory/ui/web/build/ so the build output lands where this
// embed directive looks for it.
package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/albertocavalcante/understory/pkg/understory"
)

// On a fresh clone, web/build/ contains only a committed stub
// index.html (the rest of the directory is gitignored). After
// `pnpm install && pnpm build` runs in web/, adapter-static overwrites
// it with the real bundle. The stub is enough for `go build` to
// succeed without Node installed.
//
//go:embed all:web/build
var buildFS embed.FS

// NewServerWithUI wraps understory.NewServerWithSource with the
// embedded SvelteKit app mounted at /. Pass nil sourceRoot to behave
// like understory.NewServer (the /api/source endpoint returns 503).
//
// Routing:
//
//   - /api/*           — delegated to the v0.2.1 JSON API handler.
//   - everything else  — served from the embedded web/build/ tree.
//   - SPA deep-link    — any path that does not resolve to a real
//     asset falls back to index.html so SvelteKit's client-side
//     router can handle the URL (e.g. /file/lib/foo.bzl?sym=...).
func NewServerWithUI(idx *understory.Index, sourceRoot *os.Root) http.Handler {
	apiHandler := understory.NewServerWithSource(idx, sourceRoot)

	// Resolve the embedded web/build/ as a sub-FS so http.FileServer
	// serves files at /, not /web/build/.
	buildSub, err := fs.Sub(buildFS, "web/build")
	if err != nil {
		// embed.FS is built at compile time; this branch is unreachable
		// unless the embed directive is mis-specified. A panic surfaces
		// the programmer error loudly instead of silently 404-ing every
		// request at runtime.
		panic("understory/ui: embedded web/build missing: " + err.Error())
	}
	staticHandler := http.FileServer(http.FS(buildSub))

	indexBytes, err := fs.ReadFile(buildSub, "index.html")
	if err != nil {
		// Same compile-time guarantee as above — the stub is committed.
		panic("understory/ui: embedded index.html missing: " + err.Error())
	}

	// Validate the rewrite contract at startup, not on each request.
	// If the bundle is the committed stub OR a real SvelteKit build,
	// it must contain BOTH a `/_app/` reference and a `base: ""`
	// placeholder. A future SvelteKit upgrade that changes either
	// pattern is a silent breakage for prefix-mounted deploys —
	// crashing here surfaces it before traffic hits a half-rewritten
	// fallback. Catches regressions like `base:""` (no space) or
	// `paths.base` rendered as a non-empty literal.
	if !bytes.Contains(indexBytes, []byte("/_app/")) || !bytes.Contains(indexBytes, []byte(`base: ""`)) {
		panic("understory/ui: bundled index.html missing the patterns serveIndex rewrites — SvelteKit template upgrade?")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/* always wins regardless of UI state. The mux registered
		// under apiHandler owns the wire shape; we just forward.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}

		// Root: serve index.html (with optional prefix rewrite).
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			serveIndex(w, r, indexBytes)
			return
		}

		// SPA fallback for unknown paths: serve the (optionally
		// rewritten) index.html so SvelteKit's client router can take
		// over. Real assets in the embedded FS pass through verbatim.
		if _, err := fs.Stat(buildSub, path); err != nil {
			serveIndex(w, r, indexBytes)
			return
		}
		staticHandler.ServeHTTP(w, r)
	})
}

// serveIndex writes the embedded SvelteKit index.html, rewriting
// absolute `/_app/...` and the inline `base: ""` placeholder when the
// request carries an `X-Forwarded-Prefix` header. This lets canopy
// mount understory under `/modules/<m>/<v>/code-nav/` without the
// browser fetching modulepreload assets from the bare origin (where
// canopy's *own* SvelteKit bundle lives).
//
// HEURISTIC — byte-substring rewrite of generated HTML.
//
//	Why it exists: adapter-static's `paths.relative=true` only
//	rewrites prerendered routes; the SPA fallback ships with absolute
//	references. We need a way to make one built bundle work under any
//	mount prefix decided at runtime, without rebuilding.
//
//	Why deferred: the deterministic alternative is a build-time
//	templating pass that replaces the SvelteKit literals with named
//	placeholders (e.g. `{{__BASE__}}`), then a server-side template
//	render per request. That requires either a custom SvelteKit
//	adapter or a post-build script — meaningful build-pipeline
//	complexity for a problem the current rewrite solves in 4 lines.
//
//	Why acceptable: the input bundle is OUR output (committed stub
//	+ pnpm-built bundle from this repo, not user-supplied), and the
//	startup-time contract assertion (above) crashes loudly if a
//	SvelteKit upgrade ever changes either pattern. So the failure
//	mode is "fail fast at boot," not "ship a broken UI."
func serveIndex(w http.ResponseWriter, r *http.Request, indexBytes []byte) {
	body := indexBytes
	if prefix := r.Header.Get("X-Forwarded-Prefix"); prefix != "" && safeMountPrefix(prefix) {
		// All references to /_app/ in the bundle are URL-shaped, so a
		// byte-level replace covers <link href>, import(), and
		// <link rel=stylesheet>. The inline base placeholder is rewritten
		// alongside so the SvelteKit runtime picks the mount prefix up.
		b := bytes.ReplaceAll(indexBytes, []byte("/_app/"), []byte(prefix+"/_app/"))
		b = bytes.ReplaceAll(b, []byte(`base: ""`), []byte(`base: `+strconv.Quote(prefix)))
		body = b
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

// safeMountPrefix admits a conservative subset of URL paths:
//   - leading `/`
//   - one or more segments of `[A-Za-z0-9._-]+` separated by single `/`
//   - no trailing slash
//   - no `//` anywhere (a `//evil.example` prefix is a protocol-relative
//     URL to the browser and would load every asset from an attacker-
//     controlled origin)
//
// HEURISTIC — allow-listed character set + shape rules.
//
//	Why it exists: the X-Forwarded-Prefix value is substituted as
//	raw bytes into href= attributes and an inline script, so the
//	validator IS the markup-injection defense.
//
//	Why deferred: there's no formal spec to defer to — the prefix
//	is the URL path canopy chose to mount us under, not a SCIP
//	symbol or Bazel module name. The "correct" alternative would
//	be HTML-escape the bytes at substitution time AND restrict
//	scheme-relativity at parse-time, which is more code with the
//	same outcome.
//
//	Why acceptable: every realistic mount shape (alphanumeric +
//	dot/underscore/hyphen, slash-separated) passes; every hostile
//	shape we can think of fails. Tests cover both lanes.
func safeMountPrefix(s string) bool {
	if len(s) < 2 || s[0] != '/' || s[len(s)-1] == '/' {
		return false
	}
	// Walk the string in segments, enforcing single-`/` separators and
	// the allowed char set inside each segment.
	segStart := 1
	for i := 1; i <= len(s); i++ {
		atEnd := i == len(s)
		if atEnd || s[i] == '/' {
			if i == segStart {
				return false // empty segment → "//" or trailing "/"
			}
			segStart = i + 1
			continue
		}
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			// ok
		default:
			return false
		}
	}
	return true
}
