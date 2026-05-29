package verify

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// TestCheckDeep_MatchingReport: when the stored report comes straight
// from a fresh assay of the same tarball, the diff is empty so no
// findings fire.
func TestCheckDeep_MatchingReport(t *testing.T) {
	fm, key := buildDeepFixture(t, "rule_a")
	st := mustBuildState(t, fm)
	got := checkDeep(context.Background(), st)
	for _, f := range got {
		if f.Module == key.name && f.Version == key.version {
			t.Errorf("unexpected finding: %+v", f)
		}
	}
}

// TestCheckDeep_TamperedStoredReport: drop a rule from the stored
// report so a re-assay of the same blob disagrees. Verify surfaces a
// deep_report_mismatch finding with non-zero diff counts.
func TestCheckDeep_TamperedStoredReport(t *testing.T) {
	fm, key := buildDeepFixture(t, "rule_a", "rule_b")

	// Hand-edit the stored report to remove rule_b: GetReport returns
	// what we WriteReport, so writing a tampered version models a JSON
	// edit done outside canopy.
	stored, err := fm.store.GetReport(context.Background(), key.name, key.version)
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	var kept []report.RuleSpec
	for _, r := range stored.Rules {
		if r.Name != "rule_b" {
			kept = append(kept, r)
		}
	}
	stored.Rules = kept
	if err := fm.store.WriteReport(context.Background(), stored); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	st := mustBuildState(t, fm)
	got := checkDeep(context.Background(), st)
	var found *Finding
	for i := range got {
		if got[i].Module == key.name {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("want a deep_report_mismatch finding for %s; got %+v", key.name, got)
	}
	if found.Kind != KindDeepReportMismatch || found.Severity != SevError {
		t.Errorf("kind/severity: got %s/%s", found.Severity, found.Kind)
	}
	if found.Details["rules_added"].(int) == 0 && found.Details["rules_removed"].(int) == 0 {
		t.Errorf("expected non-zero rule diff in Details: %+v", found.Details)
	}
}

// TestCheckDeep_NoStore: without a configured store, the deep check
// returns zero findings rather than crashing.
func TestCheckDeep_NoStore(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		skipDB: true,
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkDeep(context.Background(), mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0; got %+v", got)
	}
}

// TestCheckDeep_InvalidArchive: when the on-disk blob isn't a real
// archive, the deep check surfaces an Error finding rather than
// panicking. The blob is present (so blob_integrity is happy — the
// hash matches the garbage bytes) but extract+assay fails.
func TestCheckDeep_InvalidArchive(t *testing.T) {
	body := []byte("not really an archive")
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: body, indexed: true},
		},
	})
	got := checkDeep(context.Background(), mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1 finding; got %+v", got)
	}
	if got[0].Kind != KindDeepReportMismatch || got[0].Severity != SevError {
		t.Errorf("want Error deep_report_mismatch; got %s/%s", got[0].Severity, got[0].Kind)
	}
}

// TestAnalyzeBlob_ZipArchive: the deep check supports zip-shaped
// archives because BCR allows zip sources. We verify the analyzeBlob
// helper routes through the right extractor.
func TestAnalyzeBlob_ZipArchive(t *testing.T) {
	zipBytes := buildZip(t, map[string]string{
		"MODULE.bazel": "module(name = \"zfoo\", version = \"1.0.0\")\n",
	})
	dir := t.TempDir()
	blobPath := filepath.Join(dir, "blob.zip")
	must(t, os.WriteFile(blobPath, zipBytes, 0o644))
	staging := t.TempDir()
	r, err := analyzeBlob(t.Context(), blobPath, "", staging)
	if err != nil {
		t.Fatalf("analyzeBlob: %v", err)
	}
	if r.Name != "zfoo" {
		t.Errorf("name: want zfoo; got %q", r.Name)
	}
}

// TestAnalyzeBlob_UnknownFormat: a non-tar.gz, non-zip blob surfaces a
// "unknown archive format" error rather than panicking. Keeps the
// deep check tolerant of operator weirdness.
func TestAnalyzeBlob_UnknownFormat(t *testing.T) {
	dir := t.TempDir()
	blobPath := filepath.Join(dir, "blob.bin")
	must(t, os.WriteFile(blobPath, []byte("RAR!\x1a\x07\x00"), 0o644))
	_, err := analyzeBlob(t.Context(), blobPath, "", t.TempDir())
	if err == nil {
		t.Fatalf("want error for unknown archive; got nil")
	}
}

