package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFilesystemPublisher_RoundTrip(t *testing.T) {
	root := t.TempDir()
	pub, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}

	// bazel_registry.json is created on construction.
	if _, err := os.Stat(filepath.Join(root, "bazel_registry.json")); err != nil {
		t.Fatalf("bazel_registry.json missing after NewFilesystem: %v", err)
	}

	// Stage a blob through the streaming sink.
	tarball := []byte("imagine a tar.gz here")
	want := sri256(tarball)

	ctx := context.Background()
	sink, err := pub.BeginBlob(ctx, "https://example.com/foo-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(sink, bytes.NewReader(tarball)); err != nil {
		t.Fatal(err)
	}
	ref, err := sink.Close()
	if err != nil {
		t.Fatal(err)
	}
	if ref.Integrity != want {
		t.Fatalf("blob integrity: got %q, want %q", ref.Integrity, want)
	}
	if ref.Bytes != int64(len(tarball)) {
		t.Fatalf("blob bytes: got %d, want %d", ref.Bytes, len(tarball))
	}
	// Content-addressed file lives at <root>/blobs/<sha256-hex>.
	hex := sha256hex(tarball)
	if got := filepath.Base(ref.Key); got != hex {
		t.Fatalf("blob basename: got %q, want %q", got, hex)
	}
	if _, err := os.Stat(ref.Key); err != nil {
		t.Fatalf("blob file missing: %v", err)
	}

	// Publish source.json + MODULE.bazel.
	sourceJSON := []byte(`{"url":"https://example.com/foo-1.0.0.tar.gz","integrity":"` + ref.Integrity + `"}`)
	moduleBazel := []byte("module(name = \"foo\", version = \"1.0.0\")\n")

	r, err := pub.Publish(ctx, PublishRequest{
		Module:      "foo",
		Version:     "1.0.0",
		SourceJSON:  sourceJSON,
		ModuleBazel: moduleBazel,
		Blob:        ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Strategy != "filesystem" {
		t.Fatalf("strategy: %q", r.Strategy)
	}
	if r.PublishedAt.IsZero() {
		t.Fatalf("published_at unset")
	}

	// On-disk layout matches what mirror.Writer would produce directly.
	srcOnDisk := filepath.Join(root, "modules", "foo", "1.0.0", "source.json")
	if got, _ := os.ReadFile(srcOnDisk); !bytes.Equal(got, sourceJSON) {
		t.Fatalf("source.json round-trip: got %q", got)
	}
	modOnDisk := filepath.Join(root, "modules", "foo", "1.0.0", "MODULE.bazel")
	if got, _ := os.ReadFile(modOnDisk); !bytes.Equal(got, moduleBazel) {
		t.Fatalf("MODULE.bazel round-trip: got %q", got)
	}
	// metadata.json contains the version.
	if !metadataHasVersion(t, root, "foo", "1.0.0") {
		t.Fatalf("metadata.json missing version 1.0.0")
	}
}

func TestFilesystemPublisher_DoublePublishAppendsVersions(t *testing.T) {
	root := t.TempDir()
	pub, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, v := range []string{"1.0.0", "1.1.0", "1.0.1"} {
		if _, err := pub.Publish(ctx, PublishRequest{
			Module:     "foo",
			Version:    v,
			SourceJSON: []byte(`{}`),
			Blob:       BlobRef{Integrity: "sha256-aaaa"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "modules", "foo", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	// MergeMetadata sorts lexicographically; canopy uses 4-component
	// variants where Bazel's numeric comparator wins at resolve time.
	want := []string{"1.0.0", "1.0.1", "1.1.0"}
	if !slices.Equal(meta.Versions, want) {
		t.Fatalf("versions: got %v, want %v", meta.Versions, want)
	}
}

func TestFilesystemPublisher_MissingFields(t *testing.T) {
	pub, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := PublishRequest{
		Module:     "foo",
		Version:    "1.0.0",
		SourceJSON: []byte(`{}`),
		Blob:       BlobRef{Integrity: "sha256-aa"},
	}
	cases := []struct {
		name string
		mut  func(*PublishRequest)
		want string
	}{
		{"no module", func(r *PublishRequest) { r.Module = "" }, "module"},
		{"no version", func(r *PublishRequest) { r.Version = "" }, "version"},
		{"no source.json", func(r *PublishRequest) { r.SourceJSON = nil }, "source.json"},
		{"no blob integrity", func(r *PublishRequest) { r.Blob = BlobRef{} }, "blob.integrity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mut(&req)
			_, err := pub.Publish(ctx, req)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !errors.Is(err, ErrMissingRequiredField) {
				t.Fatalf("not ErrMissingRequiredField: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error mentions wrong field: %v", err)
			}
		})
	}
}

func TestFilesystemPublisher_UpstreamMetadataLift(t *testing.T) {
	root := t.TempDir()
	pub, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	upstream := []byte(`{
		"homepage": "https://example.com/foo",
		"maintainers": [{"name": "Alice", "email": "alice@example.com"}],
		"repository": ["github:example/foo"],
		"yanked_versions": {"0.9.0": "broken"},
		"versions": ["should-be-ignored"]
	}`)
	if _, err := pub.Publish(context.Background(), PublishRequest{
		Module:           "foo",
		Version:          "1.0.0",
		SourceJSON:       []byte(`{}`),
		UpstreamMetadata: upstream,
		Blob:             BlobRef{Integrity: "sha256-aa"},
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "modules", "foo", "metadata.json"))
	var meta map[string]any
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["homepage"] != "https://example.com/foo" {
		t.Fatalf("homepage not lifted: %v", meta["homepage"])
	}
	// Local versions field is authoritative; upstream's was discarded.
	vs, _ := meta["versions"].([]any)
	if len(vs) != 1 || vs[0] != "1.0.0" {
		t.Fatalf("versions: %v", meta["versions"])
	}
}

func TestFilesystemPublisher_BlobSinkAbort(t *testing.T) {
	root := t.TempDir()
	pub, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := pub.BeginBlob(context.Background(), "https://example.com/a.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	sink.Abort()
	// mirror.BlobSink.Abort removes the tmp file outright, so the
	// blobs/ directory must be empty afterward — no content-addressed
	// blob and no orphan tmp file.
	entries, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("blobs/ not empty after abort: %v", names)
	}
}

// helpers

func sri256(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func metadataHasVersion(t *testing.T, root, module, version string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "modules", module, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	return slices.Contains(meta.Versions, version)
}
