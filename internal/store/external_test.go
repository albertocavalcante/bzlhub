package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// seedVersion writes a minimal ModuleReport so external_refs's FK
// to versions(module_name, version) is satisfied.
func seedVersion(t *testing.T, s *store.Store, ctx context.Context, name, version string) {
	t.Helper()
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: name, Version: version}); err != nil {
		t.Fatalf("seed version: %v", err)
	}
}

func TestExternalRefs_RoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	seedVersion(t, s, ctx, "foo", "1.0.0")

	refs := []store.ExternalRef{
		{
			URL: "https://dl.google.com/go/go1.21.0.linux-amd64.tar.gz",
			Host: "dl.google.com", Class: "vendor-http", Mutability: "immutable",
			SHA256: "abc", APIName: "ctx.download_and_extract",
			RuleName: "go_download_sdk_rule", Platform: "linux/amd64",
			File: "go/private/sdk.bzl",
		},
		{
			URL: "https://github.com/foo/bar/archive/v1.0.tar.gz",
			Host: "github.com", Class: "github-archive", Mutability: "mutable-host",
			APIName: "ctx.download_and_extract",
			RuleName: "my_repo", Platform: "any",
			File: "deps.bzl",
		},
	}
	forkErrs := []store.ExternalForkError{
		{Platform: "windows/amd64", Message: "fail: not supported"},
	}

	if err := s.WriteExternalRefs(ctx, "foo", "1.0.0", refs, forkErrs); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := s.GetExternalRefs(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("get refs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("refs = %d, want 2", len(got))
	}
	// Class-sorted: github-archive < vendor-http (alpha).
	if got[0].Class != "github-archive" {
		t.Errorf("first class = %q, want github-archive", got[0].Class)
	}
	if got[1].Host != "dl.google.com" {
		t.Errorf("second host = %q, want dl.google.com", got[1].Host)
	}

	gotErrs, err := s.GetExternalForkErrors(ctx, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("get fork errors: %v", err)
	}
	if len(gotErrs) != 1 || gotErrs[0].Platform != "windows/amd64" {
		t.Errorf("fork errors = %v", gotErrs)
	}
}

func TestExternalRefs_ReIngestReplaces(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	seedVersion(t, s, ctx, "m", "1")

	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://old.example.com/a", Host: "old.example.com", Class: "unknown", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://new.example.com/a", Host: "new.example.com", Class: "unknown", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetExternalRefs(ctx, "m", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("refs after re-ingest = %d, want 1", len(got))
	}
	if got[0].Host != "new.example.com" {
		t.Errorf("old row leaked: %q", got[0].URL)
	}
}

func TestExternalRefs_TaintedBool(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	seedVersion(t, s, ctx, "m", "1")

	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://x", Host: "x", Class: "unknown", Platform: "any", Tainted: true},
	}, nil); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetExternalRefs(ctx, "m", "1")
	if len(got) != 1 || !got[0].Tainted {
		t.Errorf("tainted bool lost: %+v", got)
	}
}
