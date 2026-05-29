package codenav

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// buildFixtureTarGz writes a small gzipped tar containing files under a
// top-level directory (so strip_prefix has something to strip). Returns
// the tarball bytes.
func buildFixtureTarGz(t *testing.T, prefix string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	// Emit a directory entry first — some tar producers do, ours must
	// tolerate either shape.
	if prefix != "" {
		if err := tw.WriteHeader(&tar.Header{
			Name:     prefix + "/",
			Typeflag: tar.TypeDir,
			Mode:     0o755,
		}); err != nil {
			t.Fatalf("dir header: %v", err)
		}
	}
	for name, body := range files {
		full := name
		if prefix != "" {
			full = prefix + "/" + name
		}
		hdr := &tar.Header{
			Name:     full,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("file header %s: %v", full, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body %s: %v", full, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gw close: %v", err)
	}
	return buf.Bytes()
}

// writeSourceJSON writes a minimal source.json next to the tarball blob
// and returns the path.
func writeSourceJSON(t *testing.T, dir, integrity, stripPrefix string) string {
	t.Helper()
	src := map[string]any{
		"url":          "https://example.invalid/foo-1.0.tar.gz",
		"integrity":    integrity,
		"strip_prefix": stripPrefix,
	}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal source.json: %v", err)
	}
	p := filepath.Join(dir, "source.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write source.json: %v", err)
	}
	return p
}

// TestUnpackSource_StripsPrefix builds a fixture tarball with a
// "foo-1.0/" top-level directory and verifies the unpacker strips it
// per source.json.strip_prefix and lands files at the destination root.
func TestUnpackSource_StripsPrefix(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"MODULE.bazel":   "module(name = \"foo\", version = \"1.0\")\n",
		"BUILD.bazel":    "# build\n",
		"src/hello.bzl":  "x = 1\n",
		"src/world.bzl":  "y = 2\n",
	}
	tarBytes := buildFixtureTarGz(t, "foo-1.0", files)

	sum := sha256.Sum256(tarBytes)
	hexName := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	srcJSONDir := t.TempDir()
	sourcePath := writeSourceJSON(t, srcJSONDir, integrity, "foo-1.0")

	dest := filepath.Join(t.TempDir(), "extracted")
	if err := unpackSource(blobs, sourcePath, dest); err != nil {
		t.Fatalf("unpackSource: %v", err)
	}

	for relPath, want := range files {
		p := filepath.Join(dest, relPath)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		if string(got) != want {
			t.Errorf("%s: got %q want %q", relPath, got, want)
		}
	}
}

