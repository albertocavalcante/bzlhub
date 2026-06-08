// Package headtags composes per-URL <head> tags (title, description,
// canonical link, Open Graph, Twitter Card) for canopy's SPA pages
// and injects them into index.html at the SPA fallback handler.
//
// canopy ships its UI via @sveltejs/adapter-static with ssr=false +
// prerender=false — pure client-side rendering. Search engines see
// the unhydrated <html> shell; Slack/Twitter/Discord/Mastodon unfurl
// crawlers don't execute JS at all, so <svelte:head> tags inside
// Svelte components never reach them.
//
// This package fixes that. The Go server intercepts SPA requests,
// matches the path against a small route table, builds a Tags struct
// from canopy's already-indexed state (or a static fallback), and
// rewrites the <!-- HEADTAGS-SENTINEL --> comment in index.html with
// real meta tags before streaming the response. The SvelteKit app
// then boots normally; the SEO tags are already in the DOM by the
// time hydration runs.
//
// Design intent:
//   - Pure functions only. No goroutines, no caching beyond what the
//     caller's http.Handler already does.
//   - Backend-shaped data → frontend-shaped HTML. This package owns
//     the HTML escaping discipline once; callers don't have to think
//     about it.
//   - Self-host friendly. site_origin is derived from the request
//     itself, not a hard-coded bzlhub.com — anyone running canopy
//     gets correct canonical URLs without config.
package headtags

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// ogImageURL builds the absolute URL of the OG card for an
// optional (module, version) pair. Path segments are URL-escaped
// defensively even though BCR module names are constrained to
// URL-safe characters in practice — keeps the helper future-proof
// if a non-BCR registry ever indexes modules with edge characters.
//
// Centralising the URL scheme here means changes to the /og/* route
// (currently in internal/server/og_handlers.go) only need updating
// in one place.
func ogImageURL(origin, module, version string) string {
	if module == "" {
		return origin + "/og/default.png"
	}
	base := origin + "/og/" + url.PathEscape(module)
	if version != "" {
		base += "/" + url.PathEscape(version)
	}
	return base + ".png"
}

// Tags is the rendered per-page meta block. Empty fields are omitted
// from the output.
type Tags struct {
	Title       string // <title> and og:title
	Description string // <meta name="description"> and og:description
	Canonical   string // <link rel="canonical">
	OGType      string // og:type — defaults to "website"
	OGURL       string // og:url — usually same as canonical
	SiteName    string // og:site_name — defaults to "bzlhub"
	OGImage     string // og:image + twitter:image; absolute URL to a 1200x630 PNG. When non-empty, twitter:card upgrades from "summary" to "summary_large_image".
	JSONLD      string // <script type="application/ld+json">…</script> block. Should already be a valid JSON string when populated; Render handles the <script> wrapping + the </script> safety escape.
}

