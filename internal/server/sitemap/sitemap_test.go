package sitemap_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/server/sitemap"
)

func TestStream_NilCanopy_StaticOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := sitemap.Stream(context.Background(), nil, "https://bzlhub.com", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`) {
		t.Errorf("missing xmlns declaration: %s", out)
	}
	// Static routes all present.
	for _, want := range []string{
		"https://bzlhub.com/",
		"https://bzlhub.com/modules",
		"https://bzlhub.com/drift",
		"https://bzlhub.com/history",
		"https://bzlhub.com/compat-check",
	} {
		if !strings.Contains(out, "<loc>"+want+"</loc>") {
			t.Errorf("missing static route %q in:\n%s", want, out)
		}
	}
	// Validate XML well-formedness.
	if err := xml.NewDecoder(&buf).Decode(new(struct {
		XMLName xml.Name `xml:"urlset"`
		URLs    []struct {
			Loc        string `xml:"loc"`
			LastMod    string `xml:"lastmod"`
			ChangeFreq string `xml:"changefreq"`
			Priority   string `xml:"priority"`
		} `xml:"url"`
	})); err != nil {
		t.Errorf("XML doesn't decode: %v", err)
	}
}

// fakeCanopy is the minimum stub satisfying api.Canopy that
// Stream actually calls (ListModules + ListVersions). All other
// methods return zero-value or panic; tests that need them should
// extend.
type fakeCanopy struct {
	api.Canopy // embeds nil interface; calling other methods panics
	mods       []api.ModuleSummary
	versions   map[string][]string
}

func (f *fakeCanopy) ListModules(_ context.Context) ([]api.ModuleSummary, error) {
	return f.mods, nil
}
func (f *fakeCanopy) ListVersions(_ context.Context, name string) ([]string, error) {
	return f.versions[name], nil
}

func TestStream_WithCanopy_EmitsModuleAndVersion(t *testing.T) {
	c := &fakeCanopy{
		mods: []api.ModuleSummary{
			{Name: "rules_go", LatestVersion: "0.50.1", LatestIngestedAt: "2026-05-17T13:19:08Z"},
		},
		versions: map[string][]string{
			"rules_go": {"0.49.0", "0.50.1"},
		},
	}
	var buf bytes.Buffer
	if err := sitemap.Stream(context.Background(), c, "https://bzlhub.com", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"<loc>https://bzlhub.com/modules/rules_go</loc>",
		"<loc>https://bzlhub.com/modules/rules_go/0.49.0</loc>",
		"<loc>https://bzlhub.com/modules/rules_go/0.50.1</loc>",
		"<lastmod>2026-05-17</lastmod>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

// Sentinel/stub versions ("0.0.0", "HEAD", etc.) are persisted for
// cross-reference bookkeeping when a module is named via bazel_deps
// but never ingested for real. They render empty pages and shouldn't
// be advertised to crawlers — the sitemap must skip them.
func TestStream_SkipsStubVersions(t *testing.T) {
	c := &fakeCanopy{
		mods: []api.ModuleSummary{
			{Name: "rules_oci", LatestVersion: "2.0.1"},
			{Name: "gazelle", LatestVersion: "0.40.0"},
		},
		versions: map[string][]string{
			"rules_oci": {"2.0.1", "0.0.0"},                     // 0.0.0 = synthetic floor
			"gazelle":   {"0.40.0", "0.36.0", "HEAD"},           // HEAD = git-shaped placeholder
		},
	}
	var buf bytes.Buffer
	if err := sitemap.Stream(context.Background(), c, "https://bzlhub.com", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	out := buf.String()

	// Real versions present.
	for _, want := range []string{
		"<loc>https://bzlhub.com/modules/rules_oci/2.0.1</loc>",
		"<loc>https://bzlhub.com/modules/gazelle/0.40.0</loc>",
		"<loc>https://bzlhub.com/modules/gazelle/0.36.0</loc>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("real-version %q missing:\n%s", want, out)
		}
	}

	// Stubs filtered.
	for _, banned := range []string{
		"/modules/rules_oci/0.0.0",
		"/modules/gazelle/HEAD",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("stub version %q should not appear in sitemap:\n%s", banned, out)
		}
	}
}

func TestStream_EmptyLastIngestedFallsBackToToday(t *testing.T) {
	c := &fakeCanopy{
		mods:     []api.ModuleSummary{{Name: "foo", LatestVersion: "1.0"}},
		versions: map[string][]string{"foo": {"1.0"}},
	}
	var buf bytes.Buffer
	if err := sitemap.Stream(context.Background(), c, "https://bzlhub.com", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Should still emit valid XML, with a date that parses.
	if !strings.Contains(buf.String(), "<lastmod>20") {
		t.Errorf("expected fallback lastmod with year 20xx in:\n%s", buf.String())
	}
}

func TestStream_OriginIsRespected(t *testing.T) {
	var buf bytes.Buffer
	if err := sitemap.Stream(context.Background(), nil, "https://canopy.example.com", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "bzlhub.com") {
		t.Error("origin should not leak bzlhub.com when given a different origin")
	}
	if !strings.Contains(out, "https://canopy.example.com/") {
		t.Error("custom origin missing from output")
	}
}

func TestStream_PrioritiesPresent(t *testing.T) {
	var buf bytes.Buffer
	if err := sitemap.Stream(context.Background(), nil, "https://bzlhub.com", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	out := buf.String()
	// Root gets priority 1.0; static pages get 0.8.
	if !strings.Contains(out, "<priority>1.0</priority>") {
		t.Error("expected priority 1.0 for root page")
	}
	if !strings.Contains(out, "<priority>0.8</priority>") {
		t.Error("expected priority 0.8 for static pages")
	}
}
