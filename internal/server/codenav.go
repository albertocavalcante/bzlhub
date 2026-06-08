package server

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/understory/pkg/understory/ui"

	"github.com/albertocavalcante/bzlhub/internal/codenav"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// codeNav forwards /modules/<m>/<v>/code-nav/<rest> into the embedded
// understory.ui handler. The Resolver caches (Index, *os.Root) per
// coordinate so repeat requests skip the unpack + parse cost.
//
// Failure modes collapse to 503: a missing SCIP blob, missing tarball
// blob, or absent source.json all surface as "this coordinate isn't
// navigable" rather than 404 (the route itself does exist) or 500
// (the operator can fix it by ingesting + re-indexing).
func (h *handler) codeNav(w http.ResponseWriter, r *http.Request) {
	if h.codenav == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "code-nav not configured: --root and --db are both required",
		})
		return
	}
	module := chi.URLParam(r, "module")
	version := chi.URLParam(r, "version")
	idx, root, err := h.codenav.Resolve(r.Context(), module, version)
	if err != nil {
		// Specific case: the (module, version) coordinate isn't in our
		// SCIP index. The user got here by following a cross-module
		// link (e.g. rules_kotlin pins rules_java@7.2.0 but we have
		// rules_java@8.6.1 indexed), so they're in a browser following
		// what looks like a real URL — serve an HTML page they can
		// read, not a JSON blob the browser displays as raw text.
		// 404 is the right semantic: "this coordinate doesn't exist."
		if errors.Is(err, store.ErrScipNotFound) {
			h.codeNavNotIndexed(w, module, version)
			return
		}
		h.log.Warn("codenav resolve failed", "module", module, "version", version, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": err.Error(),
		})
		return
	}

	// understory.ui mounts /api/* + a SvelteKit static tree at /. Strip
	// the route prefix so the inner mux sees the same path shape it
	// would under understory's own server binary, and forward the prefix
	// via X-Forwarded-Prefix so the SPA fallback rewrites its absolute
	// /_app/ asset URLs accordingly (otherwise the browser would fetch
	// /_app/* from canopy's origin, which serves canopy's *own* bundle).
	prefix := "/modules/" + module + "/" + version + "/code-nav"
	handler := ui.NewServerWithUI(idx, root)
	stripped := http.StripPrefix(prefix, handler)
	r.Header.Set("X-Forwarded-Prefix", prefix)

	// http.StripPrefix returns 404 if Path doesn't start with prefix —
	// but chi only routes /code-nav/* here, so /code-nav (no trailing
	// slash) wouldn't match. Normalize by rewriting to "/" before
	// forwarding, mirroring how SPAs deep-link.
	if r.URL.Path == prefix {
		// 308 keeps the method (GET stays GET); browsers follow it.
		http.Redirect(w, r, prefix+"/", http.StatusPermanentRedirect)
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/") {
		http.NotFound(w, r)
		return
	}
	stripped.ServeHTTP(w, r)
}

// codeNavNotIndexed renders a small HTML 404 page when a coordinate
// has no SCIP blob in the index. Reachable when a user clicks a cross-
// module symbol whose target version we haven't ingested (a real and
// common case once the corpus is non-trivial — rules_python pinning
// rules_java@7.2.0 while we have rules_java@8.6.1, for example).
//
// The page links to `/modules/<m>` (canopy's existing module landing)
// so the user can pick a version we *do* have. html/template handles
// escaping of module/version values, defending against any pathological
// URL params chi might forward (chi already URL-decodes — this is a
// defense-in-depth measure).
func (h *handler) codeNavNotIndexed(w http.ResponseWriter, module, version string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_ = codeNavNotIndexedTmpl.Execute(w, struct{ Module, Version string }{module, version})
}

