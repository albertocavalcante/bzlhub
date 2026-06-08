package ingest_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/ingest"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// writeModuleDir creates a temp dir with a MODULE.bazel using the given
// name/version in the module(...) call — the BCR tarball convention is
// to ship a placeholder version (often "0.0.0") and let source.json
// supply the real one.
func writeModuleDir(t *testing.T, name, version string) string {
	t.Helper()
	dir := t.TempDir()
	body := "module(name = \"" + name + "\", version = \"" + version + "\")\n"
	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte(body), 0o644); err != nil {
		t.Fatalf("write MODULE.bazel: %v", err)
	}
	return dir
}

// TestAnalyze_NoStoreSideEffect: Analyze must extract the report without
// touching the store. The caller overrides Name/Version to the canonical
// BCR coord and only then writes.
func TestAnalyze_NoStoreSideEffect(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dir := writeModuleDir(t, "rules_python", "0.0.0")
	r, err := ingest.Analyze(ctx, dir)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.Name != "rules_python" || r.Version != "0.0.0" {
		t.Errorf("Analyze: got %s@%s, want rules_python@0.0.0", r.Name, r.Version)
	}
	vs, err := s.ListAllVersions(ctx)
	if err != nil {
		t.Fatalf("ListAllVersions: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("Analyze must not write to store; got %d rows: %+v", len(vs), vs)
	}
}

// TestRegistryIngest_NoPhantomVersionRow: simulates the registry-mode
// flow — Analyze the tarball dir (MODULE.bazel says version "0.0.0"),
// override with the canonical BCR coord, then WriteReport once. The
// index must contain exactly one (module, version) row at the canonical
// version, never the placeholder.
func TestRegistryIngest_NoPhantomVersionRow(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dir := writeModuleDir(t, "rules_python", "0.0.0")
	const canonModule = "rules_python"
	const canonVersion = "0.40.0"

	r, err := ingest.Analyze(ctx, dir)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	r.Name = canonModule
	r.Version = canonVersion
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	vs, err := s.ListAllVersions(ctx)
	if err != nil {
		t.Fatalf("ListAllVersions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("want exactly 1 version row, got %d: %+v", len(vs), vs)
	}
	if vs[0].Module != canonModule || vs[0].Version != canonVersion {
		t.Errorf("want %s@%s, got %s@%s", canonModule, canonVersion, vs[0].Module, vs[0].Version)
	}
}

// TestFromDir_StillWrites: the local-dir codepath must keep writing —
// Analyze splits cleanly but FromDir's contract (analyze + store) is
// unchanged.
func TestFromDir_StillWrites(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dir := writeModuleDir(t, "foo", "1.2.3")
	r, err := ingest.FromDir(ctx, s, dir)
	if err != nil {
		t.Fatalf("FromDir: %v", err)
	}
	if r.Name != "foo" || r.Version != "1.2.3" {
		t.Errorf("FromDir: got %s@%s, want foo@1.2.3", r.Name, r.Version)
	}
	vs, err := s.ListAllVersions(ctx)
	if err != nil {
		t.Fatalf("ListAllVersions: %v", err)
	}
	if len(vs) != 1 || vs[0].Module != "foo" || vs[0].Version != "1.2.3" {
		t.Errorf("want exactly foo@1.2.3, got %+v", vs)
	}
}

// buildTarGz produces an in-memory tar.gz with "<prefix>/<file>" entries.
func buildTarGz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     prefix + "/" + name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar body: %v", err)
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

// TestFromMirroredVersion: simulates the watch-mode flow. A worktree at
// disk has modules/foo/0.40.0/source.json + MODULE.bazel; the tarball
// pointed to by source.json carries a placeholder version "0.0.0". The
// resulting report must carry the canonical (foo, 0.40.0) coord, and
// the store must contain exactly one matching version row.
func TestFromMirroredVersion(t *testing.T) {
	tgz := buildTarGz(t, "foo-0.40.0", map[string]string{
		"MODULE.bazel": `module(name="foo", version="0.0.0")`,
	})
	sum := sha256.Sum256(tgz)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/foo-0.40.0.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tgz)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	worktree := t.TempDir()
	mirrorDir := filepath.Join(worktree, "modules", "foo", "0.40.0")
	if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	srcJSON, err := json.Marshal(fetch.SourceJSON{
		Type:        "archive",
		URL:         srv.URL + "/foo-0.40.0.tar.gz",
		Integrity:   integrity,
		StripPrefix: "foo-0.40.0",
	})
	if err != nil {
		t.Fatalf("marshal source.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mirrorDir, "source.json"), srcJSON, 0o644); err != nil {
		t.Fatalf("write source.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mirrorDir, "MODULE.bazel"),
		[]byte(`module(name="foo", version="0.0.0")`), 0o644); err != nil {
		t.Fatalf("write MODULE.bazel: %v", err)
	}

	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r, err := ingest.FromMirroredVersion(ctx, s, worktree, "foo", "0.40.0")
	if err != nil {
		t.Fatalf("FromMirroredVersion: %v", err)
	}
	if r.Name != "foo" || r.Version != "0.40.0" {
		t.Errorf("canonical override missing: got %s@%s, want foo@0.40.0", r.Name, r.Version)
	}
	vs, err := s.ListAllVersions(ctx)
	if err != nil {
		t.Fatalf("ListAllVersions: %v", err)
	}
	if len(vs) != 1 || vs[0].Module != "foo" || vs[0].Version != "0.40.0" {
		t.Errorf("store: want exactly foo@0.40.0, got %+v", vs)
	}
}

func TestFromMirroredVersion_NilStoreRejected(t *testing.T) {
	_, err := ingest.FromMirroredVersion(context.Background(), nil, t.TempDir(), "foo", "1.0.0")
	if err == nil {
		t.Fatal("want error on nil store, got nil")
	}
}

func TestFromMirroredVersion_MissingSourceJSON(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// worktree exists, but the modules/foo/1.0.0/ directory does not.
	_, err = ingest.FromMirroredVersion(ctx, s, t.TempDir(), "foo", "1.0.0")
	if err == nil {
		t.Fatal("want error on missing source.json, got nil")
	}
}
