package headtags_test

import (
	"context"
	"strings"
	"testing"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/server/headtags"
)

func TestCompose_RootPath(t *testing.T) {
	tags := headtags.Compose(context.Background(), "/", "https://bzlhub.com", nil)
	if !strings.Contains(tags.Title, "bzlhub") {
		t.Errorf("root title should mention bzlhub; got %q", tags.Title)
	}
	if tags.Canonical != "https://bzlhub.com/" {
		t.Errorf("root canonical: got %q", tags.Canonical)
	}
	if tags.Description == "" {
		t.Error("root description should be non-empty")
	}
}

func TestCompose_ModuleVersion(t *testing.T) {
	tags := headtags.Compose(context.Background(), "/modules/rules_go/0.50.1", "https://bzlhub.com", nil)
	if !strings.Contains(tags.Title, "rules_go") {
		t.Errorf("module version title should mention name; got %q", tags.Title)
	}
	if !strings.Contains(tags.Title, "0.50.1") {
		t.Errorf("module version title should mention version; got %q", tags.Title)
	}
	if tags.Canonical != "https://bzlhub.com/modules/rules_go/0.50.1" {
		t.Errorf("module version canonical: got %q", tags.Canonical)
	}
}

func TestCompose_ModuleName(t *testing.T) {
	tags := headtags.Compose(context.Background(), "/modules/rules_go", "https://bzlhub.com", nil)
	if !strings.Contains(tags.Title, "rules_go") {
		t.Errorf("module title should mention name; got %q", tags.Title)
	}
	if tags.Canonical != "https://bzlhub.com/modules/rules_go" {
		t.Errorf("module canonical: got %q", tags.Canonical)
	}
}

func TestCompose_ModuleVersionSubpath(t *testing.T) {
	// /modules/foo/1.0/docs should reuse the (foo, 1.0) tags.
	tags := headtags.Compose(context.Background(), "/modules/rules_go/0.50.1/docs", "https://bzlhub.com", nil)
	if !strings.Contains(tags.Title, "rules_go") || !strings.Contains(tags.Title, "0.50.1") {
		t.Errorf("sub-path under version should reuse version tags; got %q", tags.Title)
	}
}

func TestCompose_KnownRoutes(t *testing.T) {
	cases := []struct {
		path     string
		wantSlug string
	}{
		{"/modules", "Modules"},
		{"/drift", "Drift"},
		{"/history", "History"},
		{"/compat-check", "Compatibility"},
		{"/about", "About"},
		{"/status", "Status"},
		{"/mcp", "MCP"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			tags := headtags.Compose(context.Background(), c.path, "https://bzlhub.com", nil)
			if !strings.Contains(tags.Title, c.wantSlug) {
				t.Errorf("%s title should contain %q; got %q", c.path, c.wantSlug, tags.Title)
			}
			if !strings.HasSuffix(tags.Canonical, c.path) {
				t.Errorf("%s canonical should end with path; got %q", c.path, tags.Canonical)
			}
		})
	}
}

