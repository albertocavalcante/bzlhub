package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckBlobIntegrity_Good: a well-formed module+blob pair produces
// zero findings — the happy path.
func TestCheckBlobIntegrity_Good(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("good archive bytes")},
		},
	})
	st := mustBuildState(t, fm)
	got := checkBlobIntegrity(st)
	if len(got) != 0 {
		t.Fatalf("want 0 findings; got %d: %+v", len(got), got)
	}
}

// TestCheckBlobIntegrity_Tampered: after rewriting the blob bytes
// post-hoc, the computed SHA no longer matches source.json's integrity,
// so we expect exactly one Error finding of kind blob_integrity.
func TestCheckBlobIntegrity_Tampered(t *testing.T) {
	body := []byte("original archive bytes")
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: body},
		},
	})
	// Tamper: locate the canonical blob path by hash and overwrite.
	hexname, _ := blobBytesFor(body)
	tamperedPath := filepath.Join(fm.root, "blobs", hexname)
	must(t, os.WriteFile(tamperedPath, []byte("GARBAGE"), 0o644))

	st := mustBuildState(t, fm)
	got := checkBlobIntegrity(st)
	if len(got) != 1 {
		t.Fatalf("want 1 finding; got %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Kind != KindBlobIntegrity || f.Severity != SevError {
		t.Errorf("want Error blob_integrity; got %s/%s", f.Severity, f.Kind)
	}
	if f.Module != "foo" || f.Version != "1.0.0" {
		t.Errorf("module/version: want foo/1.0.0; got %s/%s", f.Module, f.Version)
	}
	if f.Details["expected_sha256_hex"] == nil || f.Details["actual_sha256_hex"] == nil {
		t.Errorf("expected expected_/actual_sha256_hex in Details: %+v", f.Details)
	}
}

// TestCheckBlobIntegrity_Missing: source.json names a blob that isn't
// on disk. Yields one Error of kind blob_missing (a separate Kind to
// keep CI gates that scope on "integrity mismatch" precise).
func TestCheckBlobIntegrity_Missing(t *testing.T) {
	body := []byte("would-be archive")
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: body, skipBlob: true},
		},
	})
	st := mustBuildState(t, fm)
	got := checkBlobIntegrity(st)
	if len(got) != 1 {
		t.Fatalf("want 1 finding; got %d: %+v", len(got), got)
	}
	if got[0].Kind != KindBlobMissing || got[0].Severity != SevError {
		t.Errorf("want Error blob_missing; got %s/%s", got[0].Severity, got[0].Kind)
	}
}

// TestCheckBlobIntegrity_SkipsUnparseableSource: when source.json
// doesn't parse, blob_integrity stays silent so the report doesn't
// double-flag the same module — schema check owns it.
func TestCheckBlobIntegrity_SkipsUnparseableSource(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", source: "not valid json", skipBlob: true},
		},
	})
	got := checkBlobIntegrity(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0 (handled by schema check); got %+v", got)
	}
}

// TestCheckBlobIntegrity_Empty: zero modules → zero findings, examined=0.
func TestCheckBlobIntegrity_Empty(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{})
	st := mustBuildState(t, fm)
	got := checkBlobIntegrity(st)
	if len(got) != 0 {
		t.Fatalf("want 0 findings on empty mirror; got %d: %+v", len(got), got)
	}
}

// mustBuildState is the per-check test entry point: wraps buildState
// and surfaces tool-level errors as t.Fatal so individual check tests
// can focus on the findings produced by their target function.
func mustBuildState(t *testing.T, fm *fakeMirror) *state {
	t.Helper()
	s, err := buildState(context.Background(), fm.root, fm.store)
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	return s
}
