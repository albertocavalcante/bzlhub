package backend_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	bundle "github.com/albertocavalcante/go-bcr-bundle"
	"github.com/albertocavalcante/bzlhub/internal/backend"
)

// bundleFixture builds a small BCR-shape tree under t.TempDir(),
// writes it to a tar.gz bundle, opens that bundle, and returns
// the resulting *backend.Bundle adapter. Tests focus on per-method
// contract without re-declaring the bundle-building boilerplate.
type bundleFixture struct {
	adapter *backend.Bundle
	bundle  *bundle.Bundle
}

func newBundleFixture(t *testing.T) *bundleFixture {
	t.Helper()

	// Build a BCR-shape tree.
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bazel_registry.json", `{"mirrors":[]}`)
	write("modules/bazel_skylib/metadata.json", `{"versions":["1.7.0"]}`)
	write("modules/bazel_skylib/1.7.0/source.json", `{"url":"https://example.test"}`)
	write("modules/bazel_skylib/1.7.0/MODULE.bazel", `module(name="bazel_skylib")`)
	write("modules/bazel_skylib/1.7.0/patches/fix.patch", `--- a\n+++ b`)
	write("modules/bazel_skylib/1.7.0/overlay/BUILD.bazel", `cc_binary()`)
	write("blobs/sha256-aaa", "blob-aaa-content")

	src, err := bundle.NewFilesystemSource(root)
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	// Write a bundle into a temp file (Open consumes the whole
	// reader anyway, but a real file is closer to canopy's flow).
	bundlePath := filepath.Join(t.TempDir(), "bundle.tar.gz")
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.WriteBundle(context.Background(), f, src, bundle.WriteOptions{
		CreatedBy: "canopy bundle adapter test",
	}); err != nil {
		_ = f.Close()
		t.Fatalf("WriteBundle: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Open the bundle.
	rd, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	b, err := bundle.Open(rd)
	if err != nil {
		t.Fatalf("bundle.Open: %v", err)
	}

	adapter := backend.NewBundle(b)
	t.Cleanup(func() { _ = adapter.Close() })

	return &bundleFixture{adapter: adapter, bundle: b}
}

// =================================================================
// Read methods — round-trip + ErrNotFound translation
// =================================================================

func TestBundle_GetBazelRegistryJSON(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetBazelRegistryJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := readAllClose(t, rc)
	if got != `{"mirrors":[]}` {
		t.Errorf("body = %q; want %q", got, `{"mirrors":[]}`)
	}
}

func TestBundle_GetMetadata(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetMetadata(context.Background(), "bazel_skylib")
	if err != nil {
		t.Fatal(err)
	}
	got := readAllClose(t, rc)
	if got != `{"versions":["1.7.0"]}` {
		t.Errorf("body = %q", got)
	}
}

func TestBundle_GetMetadata_MissingModuleIsErrNotFound(t *testing.T) {
	f := newBundleFixture(t)
	_, err := f.adapter.GetMetadata(context.Background(), "nonexistent")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestBundle_GetSourceJSON(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetSourceJSON(context.Background(), "bazel_skylib", "1.7.0")
	if err != nil {
		t.Fatal(err)
	}
	got := readAllClose(t, rc)
	if got != `{"url":"https://example.test"}` {
		t.Errorf("body = %q", got)
	}
}

func TestBundle_GetSourceJSON_MissingVersionIsErrNotFound(t *testing.T) {
	f := newBundleFixture(t)
	_, err := f.adapter.GetSourceJSON(context.Background(), "bazel_skylib", "9.9.9")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestBundle_GetModuleBazel(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetModuleBazel(context.Background(), "bazel_skylib", "1.7.0")
	if err != nil {
		t.Fatal(err)
	}
	got := readAllClose(t, rc)
	if got != `module(name="bazel_skylib")` {
		t.Errorf("body = %q", got)
	}
}

func TestBundle_GetPatch(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetPatch(context.Background(), "bazel_skylib", "1.7.0", "fix.patch")
	if err != nil {
		t.Fatal(err)
	}
	got := readAllClose(t, rc)
	if got != `--- a\n+++ b` {
		t.Errorf("body = %q", got)
	}
}

func TestBundle_GetPatch_MissingIsErrNotFound(t *testing.T) {
	f := newBundleFixture(t)
	_, err := f.adapter.GetPatch(context.Background(), "bazel_skylib", "1.7.0", "nope.patch")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestBundle_GetOverlay(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetOverlay(context.Background(), "bazel_skylib", "1.7.0", "BUILD.bazel")
	if err != nil {
		t.Fatal(err)
	}
	got := readAllClose(t, rc)
	if got != `cc_binary()` {
		t.Errorf("body = %q", got)
	}
}

func TestBundle_GetBlob_StreamsContent(t *testing.T) {
	f := newBundleFixture(t)
	rc, err := f.adapter.GetBlob(context.Background(), "sha256-aaa")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte("blob-aaa-content")) {
		t.Errorf("blob body = %q", body)
	}
}

func TestBundle_GetBlob_MissingIsErrNotFound(t *testing.T) {
	f := newBundleFixture(t)
	_, err := f.adapter.GetBlob(context.Background(), "sha256-missing")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("got %v; want ErrNotFound", err)
	}
}

func TestBundle_GetBlob_PathSeparatorRejected(t *testing.T) {
	// bundle.ReadBlob's defensive check rejects keys containing /
	// or \. Should surface as ErrNotFound through the adapter's
	// translation.
	f := newBundleFixture(t)
	_, err := f.adapter.GetBlob(context.Background(), "bad/key")
	if !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("got %v; want ErrNotFound (path-separator defense)", err)
	}
}

// =================================================================
// Adapter lifecycle
// =================================================================

func TestBundle_InnerExposesUnderlying(t *testing.T) {
	f := newBundleFixture(t)
	if got := f.adapter.Inner(); got != f.bundle {
		t.Errorf("Inner() returned a different *bundle.Bundle")
	}
}

func TestBundle_CloseIdempotent(t *testing.T) {
	f := newBundleFixture(t)
	if err := f.adapter.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := f.adapter.Close(); err != nil {
		t.Errorf("second Close: got %v; want nil (idempotent)", err)
	}
}