// codeNavLatest handles `/modules/<m>/code-nav[/<rest>]` — the
// version-less code-nav URL. We resolve the latest indexed version
// for the module and 302 to the versioned URL.
//
// Why 302 (not 301): the "latest" target moves as the corpus
// receives new ingests; we don't want browsers/proxies to cache the
// redirect target forever. 302 is the semantically-correct "found
// at this other URL for now."
//
// Edge cases:
//   - Module isn't indexed at all → friendly 404 page naming the
//     module (mirrors the existing not-indexed shape).
//   - Module is indexed but listing the versions fails → 503.
//   - Module is indexed but has zero versions (impossible in
//     practice; defensive) → friendly 404.
func (h *handler) codeNavLatest(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	rest := chi.URLParam(r, "*")
	versions, err := h.c.ListVersions(r.Context(), module)
	if err != nil {
		h.apiError(w, err)
		return
	}
	if len(versions) == 0 {
		// No versions of this module exist. Render the "not
		// indexed" page naming the user's intent (module + a
		// generic "latest" tag). Friendly + same theme.
		h.codeNavNotIndexed(w, module, "(no indexed versions)")
		return
	}
	// ListVersions sorts descending lexically, which matches what
	// the UI surfaces as "latest." See store.ListVersions.
	latest := versions[0]
	target := "/modules/" + url.PathEscape(module) +
		"/" + url.PathEscape(latest) + "/code-nav/"
	if rest != "" {
		target += rest
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// codeNavNotIndexedTmpl matches canopy's main SvelteKit UI palette
// (dark zinc/blue) so the friendly 404 doesn't visually jar against
// the rest of the site. The colors are pinned hex rather than CSS
// vars because this template renders without the SvelteKit app shell
// (no Tailwind, no theme tokens loaded).
var codeNavNotIndexedTmpl = template.Must(template.New("not-indexed").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>{{.Module}} @ {{.Version}} — not indexed | canopy</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 0; background: #0a0a0a; color: #e4e4e7; min-height: 100vh; }
    main { max-width: 36rem; margin: 5rem auto; padding: 0 1rem; line-height: 1.55; }
    h1 { font-size: 1.4rem; margin-bottom: 0.25rem; color: #fafafa; }
    h1 small { color: #71717a; font-weight: 400; font-size: 0.9rem; }
    code { background: #18181b; padding: 0.1rem 0.35rem; border-radius: 4px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; color: #fafafa; border: 1px solid #27272a; }
    p { margin: 0.8rem 0; }
    a { color: #60a5fa; text-decoration: none; }
    a:hover { text-decoration: underline; }
    .actions { margin-top: 1.6rem; display: flex; gap: 0.6rem; flex-wrap: wrap; }
    .btn { display: inline-block; padding: 0.5rem 0.9rem; border: 1px solid #3b82f6; border-radius: 6px; color: #60a5fa; }
    .btn-primary { background: #3b82f6; color: #fafafa; border-color: #3b82f6; }
    .btn:hover { text-decoration: none; background: #1e293b; }
    .btn-primary:hover { background: #2563eb; }
    .why { color: #a1a1aa; font-size: 0.9rem; margin-top: 2rem; border-top: 1px solid #27272a; padding-top: 1rem; }
    .brand { color: #60a5fa; font-weight: 600; font-size: 0.9rem; padding: 1rem; }
  </style>
</head>
<body>
  <div class="brand"><a href="/" style="color:#60a5fa">canopy</a></div>
  <main>
    <h1>Not indexed yet <small>· 404</small></h1>
    <p>Canopy doesn't have a code-nav index for <code>{{.Module}} @ {{.Version}}</code>.</p>
    <p class="why">
      You likely got here by following a cross-module symbol whose target version
      isn't in our catalogue yet. The module pinning may reference an older release
      than what canopy has ingested.
    </p>
    <div class="actions">
      <a class="btn btn-primary" href="/modules/{{.Module}}">See indexed versions of {{.Module}} →</a>
      <a class="btn" href="/">Back to canopy</a>
    </div>
  </main>
</body>
</html>
`))

// codenavResolver constructs the package-internal type the handler
// expects out of the options + the canopy service. nil when MirrorRoot
// is empty (no mirror to read tarballs from) OR SourcesCacheDir is
// empty (nowhere to extract). The handler treats nil as "feature not
// available, return 503".
func codenavResolver(c any, opts Options) *codenav.Resolver {
	if opts.MirrorRoot == "" || opts.SourcesCacheDir == "" {
		return nil
	}
	br, ok := c.(codenav.BlobReader)
	if !ok || br == nil {
		return nil
	}
	return codenav.NewResolver(br, opts.MirrorRoot, opts.SourcesCacheDir)
}
