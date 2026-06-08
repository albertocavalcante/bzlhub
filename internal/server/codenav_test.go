package server_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	scip "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func minimalReport(name, version string) *report.ModuleReport {
	return &report.ModuleReport{Name: name, Version: version}
}

// scipBlobForTest fabricates a tiny SCIP index with one document and
// one definition occurrence, enough for understory.ui to expose
// /api/files and /api/source against it.
func scipBlobForTest(t *testing.T) []byte {
	t.Helper()
	idx := &scip.Index{
		Metadata: &scip.Metadata{Version: 0},
		Documents: []*scip.Document{{
			RelativePath: "MODULE.bazel",
			Occurrences: []*scip.Occurrence{{
				Symbol:      "test sym",
				Range:       []int32{0, 0, 0, 1},
				SymbolRoles: int32(scip.SymbolRole_Definition),
			}},
		}},
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal scip: %v", err)
	}
	return b
}

// buildTinyTarGz builds a single-prefix tarball.
func buildTinyTarGz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name:     prefix + "/",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		full := prefix + "/" + name
		if err := tw.WriteHeader(&tar.Header{
			Name:     full,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes()
}

// seedCodeNavFixture creates a complete (mirror, sources-cache, store)
// triple so an end-to-end server test can hit /modules/<m>/<v>/code-nav.
func seedCodeNavFixture(t *testing.T, module, version string) (mirrorRoot, sourcesRoot string, st *store.Store) {
	t.Helper()
	mirrorRoot = t.TempDir()
	sourcesRoot = t.TempDir()

	// Tarball + content-addressed blob.
	tarBytes := buildTinyTarGz(t, module+"-"+version, map[string]string{
		"MODULE.bazel": "module(name = \"" + module + "\")\n",
	})
	sum := sha256.Sum256(tarBytes)
	hexName := hex.EncodeToString(sum[:])
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	blobs := filepath.Join(mirrorRoot, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// modules/<m>/<v>/source.json (mirror layout).
	modDir := filepath.Join(mirrorRoot, "modules", module, version)
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := map[string]any{
		"url":          "https://example.invalid/x.tar.gz",
		"integrity":    integrity,
		"strip_prefix": module + "-" + version,
	}
	srcBytes, _ := json.Marshal(src)
	if err := os.WriteFile(filepath.Join(modDir, "source.json"), srcBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Store + SCIP blob.
	s, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// WriteScipBlob requires a versions row. Write a minimal report.
	report := minimalReport(module, version)
	if err := s.WriteReport(context.Background(), report); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := s.WriteScipBlob(context.Background(), module, version, scipBlobForTest(t)); err != nil {
		t.Fatalf("WriteScipBlob: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return mirrorRoot, sourcesRoot, s
}

// TestCodeNavMount verifies the handler is registered at
// /modules/<m>/<v>/code-nav/* and forwards to understory.ui's
// /api/files (a route understory.ui exposes unconditionally).
func TestCodeNavMount(t *testing.T) {
	mirrorRoot, sourcesRoot, s := seedCodeNavFixture(t, "foo", "1.0")
	cs := bzlhub.New(s)
	cs.MirrorRoot = mirrorRoot

	ts := httptest.NewServer(server.NewWithOptions(nil, cs, nil, server.Options{
		MirrorRoot:    mirrorRoot,
		SourcesCacheDir: sourcesRoot,
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/modules/foo/1.0/code-nav/api/files")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d body=%s", res.StatusCode, body)
	}
	// Body shape: understory.ui returns JSON. Just assert it's parseable
	// and the document we wrote is listed.
	var got struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal /api/files: %v body=%s", err, body)
	}
	if !slices.Contains(got.Files, "MODULE.bazel") {
		t.Errorf("/api/files did not include MODULE.bazel: %v", got.Files)
	}
}

// TestCodeNavSPAFallbackRewritesAssets — when a deep code-nav URL
// falls back to understory's index.html, the mount prefix must be
// substituted into the embedded `/_app/` references and the inline
// `base: ""` placeholder so the browser fetches assets through the
// canopy mount instead of the bare origin (where canopy's own bundle
// lives). This is the e2e contract between canopy's codenav handler
// (sets X-Forwarded-Prefix) and understory.ui (rewrites the body).
func TestCodeNavSPAFallbackRewritesAssets(t *testing.T) {
	mirrorRoot, sourcesRoot, s := seedCodeNavFixture(t, "foo", "1.0")
	cs := bzlhub.New(s)
	cs.MirrorRoot = mirrorRoot

	ts := httptest.NewServer(server.NewWithOptions(nil, cs, nil, server.Options{
		MirrorRoot:      mirrorRoot,
		SourcesCacheDir: sourcesRoot,
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/modules/foo/1.0/code-nav/file/MODULE.bazel")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	s2 := string(body)
	wantPrefix := "/modules/foo/1.0/code-nav"
	if bytes.Contains(body, []byte(`href="/_app/`)) {
		t.Errorf("SPA fallback still has absolute /_app/ refs (rewrite did not run):\n%s", s2)
	}
	if !bytes.Contains(body, []byte(wantPrefix+"/_app/")) {
		t.Errorf("SPA fallback missing rewritten %s/_app/ refs", wantPrefix)
	}
}

// TestCodeNavNotIndexedReturnsFriendlyHTML — when canopy has a working
// codenav resolver but the requested coordinate has no SCIP blob (e.g.
// rules_kotlin's MODULE.bazel pins rules_java@7.2.0 but we only have
// 8.6.1 indexed), the handler must:
//   - return 404 (semantic: "this specific coordinate doesn't exist
//     in our index"), NOT 503 (which means "service can't help")
//   - serve HTML the browser can render, NOT JSON (the user followed
//     a deep link from a browser; a JSON body shows as raw text)
//   - link to /modules/<m> so the user can see indexed versions
func TestCodeNavNotIndexedReturnsFriendlyHTML(t *testing.T) {
	// Seed a different (module, version) so the resolver is "wired" but
	// the requested coordinate is missing.
	mirrorRoot, sourcesRoot, s := seedCodeNavFixture(t, "indexed_mod", "1.0")
	cs := bzlhub.New(s)
	cs.MirrorRoot = mirrorRoot

	ts := httptest.NewServer(server.NewWithOptions(nil, cs, nil, server.Options{
		MirrorRoot:      mirrorRoot,
		SourcesCacheDir: sourcesRoot,
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/modules/missing/9.9.9/code-nav/file/foo.bzl")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d body=%s; want 404", res.StatusCode, body)
	}
	ct := res.Header.Get("Content-Type")
	if !bytes.Contains([]byte(ct), []byte("text/html")) {
		t.Errorf("Content-Type=%q; want text/html", ct)
	}
	s2 := string(body)
	if !bytes.Contains(body, []byte("missing")) || !bytes.Contains(body, []byte("9.9.9")) {
		t.Errorf("body does not name the requested coordinate:\n%s", s2)
	}
	if !bytes.Contains(body, []byte(`href="/modules/missing"`)) {
		t.Errorf("body missing link to /modules/missing for version discovery:\n%s", s2)
	}
}

// TestCodeNavUnknownCoordinate — an unindexed coordinate now routes
// to the friendly 404 HTML page (see TestCodeNavNotIndexedReturnsFriendlyHTML)
// rather than the old 503-JSON shape. The earlier 503 contract was
// inappropriate: 503 means "this nav surface can't serve right now,"
// but a missing coordinate is a stable "doesn't exist" answer that
// won't change without an explicit ingest action. This test pins the
// new contract to keep it from regressing.
func TestCodeNavUnknownCoordinate(t *testing.T) {
	mirrorRoot, sourcesRoot, s := seedCodeNavFixture(t, "foo", "1.0")
	cs := bzlhub.New(s)
	cs.MirrorRoot = mirrorRoot

	ts := httptest.NewServer(server.NewWithOptions(nil, cs, nil, server.Options{
		MirrorRoot:    mirrorRoot,
		SourcesCacheDir: sourcesRoot,
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/modules/unknown/9.9/code-nav/api/files")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", res.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("Not indexed")) {
		t.Errorf("body missing friendly 'Not indexed' heading: %s", body)
	}
}

// TestCodeNavDisabledWhenNoResolver — when the server is built without
// SourcesCacheDir/MirrorRoot (e.g., --db only), the route registers but
// returns 503 instead of crashing. Required so bzlhub serve with a
// db-only setup doesn't go nil-deref on the route.
func TestCodeNavDisabledWhenNoResolver(t *testing.T) {
	s, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cs := bzlhub.New(s)

	ts := httptest.NewServer(server.NewWithOptions(nil, cs, nil, server.Options{}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/modules/foo/1.0/code-nav/api/files")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", res.StatusCode)
	}
}