// Compose returns Tags for the given URL path. The Canopy interface
// is optional; pass nil for paths that don't need per-module lookups
// (eg /, /about, /drift). When non-nil, /modules/<name> and
// /modules/<name>/<version> get module-specific titles + descriptions.
//
// origin is the scheme+host the request arrived on (eg
// "https://bzlhub.com"); used to build absolute canonical URLs without
// hard-coding any one deployment.
func Compose(ctx context.Context, path, origin string, c api.Canopy) Tags {
	t := Tags{
		OGType:   "website",
		SiteName: "bzlhub",
	}

	switch {
	case path == "/" || path == "":
		t.Title = "bzlhub — Bazel module registry with introspection"
		t.Description = "bzlhub mirrors the Bazel Central Registry with structured introspection, hermeticity intelligence, and an MCP surface for coding agents."
		t.Canonical = origin + "/"
		t.OGImage = ogImageURL(origin, "", "")

	case path == "/modules" || path == "/modules/":
		t.Title = "Modules · bzlhub"
		t.Description = "Browse every Bazel module indexed by bzlhub — with hermeticity, drift, and dependency intelligence."
		t.Canonical = origin + "/modules"
		t.OGImage = ogImageURL(origin, "", "")

	case strings.HasPrefix(path, "/modules/"):
		// Two shapes: /modules/<name> and /modules/<name>/<version>[/subpath]
		// Sub-paths under a version (eg /docs, /testing, /external) reuse
		// the version's metadata since the page identity is still
		// "this module at this version, viewed through facet X".
		rest := strings.TrimPrefix(path, "/modules/")
		parts := strings.SplitN(rest, "/", 3)
		switch {
		case len(parts) == 1 && parts[0] != "":
			t = moduleTags(ctx, parts[0], "", origin, c)
		case len(parts) >= 2 && parts[0] != "" && parts[1] != "":
			t = moduleTags(ctx, parts[0], parts[1], origin, c)
		default:
			// Bare /modules/ with trailing slash already handled above;
			// anything else here is a malformed URL. Fall back to the
			// /modules landing tags so the page still indexes sensibly.
			t.Title = "Modules · bzlhub"
			t.Description = "Browse every Bazel module indexed by bzlhub."
			t.Canonical = origin + "/modules"
			t.OGImage = ogImageURL(origin, "", "")
		}

	case path == "/drift":
		t.Title = "Drift · bzlhub"
		t.Description = "Modules in this bzlhub instance that have fallen behind their upstream BCR versions."
		t.Canonical = origin + "/drift"
		t.OGImage = ogImageURL(origin, "", "")

	case path == "/history":
		t.Title = "History · bzlhub"
		t.Description = "Ingest + drift activity feed for this bzlhub instance."
		t.Canonical = origin + "/history"
		t.OGImage = ogImageURL(origin, "", "")

	case path == "/compat-check":
		t.Title = "Compatibility check · bzlhub"
		t.Description = "Validate Bazel module version compatibility against bzlhub's structured surface."
		t.Canonical = origin + "/compat-check"
		t.OGImage = ogImageURL(origin, "", "")

	case path == "/about":
		// Description matches the first paragraph of
		// ui/src/lib/content/about.md verbatim — the locked content
		// per plan-65 v2 §Part 2 is the source of truth for both.
		t.Title = "About · bzlhub"
		t.Description = "bzlhub mirrors the Bazel Central Registry and adds an indexing layer: search, hermeticity classification per module, drift detection against upstream, source-code navigation across modules, and a Model Context Protocol endpoint for coding agents to query."
		t.Canonical = origin + "/about"
		t.OGImage = ogImageURL(origin, "", "")

	case path == "/status":
		// Live operational snapshot — drives a low-cardinality,
		// honest-empty-state page (plan-65 v2 §Part 3). Description
		// stays factual; the page itself is the proof.
		t.Title = "Status · bzlhub"
		t.Description = "Live status for the bzlhub.com instance — mirror freshness, federation reachability, drift, addons."
		t.Canonical = origin + "/status"
		t.OGImage = ogImageURL(origin, "", "")

	case path == "/mcp" || strings.HasPrefix(path, "/mcp?"):
		// /mcp SPA page (plan-19 Idea E) — agent integration guide
		// for the Streamable HTTP MCP transport mounted at /mcp.
		// The transport itself is registered explicitly in server.go
		// when BZLHUB_MCP_HTTP_ENABLED is on, taking precedence over
		// this SPA fallback for POST/GET — this case only fires for
		// the GET /mcp that lands in the SPA's NotFound handler.
		t.Title = "MCP · bzlhub"
		t.Description = "Wire any MCP-capable coding agent (Claude Code, Cursor, Codex) to bzlhub's module index via the Streamable HTTP transport at /mcp."
		t.Canonical = origin + "/mcp"
		t.OGImage = ogImageURL(origin, "", "")

	default:
		// Unknown route — keep generic defaults; index.html's static
		// title + description still apply.
		t.Title = "bzlhub — Bazel module registry with introspection"
		t.Description = "bzlhub mirrors the Bazel Central Registry with structured introspection, hermeticity intelligence, and an MCP surface for coding agents."
		t.Canonical = origin + path
		t.OGImage = ogImageURL(origin, "", "")
	}

	if t.OGURL == "" {
		t.OGURL = t.Canonical
	}
	return t
}

// moduleTags builds the per-module (and per-version-when-set) tag set.
// The Canopy interface is consulted opportunistically: if the lookup
// fails (module not indexed, or canopy is nil), we still emit a
// reasonable title from the name + version so the page is indexable.
func moduleTags(ctx context.Context, name, version, origin string, c api.Canopy) Tags {
	t := Tags{
		OGType:   "website",
		SiteName: "bzlhub",
	}
	if version != "" {
		t.Title = fmt.Sprintf("%s@%s · bzlhub", name, version)
		t.Canonical = origin + "/modules/" + name + "/" + version
		t.OGImage = ogImageURL(origin, name, version)
	} else {
		t.Title = fmt.Sprintf("%s · bzlhub", name)
		t.Canonical = origin + "/modules/" + name
		t.OGImage = ogImageURL(origin, name, "")
	}

	// Try to enrich description from the indexed module summary.
	// When canopy is nil or the module isn't indexed, fall back to a
	// generic but accurate description — the title alone still beats
	// what we had before (literally "canopy" on every page).
	if c != nil {
		if sum, err := c.GetModule(ctx, name); err == nil && sum != nil {
			if version != "" {
				t.Description = fmt.Sprintf(
					"%s at version %s — Bazel module rules, providers, hermeticity, and dependency graph indexed by bzlhub.",
					name, version,
				)
				// JSON-LD SoftwareSourceCode for the version page —
				// makes the result eligible for Google's rich-results
				// treatment (license, version, repo, language facets).
				t.JSONLD = buildModuleVersionJSONLD(name, version, t.Canonical, t.Description, sum)
			} else {
				t.Description = fmt.Sprintf(
					"%s — Bazel module with %d versions indexed by bzlhub. Latest: %s.",
					name, sum.VersionCount, sum.LatestVersion,
				)
			}
			return t
		}
	}

	// Fallback: no index hit (module hasn't been ingested yet, or
	// canopy is nil). Generic description still names the module.
	if version != "" {
		t.Description = fmt.Sprintf("%s@%s — Bazel module on bzlhub.", name, version)
	} else {
		t.Description = fmt.Sprintf("%s — Bazel module on bzlhub.", name)
	}
	return t
}

