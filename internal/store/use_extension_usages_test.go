package store_test

import (
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/store"
)

// Use-extension usages are the cross-module index that lets canopy's
// airgap analyzer drive a producer ruleset's module_extension impls
// with REAL tag values aggregated across the consumer corpus, rather
// than synthetic attr defaults.
//
// Pin the store contract:
//   - WriteUseExtensionUsages is idempotent per (consumer, version)
//   - GetUseExtensionUsagesForExtension returns all stored usages
//     for the given (extension_file, extension_name) across the corpus
//   - Tag attrs round-trip as JSON-encoded text
func TestUseExtensionUsages_RoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Two consumer modules using the same rules_go extension differently.
	mustSeed := func(name, version string) {
		t.Helper()
		if err := s.WriteReport(ctx, &report.ModuleReport{Name: name, Version: version}); err != nil {
			t.Fatal(err)
		}
	}
	mustSeed("myapp", "1.0.0")
	mustSeed("otherapp", "2.0.0")

	myapp := []store.UseExtensionUsage{
		{
			ExtensionFile: "@rules_go//go:extensions.bzl",
			ExtensionName: "go_sdk",
			TagIndex:      0, TagName: "download",
			TagAttrsJSON: `{"version":"1.22.5"}`,
		},
		{
			ExtensionFile: "@rules_go//go:extensions.bzl",
			ExtensionName: "go_sdk",
			TagIndex:      1, TagName: "download",
			TagAttrsJSON:  `{"version":"1.21.3","goos":"linux","goarch":"amd64"}`,
			DevDependency: false,
		},
	}
	otherapp := []store.UseExtensionUsage{
		{
			ExtensionFile: "@rules_go//go:extensions.bzl",
			ExtensionName: "go_sdk",
			TagIndex:      0, TagName: "download",
			TagAttrsJSON: `{"version":"1.20.0"}`,
		},
		{
			ExtensionFile: "@rules_python//python/extensions:python.bzl",
			ExtensionName: "python",
			TagIndex:      0, TagName: "toolchain",
			TagAttrsJSON: `{"python_version":"3.11.6","is_default":true}`,
		},
	}

	if err := s.WriteUseExtensionUsages(ctx, "myapp", "1.0.0", myapp); err != nil {
		t.Fatalf("write myapp: %v", err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "otherapp", "2.0.0", otherapp); err != nil {
		t.Fatalf("write otherapp: %v", err)
	}

	// Query: every consumer's usage of rules_go's go_sdk extension.
	goSDKUsages, err := s.GetUseExtensionUsagesForExtension(ctx,
		"@rules_go//go:extensions.bzl", "go_sdk")
	if err != nil {
		t.Fatalf("query go_sdk: %v", err)
	}
	if len(goSDKUsages) != 3 {
		t.Errorf("go_sdk usages = %d (%+v), want 3 (myapp×2 + otherapp×1)",
			len(goSDKUsages), goSDKUsages)
	}

	// Query: rules_python's python extension.
	pyUsages, err := s.GetUseExtensionUsagesForExtension(ctx,
		"@rules_python//python/extensions:python.bzl", "python")
	if err != nil {
		t.Fatalf("query python: %v", err)
	}
	if len(pyUsages) != 1 {
		t.Errorf("python usages = %d, want 1", len(pyUsages))
	}
	if pyUsages[0].ConsumerModule != "otherapp" {
		t.Errorf("python consumer = %q, want otherapp", pyUsages[0].ConsumerModule)
	}
}

// Re-ingest replaces prior usages for the same (consumer, version).
func TestUseExtensionUsages_ReIngestReplaces(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}

	if err := s.WriteUseExtensionUsages(ctx, "m", "1", []store.UseExtensionUsage{
		{ExtensionFile: "@x//:y.bzl", ExtensionName: "old", TagIndex: 0, TagName: "t", TagAttrsJSON: `{}`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "m", "1", []store.UseExtensionUsage{
		{ExtensionFile: "@x//:y.bzl", ExtensionName: "new", TagIndex: 0, TagName: "t", TagAttrsJSON: `{}`},
	}); err != nil {
		t.Fatal(err)
	}

	oldUsages, _ := s.GetUseExtensionUsagesForExtension(ctx, "@x//:y.bzl", "old")
	newUsages, _ := s.GetUseExtensionUsagesForExtension(ctx, "@x//:y.bzl", "new")
	if len(oldUsages) != 0 {
		t.Errorf("old extension should be wiped on re-ingest, got %d", len(oldUsages))
	}
	if len(newUsages) != 1 {
		t.Errorf("new extension should be present, got %d", len(newUsages))
	}
}

// Empty usages list is valid — just clears prior rows.
func TestUseExtensionUsages_EmptySliceClears(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "m", "1", []store.UseExtensionUsage{
		{ExtensionFile: "@x//:y.bzl", ExtensionName: "z", TagIndex: 0, TagName: "t", TagAttrsJSON: `{}`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "m", "1", nil); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	usages, _ := s.GetUseExtensionUsagesForExtension(ctx, "@x//:y.bzl", "z")
	if len(usages) != 0 {
		t.Errorf("empty write should clear prior usages, got %d", len(usages))
	}
}

