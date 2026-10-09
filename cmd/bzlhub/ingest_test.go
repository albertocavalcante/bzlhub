package main

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
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/scip-bazel/pkg/bzlmod"

	"github.com/albertocavalcante/bzlhub/internal/ingest"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func ingestTestTarGz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: prefix + "/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRegistry serves a BCR-shape registry for the given module@1.0.0 entries
// (MODULE.bazel text plus extra source files per module) over httptest.
func fakeRegistry(t *testing.T, mods map[string]map[string]string) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for name, files := range mods {
		prefix := name + "-1.0.0"
		tgz := ingestTestTarGz(t, prefix, files)
		sum := sha256.Sum256(tgz)
		src, err := json.Marshal(map[string]string{
			"type":         "archive",
			"url":          srv.URL + "/" + prefix + ".tar.gz",
			"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(sum[:]),
			"strip_prefix": prefix,
		})
		if err != nil {
			t.Fatal(err)
		}
		mux.HandleFunc("/modules/"+name+"/1.0.0/source.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(src) })
		mux.HandleFunc("/modules/"+name+"/1.0.0/MODULE.bazel", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(files["MODULE.bazel"]))
		})
		mux.HandleFunc("/"+prefix+".tar.gz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tgz) })
	}
	return srv.URL
}

// TestIngestRecursive_StoresRootIndex guards the regression where --recursive
// returned after the mirror walk, leaving the store empty: the root module must
// still get its report, SCIP blob, and has_source_index flag.
func TestIngestRecursive_StoresRootIndex(t *testing.T) {
	reg := fakeRegistry(t, map[string]map[string]string{
		"rootmod": {
			"MODULE.bazel": `module(name = "rootmod", version = "1.0.0")` + "\n" + `bazel_dep(name = "depmod", version = "1.0.0")` + "\n",
			"defs.bzl":     "def root_macro():\n    pass\n",
		},
		"depmod": {
			"MODULE.bazel": `module(name = "depmod", version = "1.0.0")` + "\n",
			"defs.bzl":     "def dep_macro():\n    pass\n",
		},
	})
	dbPath := filepath.Join(t.TempDir(), "bzlhub.db")
	mirrorDir := filepath.Join(t.TempDir(), "mirror")

	cmd := newIngestCmd()
	cmd.SetArgs([]string{"rootmod@1.0.0", "--from", reg, "--recursive", "--mirror-to", mirrorDir, "--db", dbPath})
	cmd.SetContext(t.Context())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ingest --recursive: %v", err)
	}

	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	blob, err := s.GetScipBlob(ctx, "rootmod", "1.0.0")
	if err != nil || len(blob) == 0 {
		t.Fatalf("root SCIP blob missing: len=%d err=%v", len(blob), err)
	}
	has, err := s.GetHasSourceIndex(ctx, "rootmod", "1.0.0")
	if err != nil || !has {
		t.Fatalf("has_source_index = %v, err = %v; want true", has, err)
	}
	// The dependency is mirrored, not ingested.
	if _, err := os.Stat(filepath.Join(mirrorDir, "modules", "depmod", "1.0.0", "MODULE.bazel")); err != nil {
		t.Errorf("dependency not mirrored: %v", err)
	}
}

func TestIndexModule_WarnsOnUnresolvedRepos(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"MODULE.bazel": `module(name = "m", version = "1.0.0")` + "\n",
		"defs.bzl":     `load("@ghost_repo//:x.bzl", "x")` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// A version row must exist for the has_source_index update to land.
	r := mustAnalyze(t, ctx, dir)
	if err := s.WriteReport(ctx, r); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	if err := indexModule(ctx, s, dir, "m", "1.0.0", bzlmod.Closure{}, &warn); err != nil {
		t.Fatalf("indexModule: %v", err)
	}
	want := "warn: m@1.0.0 has unresolved references to repos: @ghost_repo//:x.bzl"
	if !strings.Contains(warn.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", warn.String(), want)
	}
	if blob, err := s.GetScipBlob(ctx, "m", "1.0.0"); err != nil || len(blob) == 0 {
		t.Errorf("blob not stored despite warning: len=%d err=%v", len(blob), err)
	}
}

func mustAnalyze(t *testing.T, ctx context.Context, dir string) *report.ModuleReport {
	t.Helper()
	r, err := ingest.Analyze(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