// Render emits the HTML <meta>/<link> block that replaces the
// <!-- HEADTAGS-SENTINEL --> comment in index.html. The output
// always overrides app.html's static <title> by emitting a fresh
// one. Empty fields are skipped so we don't emit `content=""`.
func (t Tags) Render() string {
	var b strings.Builder
	// We emit a fresh <title> here AND leave the static one in
	// app.html (the static one stands in for `pnpm dev` and for any
	// path the route table doesn't know — both cases skip injection).
	// Browsers honour the last <title>; this one wins when injected.
	if t.Title != "" {
		fmt.Fprintf(&b, "    <title>%s</title>\n", html.EscapeString(t.Title))
	}
	if t.Description != "" {
		fmt.Fprintf(&b, "    <meta name=\"description\" content=\"%s\" />\n", html.EscapeString(t.Description))
	}
	if t.Canonical != "" {
		fmt.Fprintf(&b, "    <link rel=\"canonical\" href=\"%s\" />\n", html.EscapeString(t.Canonical))
	}
	if t.Title != "" {
		fmt.Fprintf(&b, "    <meta property=\"og:title\" content=\"%s\" />\n", html.EscapeString(t.Title))
	}
	if t.Description != "" {
		fmt.Fprintf(&b, "    <meta property=\"og:description\" content=\"%s\" />\n", html.EscapeString(t.Description))
	}
	if t.OGType != "" {
		fmt.Fprintf(&b, "    <meta property=\"og:type\" content=\"%s\" />\n", html.EscapeString(t.OGType))
	}
	if t.OGURL != "" {
		fmt.Fprintf(&b, "    <meta property=\"og:url\" content=\"%s\" />\n", html.EscapeString(t.OGURL))
	}
	if t.SiteName != "" {
		fmt.Fprintf(&b, "    <meta property=\"og:site_name\" content=\"%s\" />\n", html.EscapeString(t.SiteName))
	}
	if t.OGImage != "" {
		// Open Graph image — Slack/Discord/LinkedIn unfurl crawlers
		// honour this; the 1200x630 PNG is rendered by the /og/* handler
		// in og_handlers.go. og:image:width + og:image:height are
		// optional but speed up unfurls that pre-allocate layout.
		fmt.Fprintf(&b, "    <meta property=\"og:image\" content=\"%s\" />\n", html.EscapeString(t.OGImage))
		fmt.Fprintf(&b, "    <meta property=\"og:image:width\" content=\"1200\" />\n")
		fmt.Fprintf(&b, "    <meta property=\"og:image:height\" content=\"630\" />\n")
	}
	if t.Title != "" {
		// twitter:card upgrades to summary_large_image when we have
		// an image to show; otherwise stay on the smaller summary card.
		card := "summary"
		if t.OGImage != "" {
			card = "summary_large_image"
		}
		fmt.Fprintf(&b, "    <meta name=\"twitter:card\" content=\"%s\" />\n", card)
		fmt.Fprintf(&b, "    <meta name=\"twitter:title\" content=\"%s\" />\n", html.EscapeString(t.Title))
	}
	if t.Description != "" {
		fmt.Fprintf(&b, "    <meta name=\"twitter:description\" content=\"%s\" />\n", html.EscapeString(t.Description))
	}
	if t.OGImage != "" {
		fmt.Fprintf(&b, "    <meta name=\"twitter:image\" content=\"%s\" />\n", html.EscapeString(t.OGImage))
	}
	if t.JSONLD != "" {
		// HTML5 rules: a <script type="application/ld+json"> block is
		// raw text up to the first "</script>" — so any `</` inside the
		// JSON must be escaped. JSON allows \uXXXX sequences inside
		// string values, so replacing `<` with < is both safe and
		// JSON-valid. Done unconditionally on the whole block; the
		// price is negligible vs the cost of getting it wrong.
		safe := strings.ReplaceAll(t.JSONLD, "<", `<`)
		fmt.Fprintf(&b, "    <script type=\"application/ld+json\">%s</script>\n", safe)
	}
	return b.String()
}

