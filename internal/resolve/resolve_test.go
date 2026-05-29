package resolve

import (
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

	"github.com/albertocavalcante/canopy/internal/fetch"
)

// startFakeRegistry serves a single foo@1.0.0 module backed by a synthetic
// tar.gz with known content + correct SRI. Returns the registry base URL.
func startFakeRegistry(t *testing.T) (string, func()) {
	t.Helper()

	// Build a tar.gz with "foo-1.0.0/MODULE.bazel" inside.
	tgz := buildSyntheticTarGz(t, "foo-1.0.0", map[string]string{
		"MODULE.bazel": `module(name="foo", version="1.0.0")`,
	})
	sum := sha256.Sum256(tgz)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	mux := http.NewServeMux()
	var baseURL string
	mux.HandleFunc("/modules/foo/1.0.0/source.json", func(w http.ResponseWriter, r *http.Request) {
		s := fetch.SourceJSON{
			Type:        "archive",
			URL:         baseURL + "/blobs/foo-1.0.0.tar.gz",
			Integrity:   integrity,
			StripPrefix: "foo-1.0.0",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/blobs/foo-1.0.0.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tgz)
	})
	srv := httptest.NewServer(mux)
	baseURL = srv.URL
	return baseURL, srv.Close
}

func TestFromRegistryHappyPath(t *testing.T) {
	reg, stop := startFakeRegistry(t)
	defer stop()

	m, err := FromRegistry(context.Background(), reg, "foo", "1.0.0")
	if err != nil {
		t.Fatalf("FromRegistry: %v", err)
	}
	defer m.Cleanup()

	// Strip prefix should have collapsed; MODULE.bazel lives at dir root.
	p := filepath.Join(m.Dir, "MODULE.bazel")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read MODULE.bazel: %v", err)
	}
	if !strings.Contains(string(b), `name="foo"`) {
		t.Fatalf("unexpected MODULE.bazel content: %q", b)
	}
	if m.Source.Integrity == "" {
		t.Fatalf("Source.Integrity not populated")
	}
}

// TestFromRegistryFallsBackToStandaloneModuleBazel covers the real-world
// case where the source tarball doesn't bundle MODULE.bazel — many older
// BCR modules (rules_cc 0.0.1, platforms 0.0.4, googletest 1.11.0, ...)
// rely on the registry serving it as a separate file. Without this
// fallback, every assay.Analyze on those modules would fail.
func TestFromRegistryFallsBackToStandaloneModuleBazel(t *testing.T) {
	// Tarball has source files but NO MODULE.bazel.
	tgz := buildSyntheticTarGz(t, "noroot-1.0.0", map[string]string{
		"BUILD.bazel":    `# placeholder`,
		"src/lib.bzl":    `def helper(): pass`,
	})
	sum := sha256.Sum256(tgz)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	registryModule := `module(name="noroot", version="1.0.0")`

	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/modules/noroot/1.0.0/source.json", func(w http.ResponseWriter, _ *http.Request) {
		s := fetch.SourceJSON{
			Type:        "archive",
			URL:         baseURL + "/blobs/noroot-1.0.0.tar.gz",
			Integrity:   integrity,
			StripPrefix: "noroot-1.0.0",
		}
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/modules/noroot/1.0.0/MODULE.bazel", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(registryModule))
	})
	mux.HandleFunc("/blobs/noroot-1.0.0.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tgz)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	m, err := FromRegistry(context.Background(), baseURL, "noroot", "1.0.0")
	if err != nil {
		t.Fatalf("FromRegistry: %v", err)
	}
	defer m.Cleanup()

	b, err := os.ReadFile(filepath.Join(m.Dir, "MODULE.bazel"))
	if err != nil {
		t.Fatalf("read MODULE.bazel after fallback: %v", err)
	}
	if string(b) != registryModule {
		t.Fatalf("fallback wrote wrong content:\n  got: %q\n  want: %q", b, registryModule)
	}
}

// TestFromRegistryAcceptsZipArchive covers the modern BCR reality that
// many modules (protobuf, rules_jvm_external, zlib, ...) are published
// as zip archives, not tar.gz. The resolver must dispatch by
// archive_type / URL extension.
func TestDetectArchiveKind(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		url      string
		want     archiveKind
		wantErr  bool
	}{
		{"declared tar.gz wins", "tar.gz", "https://example.com/x.zip", archiveKindTarGz, false},
		{"declared tgz alias", "TGZ", "", archiveKindTarGz, false},
		{"declared zip wins", "zip", "https://example.com/x.tar.gz", archiveKindZip, false},
		{"unknown declared type errors", "rar", "", 0, true},
		{"sniff .zip from URL", "", "https://github.com/x/releases/foo.zip", archiveKindZip, false},
		{"sniff .tar.gz from URL", "", "https://github.com/x/releases/foo.tar.gz", archiveKindTarGz, false},
		{"sniff .tgz from URL", "", "https://example.com/foo.tgz", archiveKindTarGz, false},
		{"querystring is ignored", "", "https://example.com/foo.zip?token=abc", archiveKindZip, false},
		{"fragment is ignored", "", "https://example.com/foo.zip#frag", archiveKindZip, false},
		{"unknown extension defaults to tar.gz", "", "https://example.com/foo", archiveKindTarGz, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := detectArchiveKind(c.declared, c.url)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestFromRegistryAcceptsZipArchive(t *testing.T) {
	zbytes := buildSyntheticZip(t, "zfoo-1.0.0", map[string]string{
		"MODULE.bazel": `module(name="zfoo", version="1.0.0")`,
		"defs.bzl":     `def helper(): pass`,
	})
	sum := sha256.Sum256(zbytes)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/modules/zfoo/1.0.0/source.json", func(w http.ResponseWriter, _ *http.Request) {
		s := fetch.SourceJSON{
			Type:        "archive",
			URL:         baseURL + "/blobs/zfoo-1.0.0.zip",
			Integrity:   integrity,
			StripPrefix: "zfoo-1.0.0",
		}
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/blobs/zfoo-1.0.0.zip", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zbytes)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	m, err := FromRegistry(context.Background(), baseURL, "zfoo", "1.0.0")
	if err != nil {
		t.Fatalf("FromRegistry on zip: %v", err)
	}
	defer m.Cleanup()

	for _, p := range []string{"MODULE.bazel", "defs.bzl"} {
		if _, err := os.Stat(filepath.Join(m.Dir, p)); err != nil {
			t.Errorf("missing extracted %s: %v", p, err)
		}
	}
}

func TestFromRegistryIntegrityMismatch(t *testing.T) {
	// Server promises one integrity but serves different bytes.
	tgz := buildSyntheticTarGz(t, "foo-1.0.0", map[string]string{
		"MODULE.bazel": "actual content",
	})
	// Use a wrong integrity in source.json.
	bogus := "sha256-" + base64.StdEncoding.EncodeToString(make([]byte, 32))

	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/modules/foo/1.0.0/source.json", func(w http.ResponseWriter, r *http.Request) {
		s := fetch.SourceJSON{
			Type:        "archive",
			URL:         baseURL + "/blobs/foo-1.0.0.tar.gz",
			Integrity:   bogus,
			StripPrefix: "foo-1.0.0",
		}
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("/blobs/foo-1.0.0.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tgz)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	baseURL = srv.URL

	_, err := FromRegistry(context.Background(), baseURL, "foo", "1.0.0")
	if err == nil {
		t.Fatalf("expected integrity error, got nil")
	}
	if !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("error should mention integrity, got: %v", err)
	}
}