// TestUnpackSource_Idempotent re-running unpackSource on a directory
// already populated should no-op (no error, contents unchanged).
func TestUnpackSource_Idempotent(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	tarBytes := buildFixtureTarGz(t, "bar-2.0", map[string]string{
		"MODULE.bazel": "module(name=\"bar\")\n",
	})
	sum := sha256.Sum256(tarBytes)
	hexName := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	srcDir := t.TempDir()
	sourcePath := writeSourceJSON(t, srcDir, integrity, "bar-2.0")

	dest := filepath.Join(t.TempDir(), "extracted")
	if err := unpackSource(blobs, sourcePath, dest); err != nil {
		t.Fatalf("first unpack: %v", err)
	}
	// Mutate the file to detect a re-extract.
	canary := filepath.Join(dest, "MODULE.bazel")
	if err := os.WriteFile(canary, []byte("MUTATED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unpackSource(blobs, sourcePath, dest); err != nil {
		t.Fatalf("second unpack: %v", err)
	}
	got, err := os.ReadFile(canary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "MUTATED\n" {
		t.Fatalf("idempotent re-extract overwrote canary: got %q", got)
	}
}

// TestUnpackSource_NoStripPrefix accepts an empty strip_prefix and
// lays files out verbatim.
func TestUnpackSource_NoStripPrefix(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	tarBytes := buildFixtureTarGz(t, "", map[string]string{
		"a.bzl": "alpha\n",
	})
	sum := sha256.Sum256(tarBytes)
	hexName := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	srcDir := t.TempDir()
	sourcePath := writeSourceJSON(t, srcDir, integrity, "")

	dest := filepath.Join(t.TempDir(), "extracted")
	if err := unpackSource(blobs, sourcePath, dest); err != nil {
		t.Fatalf("unpackSource: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "a.bzl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\n" {
		t.Errorf("got %q", got)
	}
}

// TestUnpackSource_BlobMissing returns a clear error when the integrity-
// derived blob doesn't exist (mirror tarball never made it to disk).
func TestUnpackSource_BlobMissing(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	// Integrity for some bytes we never stored.
	sum := sha256.Sum256([]byte("ghost"))
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	srcDir := t.TempDir()
	sourcePath := writeSourceJSON(t, srcDir, integrity, "")

	dest := filepath.Join(t.TempDir(), "extracted")
	err := unpackSource(blobs, sourcePath, dest)
	if err == nil {
		t.Fatalf("expected error for missing blob, got nil")
	}
}

// TestUnpackSource_PartialWriteForcesRedo — if a previous extract
// crashed mid-stream (some files exist but the tree is incomplete),
// the idempotency probe must NOT treat it as cache-hit. A `.complete`
// sentinel written only after the full extract is the source of truth;
// the "directory non-empty" heuristic is too permissive.
func TestUnpackSource_PartialWriteForcesRedo(t *testing.T) {
	dir := t.TempDir()
	blobs := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	tarBytes := buildFixtureTarGz(t, "tree-1.0", map[string]string{
		"MODULE.bazel": "module(name=\"tree\")\n",
		"src/a.bzl":    "a = 1\n",
		"src/b.bzl":    "b = 2\n",
	})
	sum := sha256.Sum256(tarBytes)
	hexName := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	srcDir := t.TempDir()
	sourcePath := writeSourceJSON(t, srcDir, integrity, "tree-1.0")

	dest := filepath.Join(t.TempDir(), "extracted")
	// Simulate a partial extract: drop a single stale file with no
	// completion sentinel.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "MODULE.bazel"), []byte("STALE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := unpackSource(blobs, sourcePath, dest); err != nil {
		t.Fatalf("unpack on partial state: %v", err)
	}

	// MODULE.bazel must now hold the real content from the tarball.
	got, err := os.ReadFile(filepath.Join(dest, "MODULE.bazel"))
	if err != nil {
		t.Fatalf("read MODULE.bazel: %v", err)
	}
	if string(got) == "STALE\n" {
		t.Errorf("partial state was wrongly trusted as cache-hit; MODULE.bazel still STALE")
	}
	// And the previously-missing files must exist.
	for _, f := range []string{"src/a.bzl", "src/b.bzl"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("missing %s after redo: %v", f, err)
		}
	}
}

// TestUnpackSource_TarPathSafety: parent-dir segments (`..`) are
// rejected, but innocent filenames that *contain* `..` as a substring
// (like `..foo` or `bar..baz`) pass. The old strings.Contains check
// was a heuristic that conflated the two.
func TestUnpackSource_TarPathSafety(t *testing.T) {
	cases := []struct {
		name      string
		paths     []string // tarball entry names under the strip prefix
		wantError bool
	}{
		{
			name:      "innocent_dotdot_filename",
			paths:     []string{"..rcfile", "bar..baz.txt", "src/foo..bar.bzl"},
			wantError: false,
		},
		{
			name:      "parent_dir_segment",
			paths:     []string{"src/../../etc/passwd"},
			wantError: true,
		},
		{
			name:      "absolute_path",
			paths:     []string{"/etc/shadow"},
			wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			blobs := filepath.Join(dir, "blobs")
			if err := os.MkdirAll(blobs, 0o755); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{}
			for _, p := range tc.paths {
				files[p] = "x"
			}
			tarBytes := buildFixtureTarGz(t, "safe-1.0", files)
			sum := sha256.Sum256(tarBytes)
			hexName := hex.EncodeToString(sum[:])
			if err := os.WriteFile(filepath.Join(blobs, hexName), tarBytes, 0o644); err != nil {
				t.Fatal(err)
			}
			integrity := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
			srcDir := t.TempDir()
			sourcePath := writeSourceJSON(t, srcDir, integrity, "safe-1.0")

			dest := filepath.Join(t.TempDir(), "extracted")
			err := unpackSource(blobs, sourcePath, dest)
			if tc.wantError && err == nil {
				t.Errorf("expected error for %v, got nil", tc.paths)
			}
			if !tc.wantError && err != nil {
				t.Errorf("expected success for %v, got: %v", tc.paths, err)
			}
		})
	}
}