// buildModuleVersionJSONLD composes the SoftwareSourceCode schema for
// a /modules/<name>/<version> page. Returns "" if marshalling fails
// (defensive — should never happen with the field shapes here).
//
// Fields per plan-33 §2:
//   - name, version, url: from request path / canonical
//   - description: matches the <meta description> text
//   - programmingLanguage: Bazel + Starlark (universal floor; further
//     languages would require ModuleReport which moduleTags doesn't fetch)
//   - codeRepository: from ModuleSummary.RepoLabel when present (form is
//     "owner/repo"); skipped if absent — never guessed
//   - datePublished: ModuleSummary.LatestIngestedAt (when bzlhub first
//     saw this module; the closest signal we have without ModuleReport)
//   - license: deliberately omitted (plan-33 Q62 — never claim an
//     unverified license)
func buildModuleVersionJSONLD(name, version, canonical, description string, sum *api.ModuleSummary) string {
	type computerLanguage struct {
		Type string `json:"@type"`
		Name string `json:"name"`
	}
	doc := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "SoftwareSourceCode",
		"name":        name,
		"version":     version,
		"url":         canonical,
		"description": description,
		"programmingLanguage": []computerLanguage{
			{Type: "ComputerLanguage", Name: "Bazel"},
			{Type: "ComputerLanguage", Name: "Starlark"},
		},
	}
	if sum != nil {
		if sum.RepoLabel != "" {
			// RepoLabel is "owner/repo" form (per ModuleSummary doc
			// comment); assume GitHub for now. When other forges
			// produce repo labels with a different scheme, gate this.
			doc["codeRepository"] = "https://github.com/" + sum.RepoLabel
		}
		if sum.LatestIngestedAt != "" {
			doc["datePublished"] = sum.LatestIngestedAt
		}
	}
	body, err := json.Marshal(doc)
	if err != nil {
		// Marshalling map[string]any with these shapes shouldn't fail;
		// if it does, drop the JSON-LD block rather than corrupt HTML.
		return ""
	}
	return string(body)
}

// sentinelRE matches the placeholder comment we put in app.html.
// Tolerates surrounding whitespace so a future prettifier/formatter
// can re-indent the line without breaking injection.
var sentinelRE = regexp.MustCompile(`<!--\s*HEADTAGS-SENTINEL\s*-->`)

// Inject replaces the sentinel comment in html with the rendered
// tag block. Returns the original bytes unmodified if the sentinel
// isn't present (eg an unrelated HTML file slipped through, or
// SvelteKit's build dropped the comment) so we never blank a page.
func Inject(htmlBytes []byte, tags Tags) []byte {
	rendered := tags.Render()
	if rendered == "" {
		return htmlBytes
	}
	if !sentinelRE.Match(htmlBytes) {
		return htmlBytes
	}
	// Strip the trailing newline from rendered so the indentation in
	// the resulting <head> stays consistent — Render appends \n to
	// each line, and the sentinel itself doesn't end in \n.
	rendered = strings.TrimRight(rendered, "\n")
	return sentinelRE.ReplaceAll(htmlBytes, []byte(rendered))
}

// EstimateSize returns a conservative upper bound for the rendered
// tag block in bytes. Used by callers that want to pre-size a
// bytes.Buffer to avoid reallocations during Inject.
func EstimateSize(t Tags) int {
	// Each emitted line is roughly: 4 (indent) + ~80 (tag boilerplate)
	// + the value itself. Ten possible lines, generous per-line cap.
	const perLine = 256
	n := 0
	if t.Title != "" {
		n += perLine + len(t.Title)*4 // title appears in <title>, og:title, twitter:title — multiplier covers it
	}
	if t.Description != "" {
		n += perLine + len(t.Description)*3
	}
	if t.Canonical != "" {
		n += perLine + len(t.Canonical)
	}
	if t.OGURL != "" {
		n += perLine + len(t.OGURL)
	}
	if t.SiteName != "" {
		n += perLine + len(t.SiteName)
	}
	if t.OGType != "" {
		n += perLine + len(t.OGType)
	}
	return n
}

// ensure no unused imports — bytes is used by callers via Inject; we
// re-import here so this file compiles standalone if Inject moves.
var _ = bytes.TrimSpace
