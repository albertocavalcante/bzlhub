package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/albertocavalcante/canopy/internal/ogimg"
)

// moduleNamePattern accepts BCR-style module names (lowercase letters,
// digits, underscores). Permissive on case — BCR is lowercase-only in
// practice but normalisation should be a caller concern, not a 4xx
// here. Critically rejects "..", "/", "\" which would otherwise let a
// crafted request escape the OG cache dir.
var moduleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// versionPattern accepts semver-ish strings (dots, dashes, alphanumerics).
// "1.0.0-rc1", "0.50.1", "29.0-rc2", "1.0.0.1" all match. "..", "/",
// "\" are rejected — `..` because the explicit check below rejects
// any string containing it; "/" and "\" because they're not in the
// allowlist.
var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// validCacheKey decides whether (module, version) is safe to use in
// a filesystem path under MirrorRoot. Defence in depth — the
// MirrorRoot prefix in ogCachePath would normally contain a `..`
// traversal anyway, but rejecting at this layer means a misconfigured
// MirrorRoot can't accidentally widen the attack surface.
func validCacheKey(module, version string) bool {
	if !moduleNamePattern.MatchString(module) {
		return false
	}
	if version != "" && !versionPattern.MatchString(version) {
		return false
	}
	if strings.Contains(module, "..") || strings.Contains(version, "..") {
		return false
	}
	return true
}

// apiOGImage serves Open Graph preview PNGs at /og/...:
//
//	/og/default.png            → generic bzlhub card
//	/og/<module>.png           → module summary (uses version_count from ModuleSummary)
//	/og/<module>/<version>.png → version-specific (uses ModuleReport for hermeticity/rules/deps)
//
// Single chi route /og/* feeds this handler — using literal extension
// matching in the chi pattern (/og/{module}/{version}.png) gets fragile
// across module names with edge characters and chi versions, so we do
// path parsing here once.
//
// Cache strategy (when h.opts.MirrorRoot is set): write PNGs to
// <mirror>/og/<module>/<version>.png and serve via http.ServeFile on
// subsequent requests. When MirrorRoot is empty (tests, transient
// deploys) we render inline every request — slow but correct.
//
// Failure mode: any error generating a module-specific card falls
// back to the generic card with HTTP 200 (unfurl crawlers don't
// retry; a generic preview beats a broken one).
func (h *handler) apiOGImage(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/og/")
	rest = strings.TrimSuffix(rest, ".png")

	// Distinguish the three shapes by segment count.
	parts := strings.Split(rest, "/")
	switch {
	case rest == "" || rest == "default":
		h.serveOGGeneric(w, r)
	case len(parts) == 1:
		h.serveOGModule(w, r, parts[0])
	case len(parts) == 2:
		h.serveOGVersion(w, r, parts[0], parts[1])
	default:
		// Deep path under /og/ — not a shape we render. Generic.
		h.serveOGGeneric(w, r)
	}
}

// serveOGGeneric returns the wordmark-only PNG. Never cached on disk —
// it's cheap to render and the cache file path would collide with
// per-module names if any operator chose "default" as a module name.
func (h *handler) serveOGGeneric(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_ = ogimg.Generic(w)
}

// serveOGModule renders the module-level card: name + version_count
// in the bottom band. Falls back to a name-only render if the index
// lookup fails (module not yet ingested, transient backend error).
func (h *handler) serveOGModule(w http.ResponseWriter, r *http.Request, module string) {
	host := hostFromRequest(r)
	spec := ogimg.Spec{ModuleName: module, Host: host}

	// Best-effort enrichment from the index. On any error (not
	// indexed, backend hiccup, etc.) we fall through to a bare-name
	// render — honest preview beats 404 for a paste-into-Slack flow.
	if h.c != nil {
		if sum, err := h.c.GetModule(r.Context(), module); err == nil && sum != nil {
			spec.ModuleVersion = sum.LatestVersion
			spec.Versions = sum.VersionCount
		}
	}

	cachePath := h.ogCachePath(module, "module")
	h.serveOGCached(w, cachePath, spec)
}

