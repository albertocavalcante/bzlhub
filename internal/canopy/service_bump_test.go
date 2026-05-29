package canopy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/store"
)

func TestServiceBump_MirrorsAndIndexesFakeRegistry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "canopy.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	module := "foo"
	version := "1.2.3"
	prefix := module + "-" + version
	tgz := buildSyntheticTarGz(t, prefix, map[string]string{
		"MODULE.bazel": `module(name = "wrong_name", version = "0.0.0")`,
		"defs.bzl":     "def helper():\n    pass\n",
	})
	archiveSum := sha256.Sum256(tgz)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(archiveSum[:])

	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/modules/foo/metadata.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"versions":["1.2.3"],"homepage":"https://example.com/foo"}`))
	})
	mux.HandleFunc("/modules/foo/1.2.3/source.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fetch.SourceJSON{
			Type:        "archive",
			URL:         baseURL + "/blobs/foo-1.2.3.tar.gz",
			Integrity:   integrity,
			StripPrefix: prefix,
		})
	})
	mux.HandleFunc("/modules/foo/1.2.3/MODULE.bazel", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`module(name = "foo", version = "1.2.3")`))
	})
	mux.HandleFunc("/blobs/foo-1.2.3.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tgz)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	fetch.SetDefaultAllowedHosts([]string{hostFromURL(t, baseURL)})
	t.Cleanup(func() { fetch.SetDefaultAllowedHosts(nil) })

	mirrorRoot := t.TempDir()
	svc := New(db)
	svc.MirrorRoot = mirrorRoot
	svc.DefaultUpstream = baseURL

	got, err := svc.Bump(ctx, api.BumpOptions{Module: module, Version: version, Source: "test"})
	if err != nil {
		t.Fatalf("Bump: %v", err)
	}
	if got.Name != module || got.Version != version {
		t.Fatalf("report coords = %s@%s, want %s@%s", got.Name, got.Version, module, version)
	}

	stored, err := svc.GetModuleVersion(ctx, module, version)
	if err != nil {
		t.Fatalf("GetModuleVersion: %v", err)
	}
	if stored.Name != module || stored.Version != version {
		t.Fatalf("stored coords = %s@%s, want %s@%s", stored.Name, stored.Version, module, version)
	}
	if size, err := svc.GetTarballSize(ctx, module, version); err != nil {
		t.Fatalf("GetTarballSize: %v", err)
	} else if size != int64(len(tgz)) {
		t.Fatalf("tarball size = %d, want %d", size, len(tgz))
	}

	assertFileContains(t, filepath.Join(mirrorRoot, "bazel_registry.json"), "{}")
	assertFileContains(t, filepath.Join(mirrorRoot, "modules", module, version, "MODULE.bazel"), `name = "foo"`)
	assertFileContains(t, filepath.Join(mirrorRoot, "modules", module, version, "source.json"), "/blobs/foo-1.2.3.tar.gz")
	assertFileContains(t, filepath.Join(mirrorRoot, "modules", module, "metadata.json"), `"homepage": "https://example.com/foo"`)
	assertFileContains(t, filepath.Join(mirrorRoot, "modules", module, "metadata.json"), `"1.2.3"`)

	blobPath := filepath.Join(mirrorRoot, "blobs", hex.EncodeToString(archiveSum[:]))
	if st, err := os.Stat(blobPath); err != nil {
		t.Fatalf("mirrored blob missing at %s: %v", blobPath, err)
	} else if st.Size() != int64(len(tgz)) {
		t.Fatalf("mirrored blob size = %d, want %d", st.Size(), len(tgz))
	}
}

func TestServiceBump_DeniesArchiveOutsideAllowlist(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "canopy.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/modules/foo/1.2.3/source.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fetch.SourceJSON{
			Type:        "archive",
			URL:         "http://denied.invalid/foo-1.2.3.tar.gz",
			StripPrefix: "foo-1.2.3",
		})
	})
	mux.HandleFunc("/modules/foo/1.2.3/MODULE.bazel", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`module(name = "foo", version = "1.2.3")`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	fetch.SetDefaultAllowedHosts([]string{hostFromURL(t, baseURL)})
	t.Cleanup(func() { fetch.SetDefaultAllowedHosts(nil) })

	mirrorRoot := t.TempDir()
	svc := New(db)
	svc.MirrorRoot = mirrorRoot
	svc.DefaultUpstream = baseURL

	_, err = svc.Bump(ctx, api.BumpOptions{Module: "foo", Version: "1.2.3", Source: "test"})
	if !errors.Is(err, fetch.ErrEgressDenied) {
		t.Fatalf("Bump err = %v, want ErrEgressDenied", err)
	}
	if entries, readErr := os.ReadDir(filepath.Join(mirrorRoot, "blobs")); readErr == nil && len(entries) > 0 {
		t.Fatalf("blob temp files leaked after denied archive fetch: %v", entries)
	}
}

func buildSyntheticTarGz(t *testing.T, prefix string, files map[string]string) []byte {
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
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func hostFromURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u.Hostname()
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(b, []byte(want)) {
		t.Fatalf("%s = %q, want substring %q", path, b, want)
	}
}
