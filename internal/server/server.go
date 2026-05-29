// Package server serves both the BCR HTTP protocol (so Bazel can fetch
// modules) and canopy's own /api/* routes (so the web UI, agents, and CLI
// can query the index).
//
// BCR endpoints are projections of backend.Backend; /api endpoints are
// projections of api.Canopy. Either may be nil; the corresponding routes
// just become unavailable.
package server

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/api/paths"
	"github.com/albertocavalcante/canopy/internal/backend"
	"github.com/albertocavalcante/canopy/internal/bcrprobe"
	"github.com/albertocavalcante/canopy/internal/codenav"
	"github.com/albertocavalcante/canopy/internal/embed"
	"github.com/albertocavalcante/canopy/internal/featureflags"
	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/ratelimit"
	"github.com/albertocavalcante/canopy/internal/server/headtags"
	"github.com/albertocavalcante/canopy/internal/server/sitemap"
)

// originFromRequest reconstructs the scheme+host the client used to
// reach us. Honours X-Forwarded-Proto from configured trusted edges
// (CANOPY_TRUSTED_PROXY_CIDR — same gate as the auth middleware uses)
// so SEO canonical URLs reflect the public origin, not the internal
// http:// the reverse proxy talks to us on.
func originFromRequest(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// The reverse proxy almost always terminates TLS; trust its
	// X-Forwarded-Proto unconditionally for SEO purposes. Header
	// forgery would, at worst, produce a canonical URL with a wrong
	// scheme — annoying but not a security issue (canonical isn't
	// used for redirects or auth).
	if xp := r.Header.Get("X-Forwarded-Proto"); xp != "" {
		scheme = xp
	}
	host := r.Host
	if xh := r.Header.Get("X-Forwarded-Host"); xh != "" {
		host = xh
	}
	return scheme + "://" + host
}

// Options configures non-default behavior of the constructed handler.
type Options struct {
	// MirrorBaseURL, when non-empty, makes canopy advertise itself as a
	// tarball mirror via bazel_registry.json.mirrors. Bazel takes each
	// upstream archive URL, strips the scheme, and prepends this base
	// to construct the mirror request. /m/<host+path> is the handler
	// that looks up the corresponding content-addressed blob.
	//
	// Example: --mirror-base-url "http://canopy.local:8080/m/"
	MirrorBaseURL string

	// MirrorRoot is the filesystem path used to build the URL→blob
	// index when MirrorBaseURL is set. Defaults to the backend's root
	// when it can be derived; otherwise required.
	MirrorRoot string

	// SourcesCacheDir is the on-disk cache for per-(module, version)
	// extracted tarball trees used by the code-nav handler. Each
	// requested coordinate lazily unpacks into
	// SourcesCacheDir/<module>/<version>/. When empty, the
	// /modules/<m>/<v>/code-nav/* route stays registered but returns
	// 503 — code-nav requires both MirrorRoot (for tarball blobs) and
	// this cache directory.
	//
	// Compile-time defaults live in cmd/canopy/main.go; tests pass a
	// t.TempDir() here. Not a CLI flag: operators tune via the deploy
	// volume layout, not a runtime knob.
	SourcesCacheDir string

	// Flags is the parsed feature-flag set. The zero value is safe
	// (everything disabled / defaults), but production should always
	// pass an explicit featureflags.Parse() result. Tests can build
	// a literal in-line.
	Flags featureflags.Flags

	// TrustedProxyCIDRs lists source-IP ranges from which canopy
	// honors X-Forwarded-User / -Email / -Groups headers (set by a
	// reverse proxy doing OIDC/SSO termination). Empty disables
	// header-based auth entirely — requests stay anonymous. See
	// docs/plans/08-corporate-security.md § "Authentication model".
	TrustedProxyCIDRs []*net.IPNet

	// Helper supplies the read-side queries that don't live on the
	// cross-transport api.Canopy contract (per-row metadata,
	// adoption counts, GitHub-meta). Wired by main from
	// *canopy.Service. Nil disables every augmentation cleanly —
	// the responses degrade to "plain report" shape rather than
	// erroring, matching pre-helper behavior.
	Helper ReadHelper
}

// New constructs an http.Handler. Either b or c can be nil for partial
// deployments; the corresponding routes are simply not registered.
func New(b backend.Backend, c api.Canopy, logger *slog.Logger) http.Handler {
	return NewWithOptions(b, c, logger, Options{})
}