// serveOGVersion renders the per-version card with hermeticity +
// rule/dep counts pulled from the assay ModuleReport. The lookup
// is the heaviest call canopy makes — but it's cached on disk so the
// cost is paid once per (module, version) over the file's lifetime.
func (h *handler) serveOGVersion(w http.ResponseWriter, r *http.Request, module, version string) {
	host := hostFromRequest(r)
	spec := ogimg.Spec{
		ModuleName:    module,
		ModuleVersion: version,
		Host:          host,
	}

	// Same best-effort enrichment as serveOGModule — fall through to
	// a name+version render on any backend error rather than 404.
	if h.c != nil {
		if rep, err := h.c.GetModuleVersion(r.Context(), module, version); err == nil && rep != nil {
			// HermeticityProfile.Classes is a slice — a module can carry
			// multiple classifications. For the OG card pick the first
			// (assay sorts them strictest-first per its own contract).
			if len(rep.Hermeticity.Classes) > 0 {
				spec.Hermeticity = string(rep.Hermeticity.Classes[0])
			}
			spec.RuleCount = len(rep.Rules)
			spec.DepCount = len(rep.BazelDeps)
		}
	}

	cachePath := h.ogCachePath(module, version)
	h.serveOGCached(w, cachePath, spec)
}

// serveOGCached writes the rendered PNG to disk under the mirror
// tree (if MirrorRoot is configured) so subsequent requests serve
// via http.ServeFile. The render-and-serve path is wrapped in a
// fallback to ogimg.Generic so the unfurl crawler never sees a 500.
func (h *handler) serveOGCached(w http.ResponseWriter, cachePath string, spec ogimg.Spec) {
	// Cached hit. Stream straight from disk via io.Copy — avoids
	// http.ServeFile's range/etag machinery that doesn't matter for
	// OG previews (unfurl crawlers fetch in one shot).
	if cachePath != "" {
		if f, err := os.Open(cachePath); err == nil {
			defer f.Close()
			if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				_, _ = io.Copy(w, f)
				return
			}
		}
	}

	// Render fresh.
	body, err := ogimg.RenderBytes(spec)
	if err != nil {
		// Render failed; serve the generic card so the response is
		// still a valid PNG. We deliberately don't 500 — unfurl
		// crawlers don't retry, and a generic preview is better than
		// nothing.
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_ = ogimg.Generic(w)
		return
	}

	// Best-effort cache write. Atomic via temp + rename so a torn
	// write never serves a corrupted PNG to the next request.
	if cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		tmp := cachePath + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(body)
}

// ogCachePath returns the on-disk cache path for an OG image, or ""
// if caching is disabled (MirrorRoot unset) or the inputs would be
// unsafe to use in a filesystem path. The keying scheme is
// intentionally coarse for v0 — a hermeticity-class change for a
// cached version requires `canopy og purge` (Plan 32 §5) to
// invalidate. Acceptable since classification rarely drifts after
// first ingest.
//
// SECURITY: validCacheKey is the path-traversal guard. Any request
// for a module name not matching `[a-zA-Z0-9_]+` or a version not
// matching `[a-zA-Z0-9._-]+` (or containing "..") returns "" here,
// which falls through to inline rendering with no disk side-effect.
func (h *handler) ogCachePath(module, version string) string {
	if h.opts.MirrorRoot == "" {
		return ""
	}
	if !validCacheKey(module, version) {
		return ""
	}
	return filepath.Join(h.opts.MirrorRoot, "og", module, version+".png")
}

// hostFromRequest mirrors originFromRequest in server.go but returns
// just the host portion (no scheme). Used to render the bottom-right
// "bzlhub.com" footer of the OG card with whatever public hostname the
// operator's request arrived on.
func hostFromRequest(r *http.Request) string {
	if xh := r.Header.Get("X-Forwarded-Host"); xh != "" {
		return xh
	}
	return r.Host
}

