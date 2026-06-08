package store_test

import (
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Producer rulesets need their extension-bearing .bzl source available
// at query time so canopy can re-drive the extension impl with real
// (corpus-derived) ModuleSpec tag values. Storing just the impl-bearing
// files (NOT the whole tarball) keeps the SQLite footprint bounded —
// rules_go-class producers ship ~3-5 such files, each typically <10KB.
//
// Schema + round-trip contract:
//   - WriteModuleExtensionSources is idempotent per (module, version).
//   - GetModuleExtensionSources returns sources keyed by relative path.
//   - Empty input clears prior rows (typical when a module loses an
//     extension in a re-ingest).
func TestModuleExtensionSources_RoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "rules_go", Version: "0.50.1"}); err != nil {
		t.Fatal(err)
	}

	srcs := []store.ModuleExtensionSource{
		{File: "go/extensions.bzl", Content: []byte("# go/extensions.bzl\ndef _impl(ctx): pass\ngo_sdk = module_extension(implementation = _impl)\n")},
		{File: "private/extensions.bzl", Content: []byte("# private impl\ndef _impl(ctx): pass\nhidden_ext = module_extension(implementation = _impl)\n")},
	}
	if err := s.WriteModuleExtensionSources(ctx, "rules_go", "0.50.1", srcs); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := s.GetModuleExtensionSources(ctx, "rules_go", "0.50.1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("sources = %d, want 2", len(got))
	}
	byFile := map[string][]byte{}
	for _, src := range got {
		byFile[src.File] = src.Content
	}
	if string(byFile["go/extensions.bzl"]) != string(srcs[0].Content) {
		t.Errorf("go/extensions.bzl content roundtrip failed")
	}
	if string(byFile["private/extensions.bzl"]) != string(srcs[1].Content) {
		t.Errorf("private/extensions.bzl content roundtrip failed")
	}
}

// Re-ingest replaces prior sources.
func TestModuleExtensionSources_ReIngestReplaces(t *testing.T) {
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

	if err := s.WriteModuleExtensionSources(ctx, "m", "1", []store.ModuleExtensionSource{
		{File: "old.bzl", Content: []byte("old")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteModuleExtensionSources(ctx, "m", "1", []store.ModuleExtensionSource{
		{File: "new.bzl", Content: []byte("new")},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetModuleExtensionSources(ctx, "m", "1")
	if len(got) != 1 || got[0].File != "new.bzl" {
		t.Errorf("re-ingest didn't replace: %+v", got)
	}
}

// Empty input → clears prior rows (the producer no longer declares
// extensions in a re-ingest, e.g.).
func TestModuleExtensionSources_EmptyClears(t *testing.T) {
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
	if err := s.WriteModuleExtensionSources(ctx, "m", "1", []store.ModuleExtensionSource{
		{File: "a.bzl", Content: []byte("x")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteModuleExtensionSources(ctx, "m", "1", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetModuleExtensionSources(ctx, "m", "1")
	if len(got) != 0 {
		t.Errorf("empty write should clear, got %d", len(got))
	}
}