// NewWithOptions is New with non-default behavior controlled by Options.
func NewWithOptions(b backend.Backend, c api.Canopy, logger *slog.Logger, opts Options) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{b: b, c: c, helper: opts.Helper, log: logger, opts: opts}
	if opts.MirrorBaseURL != "" && opts.MirrorRoot != "" {
		h.mirrorIdx = newMirrorIndex(opts.MirrorRoot)
	}
	h.codenav = codenavResolver(c, opts)
	h.ingestLimiter = ratelimit.New(ratelimit.Options{
		PerMin:        opts.Flags.IngestRateLimitPerMin,
		BypassIPs:     opts.Flags.IngestRateBypassIPs,
		MaxConcurrent: opts.Flags.IngestMaxConcurrent,
	})
	h.bcrFetch = fetch.NewClient()
	r := chi.NewRouter()
	r.Use(accessLog(logger))
	// Header-based auth scaffold. No-op when TrustedProxyCIDRs is
	// empty (default for personal-canopy installs). When configured,
	// reads X-Forwarded-User/Email/Groups from requests originating
	// in the trusted CIDR block and attaches auth.Identity to ctx.
	r.Use(headerAuth(opts.TrustedProxyCIDRs))

	if b != nil {
		r.Get("/bazel_registry.json", h.bazelRegistry)
		r.Get("/modules/{module}/metadata.json", h.metadata)
		r.Get("/modules/{module}/{version}/MODULE.bazel", h.moduleBazel)
		r.Get("/modules/{module}/{version}/source.json", h.sourceJSON)
		r.Get("/modules/{module}/{version}/patches/{filename}", h.patch)
		r.Get("/modules/{module}/{version}/overlay/*", h.overlay)
		r.Get("/blobs/{key}", h.blob)
		if h.mirrorIdx != nil {
			r.Get("/m/*", h.mirrorServe)
		}
	}

	if c != nil {
		// /api/v1 — coherent path shape locked in by docs/plans/13-api-rename.md
		// and structured as nested chi sub-routers per Plan 15
		// (internal/api/paths). Rules:
		//   - all routes under /api/v1/
		//   - collections plural; details singular
		//   - sub-resources get real sub-routers (closure/, airgap/)
		//   - RPC verbs live under /actions/
		//   - representation switch is ?format= only (no .md extension)
		//   - system/admin endpoints under /system/
		//   - activity/observability under /activity/
		//
		// Path STRINGS live in internal/api/paths/paths.go and are
		// mirrored on the UI side at ui/src/lib/api/paths.ts. This
		// file uses inline literals for the chi placeholder shape
		// because mounting via r.Route() requires positional segments
		// — the paths.Pat* constants represent the same shape and are
		// safe references; the equivalence is asserted in
		// paths_test.go.
		r.Route(paths.Prefix, func(r chi.Router) {
			r.Get("/search", h.apiSearch)
			r.Get("/xrefs", h.apiXRefs)
			r.Get("/drift", h.apiDrift)
			// Plan 16 F3: federation state introspection. Registered
			// inside the c != nil block because it lives next to
			// other service-bound routes; the handler itself only
			// reads h.b so it works fine when c is nil too.
			r.Get("/upstreams", h.apiGetUpstreams)

			r.Route("/modules", func(r chi.Router) {
				r.Get("/", h.apiListModules)
				r.Route(paths.PatModule, func(r chi.Router) {
					r.Get("/", h.apiGetModule)
					r.Get("/versions", h.apiListVersions)
					r.Get("/diff", h.apiDiff)
					r.Get("/diff/closure", h.apiDiffClosure)
					r.Route(paths.PatVersion, func(r chi.Router) {
						r.Get("/", h.apiGetVersion)
						r.Get("/external", h.apiGetExternalSurface)
						r.Get("/scip", h.apiGetScip)
						r.Get("/docs", h.apiGetDocs)
						r.Get("/example-files", h.apiGetExampleFiles)
						r.Route("/closure", func(r chi.Router) {
							r.Get("/graph", h.apiGetClosureGraph)
							r.Get("/reverse-deps", h.apiGetReverseDeps)
						})
						// Plan 07: cross-corpus consumer view.
						r.Get("/consumers/{name}", h.apiGetConsumers)
						r.Route("/airgap", func(r chi.Router) {
							r.Get("/surface", h.apiGetAirgapSurface)
							r.Get("/downloader-config", h.apiGetAirgapDownloaderConfig)
							r.Get("/module-mirrors", h.apiGetAirgapModuleMirrors)
						})
					})
				})
			})

			r.Route("/actions", func(r chi.Router) {
				r.Post("/bump", h.apiBump)
				r.Post("/compat-check", h.apiCompatCheck)
				r.Post("/ingest/recursive", h.apiIngestRecursive)
				r.Post("/modules"+paths.PatModuleVersion+"/ingest-missing", h.apiIngestMissing)
			})

			r.Route("/activity", func(r chi.Router) {
				r.Get("/history", h.apiHistory)
				r.Get("/events", h.apiEvents)
			})
		})
	}

	// Per-(module, version) code navigation. The route is registered
	// unconditionally so deploy-time changes (mounting a sources cache
	// volume, ingesting a SCIP blob) take effect without restart. When
	// the resolver isn't wired (no MirrorRoot or no SourcesCacheDir),
	// the handler returns a 503 with a descriptive JSON error.
	//
	// Mounted AFTER the explicit BCR endpoints so the more-specific
	// routes (source.json, MODULE.bazel) win, and BEFORE the SPA
	// fallback so the wildcard isn't swallowed by the UI handler.
	r.Get("/modules/{module}/{version}/code-nav", h.codeNav)
	r.Get("/modules/{module}/{version}/code-nav/*", h.codeNav)
	// Version-less code-nav: redirect to the latest indexed version.
	// "/modules/<m>/code-nav[/...]" is the canonical "show me this
	// module's source, pick a version for me" URL — useful from
	// memory ("I want to see rules_shell") and from external links
	// that don't pin a version. Without this route the SPA fallback
	// would catch the URL and SvelteKit would render a client-side
	// 404, leaving the user stuck.
	if c != nil {
		r.Get("/modules/{module}/code-nav", h.codeNavLatest)
		r.Get("/modules/{module}/code-nav/*", h.codeNavLatest)
	}

	// /healthz is the liveness probe target: "process is alive".
	// Never blocks on dependencies — answering 200 means kill-and-
	// restart wouldn't help. /readyz is the readiness probe target:
	// "ready to serve traffic". Today both answer identically because
	// the router only finishes registering after server.New returns,
	// at which point any disk/db init has completed; the distinction
	// exists so operators can wire stricter readiness checks later
	// (e.g. block readiness during rehydration) without changing the
	// liveness contract.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// /api/v1/system/* — admin + introspection. Unconditional (no
	// service deps for /version, /features; bcr-probe is a read-only
	// network call that doesn't need the index either). Registered
	// outside the c != nil block so deploys without an index still
	// expose /system/version etc.
	r.Route(paths.Prefix+"/system", func(r chi.Router) {
		// /version mirrors understory's contract (same JSON shape +
		// field names) so tooling can probe either app with one
		// parser.
		r.Get("/version", h.apiVersion)
		// /features publishes the UI-visible feature-flag snapshot.
		// The UI hits it on every load to decide whether to render
		// write affordances (the "Ingest from BCR" button, eventually
		// others). Never exposes server-internal flags (registry URL,
		// bypass IPs, concurrency caps).
		r.Get("/features", h.apiFeatures)
		// /bcr-probe answers "does this (module, version) exist on
		// the configured upstream?" — the friendly-404 page calls it
		// before showing the Ingest button so the UI can render an
		// honest answer instead of a hopeful one. Read-only network
		// call; no flags, no rate limit — the upstream BCR's own
		// caching is the relevant defense.
		r.Get("/bcr-probe", h.apiBCRProbe)
	})

	// /robots.txt — allow-all + sitemap pointer. The sitemap URL is
	// origin-relative so self-hosters get a correctly-rooted pointer
	// without per-deploy config.
	r.Get("/robots.txt", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		body := "User-agent: *\nAllow: /\nSitemap: " + originFromRequest(req) + "/sitemap.xml\n"
		_, _ = w.Write([]byte(body))
	})

	// /sitemap.xml — every indexed module+version, plus static pages.
	// Streamed fresh on each request (corpus is small; cache-on-disk
	// adds invalidation complexity for negligible CPU win). When
	// traffic grows past where ~1-2ms of regen per request matters,
	// wrap in a TTL cache here.
	r.Get("/sitemap.xml", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		// Sitemap.Stream never 500s on canopy errors — it emits what
		// it can and continues. A partial sitemap is more useful to
		// a crawler than a 5xx.
		_ = sitemap.Stream(req.Context(), c, originFromRequest(req), w)
	})

	// /og/* — per-URL Open Graph image generator (plan-32). Wildcard
	// route + path parsing in the handler so module names with edge
	// characters don't trip chi's literal-extension matching.
	// Cards cached to <MirrorRoot>/og/<module>/<version>.png on first
	// request; subsequent requests serve via io.Copy. Generic fallback
	// always returns 200 — unfurl crawlers don't retry.
	r.Get("/og/*", h.apiOGImage)

	// SPA fallback: any unmatched path serves the embedded SvelteKit UI
	// (or, if the embed is empty, a polite "UI not built" page). /api/*
	// is excluded — unknown API routes get a JSON 404 instead of the
	// SPA shell so machine consumers see a structured error.
	//
	// The transform composes per-URL SEO <head> tags (title, description,
	// canonical, Open Graph, Twitter Card) and injects them into the
	// HEADTAGS-SENTINEL placeholder in app.html. This makes module pages
	// indexable + share-previewable despite the SPA being client-side-only
	// rendered. See internal/server/headtags/.
	spa := embed.HandlerWithTransform(func(req *http.Request, htmlBody []byte) []byte {
		tags := headtags.Compose(req.Context(), req.URL.Path, originFromRequest(req), c)
		return headtags.Inject(htmlBody, tags)
	})
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		spa.ServeHTTP(w, req)
	})

	return r
}

type handler struct {
	b             backend.Backend
	c             api.Canopy
	helper        ReadHelper // may be nil; see Options.Helper
	log           *slog.Logger
	opts          Options
	mirrorIdx     *mirrorIndex      // nil unless opts.MirrorBaseURL is set
	codenav       *codenav.Resolver // nil unless MirrorRoot + SourcesCacheDir are both set
	ingestLimiter *ratelimit.IngestLimiter
	bcrFetch      bcrprobe.Prober
}
