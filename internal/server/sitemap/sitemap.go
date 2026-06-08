// Package sitemap streams an XML sitemap (per sitemaps.org/schemas/
// sitemap/0.9) built from canopy's indexed module/version corpus.
// Served at /sitemap.xml so search engines can auto-discover every
// module page without crawling the link graph from /modules.
//
// Design notes:
//
//   - Streams to io.Writer; no full-doc buffering. The current corpus
//     is small (~27 modules, ~270 URLs) so this is overkill today, but
//     a sitemap.xml that fans out to thousands of URLs (e.g. after a
//     full BCR ingest per plan-20) shouldn't pin the same number of
//     XML strings in memory.
//   - Static pages get fixed lastmod (build-time, ish — "now" at
//     stream time is honest because they ARE updated on every deploy).
//     Module pages get lastmod from their LatestIngestedAt.
//   - Priorities follow the table in plan-33 §1: /=1.0, static=0.8,
//     module=0.7, module-version=0.6. Google deprecated priority
//     weighting but Bing/Kagi/DuckDuckGo still honour it; near-zero
//     cost to emit.
//   - Sub-routes (/modules/<m>/<v>/docs, /docs etc.) are deliberately
//     NOT included. They reuse the canonical version URL via the
//     <link rel="canonical"> from headtags, so emitting them would
//     split rank signal across duplicate-content URLs.
//   - One-shot — caller decides whether to cache the bytes. For v0
//     the route handler streams fresh on every request (~ms even at
//     100x current corpus); add a TTL cache when traffic justifies.
package sitemap

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// isStubVersion reports whether v is a placeholder/sentinel version
// canopy persists for cross-reference bookkeeping (a module pulled in
// via bazel_deps but never ingested for real). Common shapes:
//   - "0.0.0" or "0" — synthetic floor before ingest
//   - "HEAD" — git-shaped placeholder from an early ingest tool
//
// These are excluded from the public sitemap because clicking the
// corresponding /modules/<m>/<v> URL lands on an empty page — a real
// page rendered against a stub row, with no rules / providers /
// hermeticity to show. Indexing them costs Google a 404-ish signal
// and gives users a broken-feeling landing.
//
// Not exhaustive — we don't try to detect every malformed version
// the BCR ecosystem can produce. The three patterns above cover the
// stub rows canopy creates internally, which is the source we control.
func isStubVersion(v string) bool {
	return v == "" || v == "0" || v == "0.0.0" || v == "HEAD"
}

// Static routes (not module pages) that should appear in the sitemap.
// Order matters only for human reading of the resulting XML; ranking
// engines treat the file as a set.
var staticPages = []struct {
	path     string
	priority string
	freq     string
}{
	{"/", "1.0", "weekly"},
	{"/modules", "0.8", "monthly"},
	{"/drift", "0.8", "monthly"},
	{"/history", "0.8", "monthly"},
	{"/compat-check", "0.8", "monthly"},
}

// Stream writes the sitemap XML to w. The origin should be the
// public scheme+host (e.g. "https://bzlhub.com") — every URL in the
// sitemap is rooted there. If c is nil, only the static pages are
// emitted (still a valid sitemap, just less interesting).
//
// Errors from the canopy index (ListModules, ListVersions) are
// non-fatal: we log nothing, just emit what we have and continue.
// A partial sitemap is more useful to a crawler than a 500.
func Stream(ctx context.Context, c api.Canopy, origin string, w io.Writer) error {
	enc := xml.NewEncoder(w)

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("sitemap: write header: %w", err)
	}
	if err := enc.EncodeToken(xml.StartElement{
		Name: xml.Name{Local: "urlset"},
		Attr: []xml.Attr{{
			Name:  xml.Name{Local: "xmlns"},
			Value: "http://www.sitemaps.org/schemas/sitemap/0.9",
		}},
	}); err != nil {
		return fmt.Errorf("sitemap: encode root: %w", err)
	}

	// Static pages always present; their lastmod is "now" because each
	// canopy deploy can change what they render. Honest enough.
	now := time.Now().UTC().Format("2006-01-02")
	for _, p := range staticPages {
		if err := writeURL(enc, urlEntry{
			Loc:        origin + p.path,
			LastMod:    now,
			ChangeFreq: p.freq,
			Priority:   p.priority,
		}); err != nil {
			return err
		}
	}

	// Module + version pages — best-effort from canopy. Each lookup
	// failure just skips that subtree; the sitemap is still valid.
	if c != nil {
		mods, err := c.ListModules(ctx)
		if err == nil {
			for _, m := range mods {
				lastMod := m.LatestIngestedAt
				if lastMod == "" {
					lastMod = now
				} else if t, perr := time.Parse(time.RFC3339, lastMod); perr == nil {
					lastMod = t.Format("2006-01-02")
				}
				if err := writeURL(enc, urlEntry{
					Loc:        origin + "/modules/" + m.Name,
					LastMod:    lastMod,
					ChangeFreq: "weekly",
					Priority:   "0.7",
				}); err != nil {
					return err
				}
				versions, verr := c.ListVersions(ctx, m.Name)
				if verr != nil {
					continue
				}
				for _, v := range versions {
					// Stub/sentinel rows (e.g. "0.0.0", "HEAD") aren't
					// real pages — they render empty against a placeholder
					// DB row. Crawlers should not index them. See
					// isStubVersion for the patterns covered.
					if isStubVersion(v) {
						continue
					}
					if err := writeURL(enc, urlEntry{
						Loc:        origin + "/modules/" + m.Name + "/" + v,
						LastMod:    lastMod, // per-version date isn't on Summary; use module's latest
						ChangeFreq: "monthly",
						Priority:   "0.6",
					}); err != nil {
						return err
					}
				}
			}
		}
	}

	if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "urlset"}}); err != nil {
		return fmt.Errorf("sitemap: encode close: %w", err)
	}
	if err := enc.Flush(); err != nil {
		return fmt.Errorf("sitemap: flush: %w", err)
	}
	// Trailing newline so the file ends well under `cat`.
	_, _ = io.WriteString(w, "\n")
	return nil
}

// urlEntry is the inner shape of one <url>…</url> block.
type urlEntry struct {
	XMLName    xml.Name `xml:"url"`
	Loc        string   `xml:"loc"`
	LastMod    string   `xml:"lastmod,omitempty"`
	ChangeFreq string   `xml:"changefreq,omitempty"`
	Priority   string   `xml:"priority,omitempty"`
}

func writeURL(enc *xml.Encoder, e urlEntry) error {
	if err := enc.Encode(e); err != nil {
		return fmt.Errorf("sitemap: encode url %q: %w", e.Loc, err)
	}
	return nil
}
