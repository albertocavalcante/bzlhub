package codenav

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	scip "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// fakeBlobReader implements BlobReader against a static map.
type fakeBlobReader struct {
	blobs map[string][]byte
	calls atomic.Int32
}

func (f *fakeBlobReader) GetScipBlob(_ context.Context, module, version string) ([]byte, error) {
	f.calls.Add(1)
	b, ok := f.blobs[module+"@"+version]
	if !ok {
		return nil, errFakeNotFound{module: module, version: version}
	}
	return b, nil
}

type errFakeNotFound struct{ module, version string }

func (e errFakeNotFound) Error() string { return "scip blob " + e.module + "@" + e.version + " not found" }

// buildTinySCIP marshals a one-document SCIP index — enough for
// understory.OpenBytes to succeed and Index.Files() to return at
// least one entry.
func buildTinySCIP(t *testing.T) []byte {
	t.Helper()
	idx := &scip.Index{
		Metadata: &scip.Metadata{Version: 0},
		Documents: []*scip.Document{{
			RelativePath: "MODULE.bazel",
			Occurrences: []*scip.Occurrence{{
				Symbol:       "test sym",
				Range:        []int32{0, 0, 0, 1},
				SymbolRoles:  int32(scip.SymbolRole_Definition),
			}},
		}},
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal scip: %v", err)
	}
	return b
}

