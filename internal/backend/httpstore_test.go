package backend_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpstore "github.com/albertocavalcante/go-bcr-httpstore"
	"github.com/albertocavalcante/bzlhub/internal/backend"
)

// httpFixture spins up a httptest.Server serving a small BCR-shape
// tree from an in-memory path → response table. Each test focuses
// on one slice of the contract without re-declaring the handler.
type httpFixture struct {
	srv  *httptest.Server
	resp map[string]string
}

func newHTTPFixture(t *testing.T, resp map[string]string) *httpFixture {
	t.Helper()
	f := &httpFixture{resp: resp}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.resp[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *httpFixture) adapter(t *testing.T) *backend.HTTPStore {
	t.Helper()
	store, err := httpstore.New(httpstore.NewOptions{
		BaseURL: f.srv.URL,
		Auth:    httpstore.Anonymous{},
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		Layout:  httpstore.HTMLAutoindex{}, // adapter tests don't list; pick any valid Layout.
	})
	if err != nil {
		t.Fatalf("httpstore.New: %v", err)
	}
	return backend.NewHTTPStore(store)
}

// readAllClose reads + closes; tests reuse to avoid forgetting to
// Close which would leak the underlying response body.
func readAllClose(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(b)
}

// =================================================================
// Each Backend method, success path
// =================================================================

func TestHTTPStore_GetBazelRegistryJSON(t *testing.T) {
	want := `{"mirrors":["https://mirror.example/"]}`
	f := newHTTPFixture(t, map[string]string{
		"/bazel_registry.json": want,
	})
	rc, err := f.adapter(t).GetBazelRegistryJSON(context.Background())
	if err != nil {
		t.Fatalf("GetBazelRegistryJSON: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}
}

func TestHTTPStore_GetMetadata(t *testing.T) {
	want := `{"homepage":"https://example.test","versions":["1.0.0"]}`
	f := newHTTPFixture(t, map[string]string{
		"/modules/test_leaf/metadata.json": want,
	})
	rc, err := f.adapter(t).GetMetadata(context.Background(), "test_leaf")
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}
}

func TestHTTPStore_GetModuleBazel(t *testing.T) {
	want := `module(name = "test_leaf", version = "1.0.0")`
	f := newHTTPFixture(t, map[string]string{
		"/modules/test_leaf/1.0.0/MODULE.bazel": want,
	})
	rc, err := f.adapter(t).GetModuleBazel(context.Background(), "test_leaf", "1.0.0")
	if err != nil {
		t.Fatalf("GetModuleBazel: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}
}

func TestHTTPStore_GetSourceJSON(t *testing.T) {
	want := `{"url":"https://example.test/x.tar.gz","integrity":"sha256-..."}`
	f := newHTTPFixture(t, map[string]string{
		"/modules/test_leaf/1.0.0/source.json": want,
	})
	rc, err := f.adapter(t).GetSourceJSON(context.Background(), "test_leaf", "1.0.0")
	if err != nil {
		t.Fatalf("GetSourceJSON: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}
}

func TestHTTPStore_GetPatch(t *testing.T) {
	want := "--- a/foo\n+++ b/foo\n@@ -1 +1 @@\n-old\n+new\n"
	f := newHTTPFixture(t, map[string]string{
		"/modules/test_leaf/1.0.0/patches/fix.patch": want,
	})
	rc, err := f.adapter(t).GetPatch(context.Background(), "test_leaf", "1.0.0", "fix.patch")
	if err != nil {
		t.Fatalf("GetPatch: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}
}

func TestHTTPStore_GetOverlay(t *testing.T) {
	want := "// regenerated WORKSPACE.bzlmod\n"
	f := newHTTPFixture(t, map[string]string{
		"/modules/test_leaf/1.0.0/overlay/WORKSPACE.bzlmod": want,
	})
	rc, err := f.adapter(t).GetOverlay(context.Background(), "test_leaf", "1.0.0", "WORKSPACE.bzlmod")
	if err != nil {
		t.Fatalf("GetOverlay: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body = %q; want %q", got, want)
	}
}

func TestHTTPStore_GetBlob_Streams(t *testing.T) {
	// Verify the streaming contract — the adapter passes through
	// the library's io.ReadCloser without buffering.
	want := strings.Repeat("blob-data-", 1024) // ~10 KiB
	f := newHTTPFixture(t, map[string]string{
		"/blobs/abc123": want,
	})
	rc, err := f.adapter(t).GetBlob(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if got := readAllClose(t, rc); got != want {
		t.Errorf("body mismatch (head %q)", got[:64])
	}
}

// =================================================================
// 404 → backend.ErrNotFound for every method
// =================================================================

func TestHTTPStore_404MapsToErrNotFound(t *testing.T) {
	// Empty fixture — every read 404s. Each Backend method must
	// translate its specific httpstore sentinel to the canopy-
	// internal ErrNotFound so handlers render uniform 404s.
	f := newHTTPFixture(t, map[string]string{})
	ctx := context.Background()
	a := f.adapter(t)

	cases := []struct {
		name string
		call func() (io.ReadCloser, error)
	}{
		{"GetBazelRegistryJSON", func() (io.ReadCloser, error) { return a.GetBazelRegistryJSON(ctx) }},
		{"GetMetadata", func() (io.ReadCloser, error) { return a.GetMetadata(ctx, "x") }},
		{"GetModuleBazel", func() (io.ReadCloser, error) { return a.GetModuleBazel(ctx, "x", "1.0") }},
		{"GetSourceJSON", func() (io.ReadCloser, error) { return a.GetSourceJSON(ctx, "x", "1.0") }},
		{"GetPatch", func() (io.ReadCloser, error) { return a.GetPatch(ctx, "x", "1.0", "p") }},
		{"GetOverlay", func() (io.ReadCloser, error) { return a.GetOverlay(ctx, "x", "1.0", "f") }},
		{"GetBlob", func() (io.ReadCloser, error) { return a.GetBlob(ctx, "abc") }},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			_, err := c.call()
			if !errors.Is(err, backend.ErrNotFound) {
				t.Errorf("got %v; want backend.ErrNotFound", err)
			}
		})
	}
}

// =================================================================
// 5xx surfaces as not-ErrNotFound (so handlers can 502/503)
// =================================================================

func TestHTTPStore_5xxIsNotErrNotFound(t *testing.T) {
	// A transient upstream failure (502) is NOT a Bazel-side 404;
	// the adapter must NOT translate it to ErrNotFound or the
	// client would get a wrong "module deleted" signal during
	// what's actually an outage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	store, err := httpstore.New(httpstore.NewOptions{
		BaseURL: srv.URL,
		Auth:    httpstore.Anonymous{},
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		Layout:  httpstore.HTMLAutoindex{},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := backend.NewHTTPStore(store)
	_, err = adapter.GetMetadata(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error from 502; got nil")
	}
	if errors.Is(err, backend.ErrNotFound) {
		t.Errorf("502 should NOT translate to ErrNotFound; got %v", err)
	}
}

// =================================================================
// Store() escape hatch
// =================================================================

func TestHTTPStore_StoreExposesLibrary(t *testing.T) {
	// The Store() accessor lets callers reach the underlying
	// *httpstore.Backend for AuthName diagnostics, BaseURL
	// introspection, etc., without an extra config plumb.
	f := newHTTPFixture(t, nil)
	a := f.adapter(t)
	if a.Store() == nil {
		t.Errorf("Store() returned nil")
	}
	if got := a.Store().AuthName(); got != "anonymous" {
		t.Errorf("AuthName = %q; want anonymous", got)
	}
}
