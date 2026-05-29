package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz builds an in-memory tar.gz from a map of relative path → content.
// Directories are auto-derived from file paths.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gz: %v", err)
	}
	return buf.Bytes()
}

func TestExtractStripsPrefix(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"foo-1.0.0/MODULE.bazel":     "module(name=\"foo\")\n",
		"foo-1.0.0/BUILD.bazel":      "\n",
		"foo-1.0.0/lib/defs.bzl":     "x = 1\n",
	})
	dest := t.TempDir()
	if _, err := ExtractTarGz(bytes.NewReader(archive), dest, "foo-1.0.0", 0); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, p := range []string{"MODULE.bazel", "BUILD.bazel", "lib/defs.bzl"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing extracted file %s: %v", p, err)
		}
	}
	// strip_prefix dir itself should NOT exist
	if _, err := os.Stat(filepath.Join(dest, "foo-1.0.0")); !os.IsNotExist(err) {
		t.Errorf("strip_prefix dir leaked into output (err=%v)", err)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"foo-1.0.0/../escape.bzl": "BAD\n",
	})
	dest := t.TempDir()
	_, err := ExtractTarGz(bytes.NewReader(archive), dest, "foo-1.0.0", 0)
	if err == nil {
		t.Fatalf("expected traversal rejection, got nil")
	}
}

func TestExtractNoStripPrefix(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"a.txt":     "a",
		"sub/b.txt": "b",
	})
	dest := t.TempDir()
	if _, err := ExtractTarGz(bytes.NewReader(archive), dest, "", 0); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, p := range []string{"a.txt", "sub/b.txt"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
}
