package archive

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// makeZip builds an in-memory .zip from a map of relative path → content.
// Mirrors makeTarGz so zip tests stay symmetric with tar.gz tests.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractZipStripsPrefix(t *testing.T) {
	a := makeZip(t, map[string]string{
		"foo-1.0.0/MODULE.bazel":  "module(name=\"foo\")\n",
		"foo-1.0.0/BUILD.bazel":   "\n",
		"foo-1.0.0/lib/defs.bzl":  "x = 1\n",
	})
	dest := t.TempDir()
	if _, err := ExtractZip(bytes.NewReader(a), dest, "foo-1.0.0", 0); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, p := range []string{"MODULE.bazel", "BUILD.bazel", "lib/defs.bzl"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing extracted file %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "foo-1.0.0")); !os.IsNotExist(err) {
		t.Errorf("strip_prefix dir leaked into output (err=%v)", err)
	}
}

func TestExtractZipRejectsTraversal(t *testing.T) {
	a := makeZip(t, map[string]string{
		"foo-1.0.0/../escape.bzl": "BAD\n",
	})
	dest := t.TempDir()
	_, err := ExtractZip(bytes.NewReader(a), dest, "foo-1.0.0", 0)
	if err == nil {
		t.Fatalf("expected traversal rejection, got nil")
	}
}

func TestExtractZipNoStripPrefix(t *testing.T) {
	a := makeZip(t, map[string]string{
		"a.txt":     "a",
		"sub/b.txt": "b",
	})
	dest := t.TempDir()
	if _, err := ExtractZip(bytes.NewReader(a), dest, "", 0); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, p := range []string{"a.txt", "sub/b.txt"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}

// TestExtractZipPreservesNestedDirs checks that an entry deeper than the
// prefix creates the intermediate directories. zip entries can come in
// any order (unlike tar where dirs typically precede files); ExtractZip
// must MkdirAll the parent regardless.
func TestExtractZipPreservesNestedDirs(t *testing.T) {
	a := makeZip(t, map[string]string{
		// Deliberately out of order: deeper file first.
		"root-2.0/a/b/c/deep.bzl": "deep\n",
		"root-2.0/a/b/c/peer.bzl": "peer\n",
	})
	dest := t.TempDir()
	if _, err := ExtractZip(bytes.NewReader(a), dest, "root-2.0", 0); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, p := range []string{"a/b/c/deep.bzl", "a/b/c/peer.bzl"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing nested %s: %v", p, err)
		}
	}
}