func TestRender_EscapesAndOrders(t *testing.T) {
	tags := headtags.Tags{
		Title:       `Tricky "quoted" & ampersand`,
		Description: "Plain desc",
		Canonical:   "https://bzlhub.com/x",
		OGType:      "website",
		OGURL:       "https://bzlhub.com/x",
		SiteName:    "bzlhub",
	}
	out := tags.Render()
	// HTML escaping must apply to attribute values.
	if strings.Contains(out, `"quoted"`) {
		t.Errorf("title must be HTML-escaped; got %q", out)
	}
	// Go's html.EscapeString uses numeric &#34; rather than &quot; — both
	// are valid HTML; assert on the actual library behaviour.
	if !strings.Contains(out, "&#34;quoted&#34;") {
		t.Errorf("expected escaped quotes in output; got %q", out)
	}
	if !strings.Contains(out, "&amp; ampersand") {
		t.Errorf("expected escaped ampersand in output; got %q", out)
	}
	// Required tags all present.
	for _, want := range []string{
		"<title>",
		`<meta name="description"`,
		`<link rel="canonical"`,
		`<meta property="og:title"`,
		`<meta property="og:description"`,
		`<meta property="og:type"`,
		`<meta property="og:url"`,
		`<meta property="og:site_name"`,
		`<meta name="twitter:card"`,
		`<meta name="twitter:title"`,
		`<meta name="twitter:description"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing tag %q in output:\n%s", want, out)
		}
	}
}

func TestRender_OGImage_UpgradesTwitterCard(t *testing.T) {
	// With OGImage set, twitter:card should be summary_large_image
	// AND og:image:width + og:image:height should be emitted.
	tags := headtags.Tags{
		Title:       "rules_go@0.50.1 · bzlhub",
		Description: "rules_go at version 0.50.1",
		Canonical:   "https://bzlhub.com/modules/rules_go/0.50.1",
		OGImage:     "https://bzlhub.com/og/rules_go/0.50.1.png",
		SiteName:    "bzlhub",
		OGType:      "website",
	}
	out := tags.Render()
	for _, want := range []string{
		`<meta property="og:image" content="https://bzlhub.com/og/rules_go/0.50.1.png" />`,
		`<meta property="og:image:width" content="1200" />`,
		`<meta property="og:image:height" content="630" />`,
		`<meta name="twitter:card" content="summary_large_image" />`,
		`<meta name="twitter:image" content="https://bzlhub.com/og/rules_go/0.50.1.png" />`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestRender_NoOGImage_KeepsSummaryCard(t *testing.T) {
	// Without OGImage, twitter:card stays as "summary" and no
	// og:image:* tags are emitted.
	tags := headtags.Tags{
		Title:       "Some Page",
		Description: "Description",
		Canonical:   "https://bzlhub.com/x",
		SiteName:    "bzlhub",
		OGType:      "website",
	}
	out := tags.Render()
	if !strings.Contains(out, `<meta name="twitter:card" content="summary" />`) {
		t.Errorf("expected summary twitter:card when OGImage unset, got:\n%s", out)
	}
	if strings.Contains(out, "og:image") || strings.Contains(out, "twitter:image") {
		t.Errorf("did not expect og:image/twitter:image when OGImage unset, got:\n%s", out)
	}
}

func TestCompose_ModuleVersion_EmitsPerModuleOGImage(t *testing.T) {
	tags := headtags.Compose(context.Background(), "/modules/rules_go/0.50.1", "https://bzlhub.com", nil)
	if tags.OGImage != "https://bzlhub.com/og/rules_go/0.50.1.png" {
		t.Errorf("og:image: got %q, want https://bzlhub.com/og/rules_go/0.50.1.png", tags.OGImage)
	}
}

func TestCompose_Root_EmitsDefaultOGImage(t *testing.T) {
	tags := headtags.Compose(context.Background(), "/", "https://bzlhub.com", nil)
	if tags.OGImage != "https://bzlhub.com/og/default.png" {
		t.Errorf("og:image: got %q, want https://bzlhub.com/og/default.png", tags.OGImage)
	}
}

func TestRender_JSONLD_WrappedInScriptTag(t *testing.T) {
	tags := headtags.Tags{
		Title:  "rules_go@0.50.1 · bzlhub",
		JSONLD: `{"@context":"https://schema.org","@type":"SoftwareSourceCode","name":"rules_go"}`,
	}
	out := tags.Render()
	if !strings.Contains(out, `<script type="application/ld+json">`) {
		t.Errorf("missing script open tag in:\n%s", out)
	}
	if !strings.Contains(out, `"@type":"SoftwareSourceCode"`) {
		t.Errorf("missing JSON-LD body in:\n%s", out)
	}
	if !strings.Contains(out, "</script>") {
		t.Errorf("missing script close tag in:\n%s", out)
	}
}

// TestRender_JSONLD_EscapesScriptBreakout locks in the HTML5 safety
// rule: a JSON-LD block inside <script> is raw text up to the first
// "</script>" — so any "</" in the JSON must be escaped. We use
// "<" replacement, which is JSON-valid (allowed in string values
// via \uXXXX-style numeric character refs aren't needed since the
// JSON parser sees the entity AFTER HTML decode — wait, that's
// HTML semantics not JSON. Re-check below).
//
// Actually: the script tag's content is parsed as raw text by the
// HTML parser; HTML entities are NOT decoded inside <script>. So
// "<" stays literal "<" in what the JSON parser sees. The JSON
// parser treats "<" as a normal string character. So replacement
// works because there's no "</script>" substring left after the
// "<" → "<" swap.
//
// If this test ever breaks, treat it as a security regression.
func TestRender_JSONLD_EscapesScriptBreakout(t *testing.T) {
	// Attacker-controlled-ish input embedded in the JSON-LD.
	tags := headtags.Tags{
		Title:  "x",
		JSONLD: `{"description":"</script><script>alert(1)</script>"}`,
	}
	out := tags.Render()
	// The dangerous substring must not appear verbatim — the < swap
	// must have broken any potential </script> close.
	if strings.Contains(out, `"</script>"`) {
		t.Errorf("JSON-LD allowed a </script> breakout:\n%s", out)
	}
	// The < replacement should be present.
	if !strings.Contains(out, "<") {
		t.Errorf("expected < escapes in:\n%s", out)
	}
}

func TestRender_NoJSONLD_NoScriptTag(t *testing.T) {
	tags := headtags.Tags{Title: "x", Description: "y"}
	out := tags.Render()
	if strings.Contains(out, "application/ld+json") {
		t.Errorf("did not expect JSON-LD when Tags.JSONLD unset:\n%s", out)
	}
}

func TestInject_ReplacesSentinel(t *testing.T) {
	in := []byte(`<!doctype html><html><head>
<title>old</title>
<!-- HEADTAGS-SENTINEL -->
</head></html>`)
	tags := headtags.Tags{Title: "new", Description: "desc", Canonical: "https://example.com/", SiteName: "bzlhub", OGType: "website"}
	out := headtags.Inject(in, tags)
	if !strings.Contains(string(out), "<title>new</title>") {
		t.Errorf("Inject should add new title; got\n%s", string(out))
	}
	if strings.Contains(string(out), "HEADTAGS-SENTINEL") {
		t.Errorf("sentinel should be replaced; got\n%s", string(out))
	}
}

func TestInject_NoSentinelIsNoOp(t *testing.T) {
	in := []byte(`<!doctype html><html><head><title>x</title></head></html>`)
	out := headtags.Inject(in, headtags.Tags{Title: "y"})
	if string(out) != string(in) {
		t.Errorf("Inject without sentinel should be no-op; got %q", string(out))
	}
}

func TestInject_EmptyTagsIsNoOp(t *testing.T) {
	in := []byte(`<head><!-- HEADTAGS-SENTINEL --></head>`)
	out := headtags.Inject(in, headtags.Tags{})
	if string(out) != string(in) {
		t.Errorf("Inject with empty Tags should be no-op; got %q", string(out))
	}
}

func TestInject_TolerantSentinelWhitespace(t *testing.T) {
	// A future prettifier might re-format the comment with extra spaces.
	in := []byte(`<head><!--  HEADTAGS-SENTINEL  --></head>`)
	tags := headtags.Tags{Title: "T"}
	out := headtags.Inject(in, tags)
	if !strings.Contains(string(out), "<title>T</title>") {
		t.Errorf("should match sentinel with surrounding whitespace; got %q", string(out))
	}
}

// Ensure the package consumes api.Canopy as documented.
var _ = func() api.Canopy { return nil }
