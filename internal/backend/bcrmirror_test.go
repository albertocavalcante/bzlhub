package backend

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// seedMirror copies testdata/registry-tree into a fresh temp dir,
// initialises it as a git repository, commits the seed tree, and
// returns an Open'd Mirror pointed at the result. The Backend tests
// exercise the BCRMirror adapter against this real on-disk shape —
// no mocks of bcrmirror itself.
func seedMirror(t *testing.T) *bcrmirror.Mirror {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "registry-tree")
	copyTree(t, src, dir)

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatalf("AddGlob: %v", err)
	}
	if _, err := wt.Commit("seed registry-tree", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	m := bcrmirror.New(dir, "")
	if err := m.Open(t.Context()); err != nil {
		t.Fatalf("Mirror.Open: %v", err)
	}
	return m
}

// copyTree mirrors src → dst, preserving the relative layout.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
}

func readAll(t *testing.T, rc io.ReadCloser, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatalf("Backend method: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return data
}

// TestBCRMirror_SatisfiesBackendInterface is a compile-time guard
// against the adapter drifting away from the Backend contract.
func TestBCRMirror_SatisfiesBackendInterface(t *testing.T) {
	var _ Backend = (*BCRMirror)(nil)
}

// TestBCRMirror_GetMetadata exercises the happy path through the
// BCR-shape read API.
func TestBCRMirror_GetMetadata(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	rc, err := b.GetMetadata(t.Context(), "foo")
	got := readAll(t, rc, err)
	if !strings.Contains(string(got), `"versions"`) {
		t.Errorf("GetMetadata(foo) = %s; want JSON containing \"versions\"", got)
	}
}

// TestBCRMirror_GetMetadataNotFound asserts the ErrModuleNotFound →
// backend.ErrNotFound translation. Handlers depend on this to render
// 404 instead of 500.
func TestBCRMirror_GetMetadataNotFound(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	_, err := b.GetMetadata(t.Context(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMetadata(nonexistent) err = %v; want errors.Is(_, ErrNotFound)", err)
	}
}

// TestBCRMirror_GetModuleBazel covers the version-keyed read path.
func TestBCRMirror_GetModuleBazel(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	rc, err := b.GetModuleBazel(t.Context(), "foo", "1.0.0")
	got := readAll(t, rc, err)
	if !strings.Contains(string(got), "module(") {
		t.Errorf("GetModuleBazel(foo, 1.0.0) = %s; want module() declaration", got)
	}
}

// TestBCRMirror_GetSourceJSON covers source.json.
func TestBCRMirror_GetSourceJSON(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	rc, err := b.GetSourceJSON(t.Context(), "foo", "1.0.0")
	got := readAll(t, rc, err)
	if !strings.Contains(string(got), `"url"`) {
		t.Errorf("GetSourceJSON(foo, 1.0.0) = %s; want url field", got)
	}
}

// TestBCRMirror_GetSourceJSONNotFound asserts ErrVersionNotFound →
// ErrNotFound translation.
func TestBCRMirror_GetSourceJSONNotFound(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	_, err := b.GetSourceJSON(t.Context(), "foo", "9.9.9")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSourceJSON(foo, 9.9.9) err = %v; want ErrNotFound", err)
	}
}

// TestBCRMirror_GetPatchNotFound asserts ErrPatchNotFound → ErrNotFound.
// The fixture has no patches for foo/1.0.0; any patch name should 404.
func TestBCRMirror_GetPatchNotFound(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	_, err := b.GetPatch(t.Context(), "foo", "1.0.0", "0001-doesnt-exist.patch")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPatch on missing patch err = %v; want ErrNotFound", err)
	}
}

// TestBCRMirror_PathTraversalReturnsNotFound asserts bcrmirror's
// ErrInvalidName is translated to ErrNotFound at the Backend
// boundary. From the HTTP handler's perspective, a path-traversal
// attempt is indistinguishable from a 404 — never a 5xx (which would
// leak the rejection back to the caller as "something interesting
// happened here").
func TestBCRMirror_PathTraversalReturnsNotFound(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	cases := []struct {
		name   string
		call   func() (io.ReadCloser, error)
		expect string
	}{
		{
			name: "GetMetadata with ..",
			call: func() (io.ReadCloser, error) {
				return b.GetMetadata(t.Context(), "..")
			},
			expect: "module name with ..",
		},
		{
			name: "GetSourceJSON with slash in module",
			call: func() (io.ReadCloser, error) {
				return b.GetSourceJSON(t.Context(), "foo/bar", "1.0.0")
			},
			expect: "module name with /",
		},
		{
			name: "GetPatch with .. version",
			call: func() (io.ReadCloser, error) {
				return b.GetPatch(t.Context(), "foo", "..", "any.patch")
			},
			expect: "version with ..",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call()
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s err = %v; want ErrNotFound (got translation of %s)", tc.name, err, tc.expect)
			}
		})
	}
}

// TestBCRMirror_GetBazelRegistryJSON asserts the registry-root file
// is served. bcrmirror itself doesn't have a public read for this
// path; the adapter reads it from Mirror.Path directly.
func TestBCRMirror_GetBazelRegistryJSON(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	rc, err := b.GetBazelRegistryJSON(t.Context())
	got := readAll(t, rc, err)
	if len(got) == 0 {
		t.Errorf("GetBazelRegistryJSON returned empty bytes")
	}
}

// TestBCRMirror_GetBlob covers blobs/ which sits outside the
// BCR-shape modules/ tree. Adapter reads from Mirror.Path/blobs/.
func TestBCRMirror_GetBlob(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	rc, err := b.GetBlob(t.Context(), "foo-1.0.0.tar.gz")
	got := readAll(t, rc, err)
	if len(got) == 0 {
		t.Errorf("GetBlob returned empty bytes")
	}
}

// TestBCRMirror_GetBlobNotFound asserts an absent blob → ErrNotFound.
func TestBCRMirror_GetBlobNotFound(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	_, err := b.GetBlob(t.Context(), "nope.tar.gz")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBlob on missing blob err = %v; want ErrNotFound", err)
	}
}

// TestBCRMirror_GetBlobRejectsPathTraversal asserts the same
// hardening File backend applies (blobs are read straight from
// disk; the key must not contain separators).
func TestBCRMirror_GetBlobRejectsPathTraversal(t *testing.T) {
	m := seedMirror(t)
	b := NewBCRMirror(m)

	_, err := b.GetBlob(t.Context(), "../bazel_registry.json")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBlob path traversal err = %v; want ErrNotFound", err)
	}
}