// buildMirrorTree creates modules/<m>/<v>/source.json + blobs/<hex>
// under a fresh temp dir. Returns the mirror root.
func buildMirrorTree(t *testing.T, module, version string) string {
	t.Helper()
	root := t.TempDir()
	// Tarball with one entry under "<m>-<v>/".
	tarBytes := buildFixtureTarGz(t, module+"-"+version, map[string]string{
		"MODULE.bazel": "module(name = \"" + module + "\")\n",
		"src/a.bzl":    "a = 1\n",
	})
	sum := sha256.Sum256(tarBytes)
	hexName := hex.EncodeToString(sum[:])
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	modDir := filepath.Join(root, "modules", module, version)
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := map[string]any{
		"url":          "https://example.invalid/x.tar.gz",
		"integrity":    integrity,
		"strip_prefix": module + "-" + version,
	}
	b, _ := json.Marshal(src)
	if err := os.WriteFile(filepath.Join(modDir, "source.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestResolver_Resolve loads the SCIP index, unpacks the tarball, and
// returns a usable (Index, *os.Root) pair for the requested coordinate.
func TestResolver_Resolve(t *testing.T) {
	mirror := buildMirrorTree(t, "foo", "1.0")
	br := &fakeBlobReader{blobs: map[string][]byte{
		"foo@1.0": buildTinySCIP(t),
	}}
	cache := t.TempDir()
	r := NewResolver(br, mirror, cache)

	idx, root, err := r.Resolve(context.Background(), "foo", "1.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if idx == nil {
		t.Fatal("nil index")
	}
	if root == nil {
		t.Fatal("nil source root")
	}
	t.Cleanup(func() { _ = root.Close() })

	// File present in the extracted source tree under the cache root.
	if _, err := root.Stat("MODULE.bazel"); err != nil {
		t.Fatalf("source root missing MODULE.bazel: %v", err)
	}
	// SCIP index sanity — must report the one document we wrote.
	files := idx.Files()
	if len(files) != 1 || files[0] != "MODULE.bazel" {
		t.Fatalf("idx.Files = %v, want [MODULE.bazel]", files)
	}
}

// TestResolver_Cached returns the SAME (Index, root) handles on repeat
// Resolve calls + does not re-fetch the SCIP blob from the store.
func TestResolver_Cached(t *testing.T) {
	mirror := buildMirrorTree(t, "bar", "2.0")
	br := &fakeBlobReader{blobs: map[string][]byte{
		"bar@2.0": buildTinySCIP(t),
	}}
	r := NewResolver(br, mirror, t.TempDir())

	idx1, root1, err := r.Resolve(context.Background(), "bar", "2.0")
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	t.Cleanup(func() { _ = root1.Close() })

	idx2, root2, err := r.Resolve(context.Background(), "bar", "2.0")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if idx1 != idx2 {
		t.Errorf("index not cached: %p vs %p", idx1, idx2)
	}
	if root1 != root2 {
		t.Errorf("source root not cached: %p vs %p", root1, root2)
	}
	if got := br.calls.Load(); got != 1 {
		t.Errorf("blob reader called %d times, want 1", got)
	}
}

// TestResolver_Concurrent eight goroutines race on the same coordinate;
// only one unpack must happen (verified via call count on the blob
// reader, which the once.Do path inside the resolver only invokes once).
func TestResolver_Concurrent(t *testing.T) {
	mirror := buildMirrorTree(t, "race", "9.0")
	br := &fakeBlobReader{blobs: map[string][]byte{
		"race@9.0": buildTinySCIP(t),
	}}
	r := NewResolver(br, mirror, t.TempDir())

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _, err := r.Resolve(context.Background(), "race", "9.0")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Resolve under race: %v", err)
		}
	}
	if got := br.calls.Load(); got != 1 {
		t.Errorf("blob reader called %d times under race, want 1", got)
	}
}

// TestResolver_ScipMissing surfaces a clean error when the store
// holds no SCIP blob for the requested coordinate.
func TestResolver_ScipMissing(t *testing.T) {
	mirror := buildMirrorTree(t, "miss", "0.1")
	br := &fakeBlobReader{blobs: map[string][]byte{}}
	r := NewResolver(br, mirror, t.TempDir())

	_, _, err := r.Resolve(context.Background(), "miss", "0.1")
	if err == nil {
		t.Fatalf("expected error for missing scip blob, got nil")
	}
}

// TestResolver_SourceJSONMissing surfaces a clean error when the
// SCIP blob is present but source.json isn't in the mirror.
func TestResolver_SourceJSONMissing(t *testing.T) {
	mirror := t.TempDir()
	br := &fakeBlobReader{blobs: map[string][]byte{
		"orphan@0.1": buildTinySCIP(t),
	}}
	r := NewResolver(br, mirror, t.TempDir())

	_, _, err := r.Resolve(context.Background(), "orphan", "0.1")
	if err == nil {
		t.Fatalf("expected error when source.json missing, got nil")
	}
}

// silence unused-import linter when running test-only files
var _ = bytes.MinRead
var _ tar.Header
var _ gzip.Reader

// TestResolver_EvictionDoesNotCloseInflightRoot exercises the soundness
// of LRU eviction: when a request still holds a *os.Root returned by a
// prior Resolve, evicting that coordinate must NOT invalidate the
// caller's handle. The earlier implementation called root.Close() on
// eviction; if an HTTP request was mid-stream inside http.FileServer at
// that moment, the read would fault with "use of closed file." This
// test pins maxEntries=1 so a second resolve evicts the first, then
// reads through the original root to prove it still works.
func TestResolver_EvictionDoesNotCloseInflightRoot(t *testing.T) {
	mirrorA := buildMirrorTree(t, "moda", "1.0")
	mirrorB := buildMirrorTree(t, "modb", "1.0")

	br := &fakeBlobReader{blobs: map[string][]byte{
		"moda@1.0": buildTinySCIP(t),
		"modb@1.0": buildTinySCIP(t),
	}}

	// Two resolvers can't share a mirror in this fake (one source.json
	// per module). Run with maxEntries=1 against a unified mirror tree.
	unified := t.TempDir()
	for _, src := range []string{mirrorA, mirrorB} {
		if err := copyTree(t, src, unified); err != nil {
			t.Fatalf("copy mirror: %v", err)
		}
	}

	r := NewResolver(br, unified, t.TempDir())
	r.maxEntries = 1

	_, rootA, err := r.Resolve(context.Background(), "moda", "1.0")
	if err != nil {
		t.Fatalf("Resolve moda: %v", err)
	}
	// Hold rootA — simulate an in-flight HTTP request still using it.
	// Resolving a different coordinate must evict moda from the cache.
	_, _, err = r.Resolve(context.Background(), "modb", "1.0")
	if err != nil {
		t.Fatalf("Resolve modb: %v", err)
	}

	// Confirm moda was actually evicted (LRU semantics).
	r.mu.Lock()
	_, modaStillCached := r.entries["moda@1.0"]
	r.mu.Unlock()
	if modaStillCached {
		t.Fatalf("expected moda to be evicted under maxEntries=1; entries=%v",
			func() []string {
				keys := make([]string, 0, len(r.entries))
				r.mu.Lock()
				for k := range r.entries {
					keys = append(keys, k)
				}
				r.mu.Unlock()
				return keys
			}())
	}

	// The original rootA must still be usable — Stat must NOT return
	// "use of closed file" or similar.
	if _, err := rootA.Stat("MODULE.bazel"); err != nil {
		t.Fatalf("rootA unusable after eviction: %v", err)
	}
}

// copyTree mirrors src's whole tree into dst, merging directories.
func copyTree(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode())
	})
}
