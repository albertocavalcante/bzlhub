package docview

import (
	"testing"

	bazeldoc "github.com/albertocavalcante/bazel-doc-go"
)

// fakeResolver is a minimal LinkResolver that produces predictable
// URLs so the tests can compare strings without depending on
// canopy's link helpers.
type fakeResolver struct{}

func (fakeResolver) ModuleHref(name string) string { return "/modules/" + name }
func (fakeResolver) CodeNavFileHref(module, version, file string) string {
	return "/modules/" + module + "/" + version + "/code-nav/file/" + file
}

func TestBuild_AtRepoLabelGetsModuleHref(t *testing.T) {
	e := bazeldoc.Parse("See @bazel_skylib//rules:common_settings.bzl for details.")
	d := Build(e, Owner{Module: "rules_cc", Version: "0.1.0"}, fakeResolver{})
	if d == nil {
		t.Fatal("expected Doc, got nil")
	}
	if len(d.Refs) != 1 {
		t.Fatalf("expected one ref, got %d", len(d.Refs))
	}
	if got, want := d.Refs[0].Href, "/modules/bazel_skylib"; got != want {
		t.Fatalf("ref href: got %q want %q", got, want)
	}
	if len(d.Chips) != 1 || d.Chips[0].Label != "@bazel_skylib" {
		t.Fatalf("chip: %#v", d.Chips)
	}
}

func TestBuild_SameRepoFileLabelLinksToCodeNav(t *testing.T) {
	e := bazeldoc.Parse("Defined in //lib:write_source_files.bzl, used everywhere.")
	d := Build(e, Owner{Module: "aspect_bazel_lib", Version: "2.7.7"}, fakeResolver{})
	if len(d.Refs) != 1 {
		t.Fatalf("expected one ref, got %d", len(d.Refs))
	}
	if got, want := d.Refs[0].Href, "/modules/aspect_bazel_lib/2.7.7/code-nav/file/lib/write_source_files.bzl"; got != want {
		t.Fatalf("ref href: got %q want %q", got, want)
	}
	if len(d.Chips) != 1 || d.Chips[0].Label != "lib/write_source_files.bzl" {
		t.Fatalf("chip: %#v", d.Chips)
	}
}

func TestBuild_SameRepoNonFileTargetSkipped(t *testing.T) {
	e := bazeldoc.Parse("Apply to //my_pkg:some_rule and you're done.")
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	// One ref extracted, but no Href, no chip.
	if len(d.Refs) != 1 {
		t.Fatalf("expected one ref, got %d", len(d.Refs))
	}
	if d.Refs[0].Href != "" {
		t.Fatalf("expected blank href for non-file target, got %q", d.Refs[0].Href)
	}
	if len(d.Chips) != 0 {
		t.Fatalf("expected no chips, got %d", len(d.Chips))
	}
}

func TestBuild_NoOwnerLeavesSameRepoUnresolved(t *testing.T) {
	e := bazeldoc.Parse("Defined in //lib:thing.bzl.")
	d := Build(e, Owner{}, fakeResolver{})
	if d.Refs[0].Href != "" {
		t.Fatalf("same-repo without owner should not resolve, got %q", d.Refs[0].Href)
	}
}

func TestBuild_DedupesChipsAcrossMultipleMentions(t *testing.T) {
	src := "See @bazel_skylib//rules:a.bzl, also @bazel_skylib//rules:b.bzl."
	e := bazeldoc.Parse(src)
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	if len(d.Refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(d.Refs))
	}
	if len(d.Chips) != 1 {
		t.Fatalf("expected one deduplicated chip, got %d: %#v", len(d.Chips), d.Chips)
	}
}

