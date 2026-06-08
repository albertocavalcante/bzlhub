package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	httpstore "github.com/albertocavalcante/go-bcr-httpstore"

	"github.com/albertocavalcante/bzlhub/internal/backend"
)

// writeSecretFile writes value to a 0600 temp file under t.TempDir
// and returns its path. Mirrors the secrets-from-files corporate-
// security-first pattern that bearer/basic auth read.
func writeSecretFile(t *testing.T, name, value string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// =================================================================
// Validate: parameter contracts
// =================================================================

func TestHTTPStoreConfig_Validate_KindRequired(t *testing.T) {
	cfg := backend.HTTPStoreConfig{BaseURL: "https://example.com"}
	if _, err := cfg.Build(http.DefaultClient); err == nil {
		t.Fatal("Build with empty Kind: want error, got nil")
	}
}

func TestHTTPStoreConfig_Validate_BadKind(t *testing.T) {
	cfg := backend.HTTPStoreConfig{Kind: "magic-store", BaseURL: "https://example.com"}
	_, err := cfg.Build(http.DefaultClient)
	if err == nil {
		t.Fatal("Build with unknown Kind: want error, got nil")
	}
	if !strings.Contains(err.Error(), "magic-store") {
		t.Errorf("error %q does not name the bad kind", err.Error())
	}
}

func TestHTTPStoreConfig_Validate_BaseURLRequired(t *testing.T) {
	cfg := backend.HTTPStoreConfig{Kind: "httpstore"}
	if _, err := cfg.Build(http.DefaultClient); err == nil {
		t.Fatal("Build with empty BaseURL: want error, got nil")
	}
}

func TestHTTPStoreConfig_Validate_ArtifactoryRequiresRepo(t *testing.T) {
	cfg := backend.HTTPStoreConfig{Kind: "artifactory", BaseURL: "https://x"}
	_, err := cfg.Build(http.DefaultClient)
	if err == nil {
		t.Fatal("artifactory with empty repo: want error, got nil")
	}
	if !strings.Contains(err.Error(), "BZLHUB_ARTIFACTORY_REPO") {
		t.Errorf("error %q does not name the missing knob", err.Error())
	}
}

// =================================================================
// Auth: secrets-from-files contract
// =================================================================

func TestHTTPStoreConfig_Build_BearerReadsFile(t *testing.T) {
	tok := writeSecretFile(t, "token", "bearer-secret-xyz")
	cfg := backend.HTTPStoreConfig{
		Kind:     "httpstore",
		BaseURL:  "https://example.com",
		AuthKind: "bearer",
		AuthFile: tok,
	}
	hs, err := cfg.Build(http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := hs.Store().AuthName(); got != "bearer" {
		t.Errorf("AuthName=%q, want bearer", got)
	}
}

func TestHTTPStoreConfig_Build_BearerMissingFile(t *testing.T) {
	cfg := backend.HTTPStoreConfig{
		Kind:     "httpstore",
		BaseURL:  "https://example.com",
		AuthKind: "bearer",
		AuthFile: filepath.Join(t.TempDir(), "does-not-exist"),
	}
	_, err := cfg.Build(http.DefaultClient)
	if err == nil {
		t.Fatal("missing bearer file: want error, got nil")
	}
}

func TestHTTPStoreConfig_Build_BearerRequiresFile(t *testing.T) {
	cfg := backend.HTTPStoreConfig{
		Kind:     "httpstore",
		BaseURL:  "https://example.com",
		AuthKind: "bearer",
	}
	_, err := cfg.Build(http.DefaultClient)
	if err == nil {
		t.Fatal("bearer with no file: want error, got nil")
	}
}

func TestHTTPStoreConfig_Build_BasicRequiresUserAndFile(t *testing.T) {
	pw := writeSecretFile(t, "pw", "hunter2")
	cases := []struct {
		name string
		cfg  backend.HTTPStoreConfig
	}{
		{"empty user", backend.HTTPStoreConfig{Kind: "httpstore", BaseURL: "https://x", AuthKind: "basic", AuthFile: pw}},
		{"empty file", backend.HTTPStoreConfig{Kind: "httpstore", BaseURL: "https://x", AuthKind: "basic", AuthUser: "alice"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.Build(http.DefaultClient); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestHTTPStoreConfig_Build_BasicOk(t *testing.T) {
	pw := writeSecretFile(t, "pw", "hunter2")
	cfg := backend.HTTPStoreConfig{
		Kind:     "httpstore",
		BaseURL:  "https://example.com",
		AuthKind: "basic",
		AuthUser: "alice",
		AuthFile: pw,
	}
	hs, err := cfg.Build(http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := hs.Store().AuthName(); got != "basic" {
		t.Errorf("AuthName=%q, want basic", got)
	}
}

func TestHTTPStoreConfig_Build_AnonymousDefault(t *testing.T) {
	cfg := backend.HTTPStoreConfig{
		Kind:    "httpstore",
		BaseURL: "https://example.com",
		// AuthKind unset → anonymous default
	}
	hs, err := cfg.Build(http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := hs.Store().AuthName(); got != "anonymous" {
		t.Errorf("AuthName=%q, want anonymous (default when unset)", got)
	}
}

func TestHTTPStoreConfig_Build_Artifactory_APIKeyHeader(t *testing.T) {
	key := writeSecretFile(t, "apikey", "AKCp8j...")
	cfg := backend.HTTPStoreConfig{
		Kind:            "artifactory",
		BaseURL:         "https://artifactory.example.com/artifactory",
		ArtifactoryRepo: "bcr-mirror",
		AuthKind:        "artifactory-api-key",
		AuthFile:        key,
	}
	hs, err := cfg.Build(http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := hs.Store().AuthName(); got != "custom-header:X-JFrog-Art-Api" {
		t.Errorf("AuthName=%q, want custom-header:X-JFrog-Art-Api", got)
	}
}

// =================================================================
// End-to-end: Artifactory shape against a httptest.Server
// =================================================================

func TestHTTPStoreConfig_Artifactory_E2E_ReadsBCRShape(t *testing.T) {
	// Simulate an Artifactory-shape server:
	//   - storage API at /api/storage/<repo>/... returns the
	//     documented children[] shape for listings
	//   - content at /modules/... served verbatim
	//
	// NOTE: this exercises the simplified deployment topology where
	// the BCR-shape tree is reachable directly under BaseURL without
	// the JFrog `/<repo>/` content-path prefix. Real JFrog Artifactory
	// puts content at /<repo>/<path>, while the v0.3 artifactory.Layout
	// + httpstore.Backend pair issues content reads against BaseURL
	// alone — a known mismatch tracked as a follow-up against
	// go-bcr-artifactory. For canopy M3, operators wanting real-world
	// JFrog topology should front Artifactory with an nginx/CDN
	// rewrite that adds the repo segment, OR configure BaseURL to
	// the repo-inclusive URL and accept double-rooted storage API
	// calls until the library evolves.
	mux := http.NewServeMux()

	mux.HandleFunc("/artifactory/api/storage/bcr-mirror/modules",
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"children": []map[string]any{
					{"uri": "/rules_go", "folder": true},
					{"uri": "/rules_python", "folder": true},
					{"uri": "/bazel_registry.json", "folder": false},
				},
			})
		})

	mux.HandleFunc("/artifactory/api/storage/bcr-mirror/modules/rules_go",
		func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"children": []map[string]any{
					{"uri": "/0.50.0", "folder": true},
					{"uri": "/metadata.json", "folder": false},
				},
			})
		})

	const metadataJSON = `{"homepage": "https://github.com/bazel-contrib/rules_go", "versions": ["0.50.0"]}`
	mux.HandleFunc("/artifactory/modules/rules_go/metadata.json",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, metadataJSON)
		})

	mux.HandleFunc("/artifactory/modules/rules_go/0.50.0/MODULE.bazel",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `module(name = "rules_go", version = "0.50.0")`)
		})

	// Default: 404 for anything else so tests notice missing routes.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := backend.HTTPStoreConfig{
		Kind:            "artifactory",
		BaseURL:         srv.URL + "/artifactory",
		ArtifactoryRepo: "bcr-mirror",
	}
	hs, err := cfg.Build(http.DefaultClient)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// 1. Read metadata.json through the canopy Backend interface.
	rc, err := hs.GetMetadata(context.Background(), "rules_go")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != metadataJSON {
		t.Errorf("metadata body=%q, want %q", string(body), metadataJSON)
	}

	// 2. Reads through the underlying httpstore.Backend confirm
	//    the artifactory Layout's storage-API listing wires up.
	mods, err := hs.Store().ListModules(context.Background())
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	gotMods := strings.Join(mods, ",")
	if !strings.Contains(gotMods, "rules_go") || !strings.Contains(gotMods, "rules_python") {
		t.Errorf("ListModules = %v, want both rules_go + rules_python", mods)
	}

	// 3. ListVersions for one module via the artifactory storage API.
	versions, err := hs.Store().ListVersions(context.Background(), "rules_go")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != "0.50.0" {
		t.Errorf("ListVersions = %v, want [0.50.0]", versions)
	}

	// 4. Unknown module → ErrNotFound through the translation layer.
	_, err = hs.GetMetadata(context.Background(), "no-such-module")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("GetMetadata(unknown) = %v, want backend.ErrNotFound", err)
	}
}

// Compile-time guard: package keeps importing httpstore so
// future refactors notice if the link breaks.
var _ = httpstore.Anonymous{}
