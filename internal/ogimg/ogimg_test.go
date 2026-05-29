package ogimg_test

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/albertocavalcante/canopy/internal/ogimg"
)

func TestRender_Dimensions(t *testing.T) {
	var buf bytes.Buffer
	spec := ogimg.Spec{
		ModuleName:    "rules_go",
		ModuleVersion: "0.50.1",
		Hermeticity:   "prebuilt-binaries-pinned",
		RuleCount:     47,
		DepCount:      12,
		Versions:      23,
		Host:          "bzlhub.com",
	}
	if err := ogimg.Render(&buf, spec); err != nil {
		t.Fatalf("Render: %v", err)
	}
	img, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 1200 || b.Dy() != 630 {
		t.Errorf("dimensions: got %dx%d, want 1200x630", b.Dx(), b.Dy())
	}
}

func TestGeneric_AlwaysSucceeds(t *testing.T) {
	var buf bytes.Buffer
	if err := ogimg.Generic(&buf); err != nil {
		t.Fatalf("Generic: %v", err)
	}
	if buf.Len() < 1000 {
		t.Errorf("generic PNG suspiciously small: %d bytes", buf.Len())
	}
}

func TestRender_HandlesLongModuleNames(t *testing.T) {
	var buf bytes.Buffer
	spec := ogimg.Spec{
		ModuleName: "some_extremely_long_module_name_that_would_overflow_the_image_width_easily",
		Host:       "bzlhub.com",
	}
	if err := ogimg.Render(&buf, spec); err != nil {
		t.Fatalf("Render with long name: %v", err)
	}
}

func TestRender_HandlesUnknownHermeticity(t *testing.T) {
	var buf bytes.Buffer
	spec := ogimg.Spec{
		ModuleName:  "foo",
		Hermeticity: "this-class-does-not-exist",
		Host:        "bzlhub.com",
	}
	if err := ogimg.Render(&buf, spec); err != nil {
		t.Fatalf("Render with unknown hermeticity: %v", err)
	}
}

func TestRender_HandlesEmptyCounts(t *testing.T) {
	var buf bytes.Buffer
	spec := ogimg.Spec{
		ModuleName:    "minimal",
		ModuleVersion: "1.0.0",
		Host:          "bzlhub.com",
	}
	if err := ogimg.Render(&buf, spec); err != nil {
		t.Fatalf("Render with zero counts: %v", err)
	}
}

func TestRenderBytes_RoundTrip(t *testing.T) {
	spec := ogimg.Spec{ModuleName: "rules_go", ModuleVersion: "0.50.1", Host: "bzlhub.com"}
	b1, err := ogimg.RenderBytes(spec)
	if err != nil {
		t.Fatalf("RenderBytes: %v", err)
	}
	b2, err := ogimg.RenderBytes(spec)
	if err != nil {
		t.Fatalf("RenderBytes #2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("Render is non-deterministic: differs on second call")
	}
}

// BenchmarkRender measures the cache-miss latency operators pay on
// the first request for an OG image. Target: < 100ms per render on
// a modest VPS (Hetzner CAX21 ARM). Regressions in font handling or
// layout maths should surface here before they hit production.
func BenchmarkRender(b *testing.B) {
	spec := ogimg.Spec{
		ModuleName:    "rules_go",
		ModuleVersion: "0.50.1",
		Hermeticity:   "prebuilt-binaries-pinned",
		RuleCount:     47,
		DepCount:      12,
		Versions:      23,
		Host:          "bzlhub.com",
	}
	b.ReportAllocs()
	for b.Loop() {
		var buf bytes.Buffer
		if err := ogimg.Render(&buf, spec); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRender_AllHermeticityClassesWork(t *testing.T) {
	classes := []string{
		"pure-starlark",
		"prebuilt-binaries-pinned",
		"build-from-source",
		"network-fetch-pinned",
		"network-fetch-unpinned",
		"requires-system-tools",
		"repository-rule-arbitrary-code",
	}
	for _, c := range classes {
		t.Run(c, func(t *testing.T) {
			var buf bytes.Buffer
			spec := ogimg.Spec{ModuleName: "foo", ModuleVersion: "1.0", Hermeticity: c, Host: "bzlhub.com"}
			if err := ogimg.Render(&buf, spec); err != nil {
				t.Fatalf("Render(%s): %v", c, err)
			}
		})
	}
}