func TestBuild_InsideCodeSpan_HrefSetButSpliceFalse(t *testing.T) {
	// Refs inside inline code still navigate (chip is useful),
	// but the inline splice would corrupt the Markdown — so
	// Splice=false even though Href is populated.
	e := bazeldoc.Parse("Use `@bazel_skylib//rules:t.bzl` for that.")
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	if d.Refs[0].Href == "" {
		t.Fatalf("ref inside inline code should still have Href")
	}
	if d.Refs[0].Splice {
		t.Fatalf("ref inside inline code must not splice")
	}
	if len(d.Chips) != 1 {
		t.Fatalf("ref inside inline code should still chip, got %d", len(d.Chips))
	}
}

func TestBuild_InsideLinkText_HrefSetButSpliceFalse(t *testing.T) {
	e := bazeldoc.Parse("[@bazel_skylib//rules:t.bzl](https://example.com) here.")
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	if d.Refs[0].Href == "" {
		t.Fatalf("ref inside existing [text] should still have Href")
	}
	if d.Refs[0].Splice {
		t.Fatalf("ref inside existing [text] must not splice")
	}
}

func TestBuild_SafeRef_BothHrefAndSplice(t *testing.T) {
	e := bazeldoc.Parse("Use @bazel_skylib//rules:t.bzl freely.")
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	if d.Refs[0].Href == "" || !d.Refs[0].Splice {
		t.Fatalf("clean prose ref should have Href + Splice, got %+v", d.Refs[0])
	}
}

func TestBuild_SelfReferenceFileLabelDeepLinks(t *testing.T) {
	// @aspect_bazel_lib//lib:write_source_files.bzl inside the
	// aspect_bazel_lib module's own docs should deep-link past
	// the module landing into code-nav.
	src := "See @aspect_bazel_lib//lib:write_source_files.bzl for details."
	e := bazeldoc.Parse(src)
	d := Build(e, Owner{Module: "aspect_bazel_lib", Version: "2.7.7"}, fakeResolver{})
	if len(d.Refs) != 1 {
		t.Fatalf("expected one ref, got %d", len(d.Refs))
	}
	if got, want := d.Refs[0].Href, "/modules/aspect_bazel_lib/2.7.7/code-nav/file/lib/write_source_files.bzl"; got != want {
		t.Fatalf("self-ref href: got %q want %q", got, want)
	}
}

func TestBuild_SelfReferenceNonFileTargetStaysOnModuleLanding(t *testing.T) {
	// Self-reference to a build target (not a file): still useful
	// to land on the module page, even if we can't deep-link.
	src := "See @aspect_bazel_lib//my_pkg:some_rule"
	e := bazeldoc.Parse(src)
	d := Build(e, Owner{Module: "aspect_bazel_lib", Version: "2.7.7"}, fakeResolver{})
	if got, want := d.Refs[0].Href, "/modules/aspect_bazel_lib"; got != want {
		t.Fatalf("non-file self-ref: got %q want %q", got, want)
	}
}

func TestBuild_InsideFencedCodeBlock_SpliceFalse(t *testing.T) {
	// Description with a fenced code block that contains a label.
	// Href still useful for chip navigation; Splice would corrupt
	// the rendered code fence.
	src := "Usage:\n\n```starlark\nload(\"@bazel_skylib//rules:t.bzl\", \"x\")\n```\n"
	e := bazeldoc.Parse(src)
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	if d.Refs[0].Splice {
		t.Fatalf("ref inside fenced code block must not splice")
	}
	if d.Refs[0].Href == "" {
		t.Fatalf("ref inside fenced code block should still have a chip-eligible Href")
	}
}

func TestBuild_MiddleOfLinkText_SpliceFalse(t *testing.T) {
	src := "See [the helper @bazel_skylib//rules:t.bzl docs](https://example.com)."
	e := bazeldoc.Parse(src)
	d := Build(e, Owner{Module: "x", Version: "1"}, fakeResolver{})
	if d.Refs[0].Splice {
		t.Fatalf("ref in middle of existing [...] must not splice")
	}
}

func TestBuild_NilEnrichedReturnsNil(t *testing.T) {
	if d := Build(nil, Owner{}, fakeResolver{}); d != nil {
		t.Fatalf("Build(nil) should return nil, got %#v", d)
	}
}
