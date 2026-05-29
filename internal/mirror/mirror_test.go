package mirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestEnsureRegistryJSON(t *testing.T) {
	root := t.TempDir()
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.EnsureRegistryJSON(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "bazel_registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("{}")) {
		t.Fatalf("registry json: %q", b)
	}
}

func TestBlobWriterContentAddressed(t *testing.T) {
	root := t.TempDir()
	w, _ := New(root)
	sink, err := w.BlobWriter("https://example.com/foo-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("imagine a tar.gz here")
	if _, err := io.Copy(sink, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	p, sri, n, err := sink.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Final path must be blobs/<sha256-hex>, not URL-basename.
	wantHash := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(wantHash[:])
	wantPath := filepath.Join(root, "blobs", wantHex)
	if p != wantPath {
		t.Fatalf("path %q != %q (content address)", p, wantPath)
	}
	if n != int64(len(payload)) {
		t.Fatalf("bytes %d != %d", n, len(payload))
	}
	wantSRI := "sha256-" + base64.StdEncoding.EncodeToString(wantHash[:])
	if sri != wantSRI {
		t.Fatalf("sri %q != %q", sri, wantSRI)
	}
	on, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(on, payload) {
		t.Fatalf("on-disk bytes differ")
	}
}

// TestBlobWriterDedup: two writes of the same bytes — even via different
// upstream URLs (which previously caused basename collisions) — must
// land at the same content-addressed path, and the second write must
// not error.
func TestBlobWriterDedup(t *testing.T) {
	root := t.TempDir()
	w, _ := New(root)
	payload := []byte("two modules share me")

	sink1, _ := w.BlobWriter("https://example.com/v1.0.0.tar.gz")
	if _, err := io.Copy(sink1, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	p1, sri1, _, err := sink1.Close()
	if err != nil {
		t.Fatal(err)
	}

	sink2, _ := w.BlobWriter("https://other.com/different-name-same-bytes.tar.gz")
	if _, err := io.Copy(sink2, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	p2, sri2, _, err := sink2.Close()
	if err != nil {
		t.Fatal(err)
	}

	if p1 != p2 {
		t.Fatalf("dedup failed: %q vs %q", p1, p2)
	}
	if sri1 != sri2 {
		t.Fatalf("sri mismatch on same content: %q vs %q", sri1, sri2)
	}
	// Only one blob file on disk.
	entries, _ := os.ReadDir(filepath.Join(root, "blobs"))
	count := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".tmp-blob") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 blob on disk, got %d", count)
	}
}

// TestBlobWriterNoCollisionDifferentBytes: two writes with different bytes
// land at distinct content addresses (regression for the collision bug
// caught in the bazel-tools closure ingest).
func TestBlobWriterNoCollisionDifferentBytes(t *testing.T) {
	root := t.TempDir()
	w, _ := New(root)
	// Both URLs SHARE THE SAME BASENAME, but bytes differ.
	sink1, _ := w.BlobWriter("https://example.com/v1.14.0.tar.gz")
	_, _ = io.Copy(sink1, bytes.NewReader([]byte("module-A bytes")))
	p1, _, _, _ := sink1.Close()

	sink2, _ := w.BlobWriter("https://other.com/v1.14.0.tar.gz")
	_, _ = io.Copy(sink2, bytes.NewReader([]byte("module-B bytes — different")))
	p2, _, _, _ := sink2.Close()

	if p1 == p2 {
		t.Fatalf("expected distinct content-addressed paths, both %q", p1)
	}
	for _, p := range []string{p1, p2} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("blob missing: %s (%v)", p, err)
		}
	}
}

func TestMergeMetadataAppendsAndSorts(t *testing.T) {
	root := t.TempDir()
	w, _ := New(root)
	for _, v := range []string{"1.0.0", "1.0.1", "0.9.0", "1.0.0"} {
		if err := w.MergeMetadata("foo", v); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "modules", "foo", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var versions []string
	_ = json.Unmarshal(m["versions"], &versions)
	want := []string{"0.9.0", "1.0.0", "1.0.1"}
	if len(versions) != len(want) {
		t.Fatalf("len %d != %d  (%v)", len(versions), len(want), versions)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("versions[%d] = %q want %q (full: %v)", i, versions[i], want[i], versions)
		}
	}
}

// TestMergeMetadataConcurrent stresses the per-module lock by spawning N
// goroutines that all call MergeMetadata for the same module with
// distinct versions. Without the lock, the read-modify-write would lose
// versions due to interleaving. With the lock, every version must show
// up in the final metadata.json.
func TestMergeMetadataConcurrent(t *testing.T) {
	root := t.TempDir()
	w, _ := New(root)
	const N = 50
	versions := make([]string, N)
	for i := range versions {
		versions[i] = "1.0." + strconv.Itoa(i)
	}
	var wg sync.WaitGroup
	for _, v := range versions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.MergeMetadata("foo", v); err != nil {
				t.Errorf("MergeMetadata: %v", err)
			}
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(filepath.Join(root, "modules", "foo", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	var got []string
	_ = json.Unmarshal(m["versions"], &got)
	if len(got) != N {
		t.Fatalf("got %d versions, want %d (lock dropped %d)", len(got), N, N-len(got))
	}
}

func TestMergeMetadataPreservesUnknownFields(t *testing.T) {
	root := t.TempDir()
	w, _ := New(root)
	preexisting := []byte(`{"versions":["1.0.0"],"homepage":"https://example.com","maintainers":[{"name":"Alice"}],"custom_field":42}`)
	dir := filepath.Join(root, "modules", "foo")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "metadata.json"), preexisting, 0o644)

	if err := w.MergeMetadata("foo", "1.0.1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "metadata.json"))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if _, ok := m["custom_field"]; !ok {
		t.Fatalf("unknown field dropped: %s", b)
	}
	if _, ok := m["homepage"]; !ok {
		t.Fatalf("homepage dropped: %s", b)
	}
}
