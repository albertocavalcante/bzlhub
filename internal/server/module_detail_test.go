package server

import (
	"context"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/canopy/internal/docview"
)

func TestAugmentModuleResponseReturnsPlainReportWithoutAugmentations(t *testing.T) {
	rep := &report.ModuleReport{Name: "empty", Version: "1.0.0"}

	got := (&handler{}).augmentModuleResponse(context.Background(), rep, "empty")
	if got != rep {
		t.Fatalf("augmentModuleResponse without docs/helper/meta = %#v, want original report", got)
	}
}

func TestAugmentModuleResponseAddsParsedDocs(t *testing.T) {
	rep := &report.ModuleReport{
		Name:    "rules_go",
		Version: "0.50.0",
		Rules: []report.RuleSpec{{
			Name: "go_binary",
			Doc:  "Use @bazel_skylib//rules:common_settings.bzl and //go:def.bzl.",
		}},
		Providers: []report.ProviderSpec{{
			Name: "empty_provider",
		}},
	}

	got := (&handler{}).augmentModuleResponse(context.Background(), rep, "rules_go")
	wrapped, ok := got.(moduleResponseWithDocs)
	if !ok {
		t.Fatalf("augmentModuleResponse with docs type = %T, want moduleResponseWithDocs", got)
	}
	doc := wrapped.ParsedDocs["go_binary"]
	if doc == nil {
		t.Fatalf("missing parsed doc for go_binary: %#v", wrapped.ParsedDocs)
	}
	if _, ok := wrapped.ParsedDocs["empty_provider"]; ok {
		t.Fatalf("empty provider doc should be omitted: %#v", wrapped.ParsedDocs["empty_provider"])
	}
	if !hasChipHref(doc.Chips, "/modules/bazel_skylib") {
		t.Fatalf("parsed doc missing external module chip: %#v", doc.Chips)
	}
}

func hasChipHref(chips []docview.Chip, href string) bool {
	for _, chip := range chips {
		if chip.Href == href {
			return true
		}
	}
	return false
}

func TestCanopyLinkResolver(t *testing.T) {
	resolver := canopyLinkResolver{}
	if got, want := resolver.ModuleHref("bazel_skylib"), "/modules/bazel_skylib"; got != want {
		t.Fatalf("ModuleHref = %q, want %q", got, want)
	}
	if got, want := resolver.CodeNavFileHref("rules_go", "0.50.0", "go/def.bzl"), "/modules/rules_go/0.50.0/code-nav/file/go/def.bzl"; got != want {
		t.Fatalf("CodeNavFileHref = %q, want %q", got, want)
	}
}