// TestVerify_DeepEnabled: end-to-end Verify with Deep=true runs the
// deep check. Uses a real tarball so the path is exercised.
func TestVerify_DeepEnabled(t *testing.T) {
	fm, _ := buildDeepFixture(t, "rule_a")
	r, err := Verify(context.Background(), Options{
		MirrorRoot: fm.root,
		DBPath:     fm.dbPath,
		Deep:       true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Errors != 0 {
		t.Errorf("clean deep run should produce 0 errors; got %d (%+v)", r.Errors, r.Findings)
	}
}

// buildDeepFixture writes a minimal tar.gz holding a MODULE.bazel +
// rules.bzl, ingests it via WriteReport, and returns a fakeMirror
// wired up so checkDeep has everything it needs.
func buildDeepFixture(t *testing.T, ruleNames ...string) (*fakeMirror, moduleKey) {
	t.Helper()
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "modules"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "blobs"), 0o755))

	const moduleName = "deepfoo"
	const moduleVersion = "1.0.0"

	// Build the MODULE.bazel + a rules.bzl that defines the rules.
	moduleBzl := "module(name = \"" + moduleName + "\", version = \"" + moduleVersion + "\")\n"
	var bzlBuf bytes.Buffer
	bzlBuf.WriteString("def _impl(ctx): pass\n")
	for _, n := range ruleNames {
		bzlBuf.WriteString(n)
		bzlBuf.WriteString(" = rule(implementation = _impl)\n")
	}

	// Build a tar.gz whose top-level layout matches a typical bazel
	// module (no strip_prefix needed).
	tarballBytes := buildTarGz(t, map[string]string{
		"MODULE.bazel": moduleBzl,
		"defs.bzl":     bzlBuf.String(),
	})

	// Content-address the tarball.
	sum := sha256.Sum256(tarballBytes)
	blobHex := hex.EncodeToString(sum[:])
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	// Write blob + source.json + MODULE.bazel into the BCR tree.
	must(t, os.WriteFile(filepath.Join(root, "blobs", blobHex), tarballBytes, 0o644))
	moduleDir := filepath.Join(root, "modules", moduleName, moduleVersion)
	must(t, os.MkdirAll(moduleDir, 0o755))
	src := map[string]any{
		"type":         "archive",
		"url":          "https://example.invalid/x.tar.gz",
		"integrity":    integrity,
		"strip_prefix": "",
	}
	srcJSON, _ := json.MarshalIndent(src, "", "  ")
	must(t, os.WriteFile(filepath.Join(moduleDir, "source.json"), srcJSON, 0o644))
	must(t, os.WriteFile(filepath.Join(moduleDir, "MODULE.bazel"), []byte(moduleBzl), 0o644))

	// Open a store and seed it by extracting+assaying the blob, then
	// storing the result. This produces a "correct" stored report; the
	// test then mutates it to model a tamper.
	dbPath := filepath.Join(t.TempDir(), "canopy.db")
	fm := &fakeMirror{root: root, dbPath: dbPath}
	openStore(t, fm)

	tmp := t.TempDir()
	blobPath := filepath.Join(root, "blobs", blobHex)
	freshReport, err := analyzeBlob(t.Context(), blobPath, "", tmp)
	if err != nil {
		t.Fatalf("seed analyze: %v", err)
	}
	freshReport.Name = moduleName
	freshReport.Version = moduleVersion
	if err := fm.store.WriteReport(context.Background(), freshReport); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	return fm, moduleKey{moduleName, moduleVersion}
}

// openStore is a tiny shim that opens fm.store and registers the
// cleanup. Used by deep tests that build the layout manually (the
// buildFakeMirror happy-path helper doesn't fit because it auto-
// generates source.json / blobs from byte literals).
func openStore(t *testing.T, fm *fakeMirror) {
	t.Helper()
	// Re-use buildFakeMirror's store setup by opening directly.
	importedStore, err := storeOpen(t, fm.dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = importedStore.Close() })
	fm.store = importedStore
}

// buildZip builds an in-memory zip from a name→content map.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// buildTarGz builds an in-memory tar.gz from a name→content map. Tests
// for the deep check need real archives to push through extract+assay,
// but the actual file layout is trivial — three files under root.
func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}
