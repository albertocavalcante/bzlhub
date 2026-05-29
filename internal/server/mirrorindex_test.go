package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrityToHex(t *testing.T) {
	raw := sha256.Sum256([]byte("canopy"))
	want := hex.EncodeToString(raw[:])
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(raw[:])

	got, ok := integrityToHex(integrity)
	if !ok || got != want {
		t.Fatalf("got %q ok=%v, want %q ok=true", got, ok, want)
	}

	for _, bad := range []string{"", "nope", "md5-abc", "sha256-not-base64-?!", "sha256-AAAA"} {
		if _, ok := integrityToHex(bad); ok {
			t.Errorf("expected !ok for %q", bad)
		}
	}
}

func TestURLKeyStripsScheme(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/x/y/v1.tar.gz", "github.com/x/y/v1.tar.gz"},
		{"http://example.com:8080/p/q.tgz", "example.com:8080/p/q.tgz"},
		{"https://host/p?token=abc", "host/p?token=abc"},
	}
	for _, c := range cases {
		got, ok := urlKey(c.in)
		if !ok || got != c.want {
			t.Errorf("urlKey(%q) = %q,%v want %q,true", c.in, got, ok, c.want)
		}
	}
	if _, ok := urlKey("not-a-url"); ok {
		t.Error("expected urlKey(\"not-a-url\") to fail")
	}
}

// writeSrc materializes a synthetic mirror tree with one source.json so the
// index walker has something to find. Returns the sha256-hex of the
// payload (which the test uses to verify the map result).
func writeSrc(t *testing.T, root, name, version, url string, body []byte) string {
	t.Helper()
	dir := filepath.Join(root, "modules", name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	sri := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	srcBytes, _ := json.Marshal(map[string]any{"url": url, "integrity": sri})
	if err := os.WriteFile(filepath.Join(dir, "source.json"), srcBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum[:])
}

func TestMirrorIndexLookupAndRebuild(t *testing.T) {
	root := t.TempDir()
	wantHex := writeSrc(t, root, "foo", "1.0.0",
		"https://github.com/foo/foo/releases/download/1.0.0/foo-1.0.0.tar.gz",
		[]byte("module foo bytes"),
	)
	idx := newMirrorIndex(root)

	got, ok := idx.Lookup("github.com/foo/foo/releases/download/1.0.0/foo-1.0.0.tar.gz")
	if !ok || got != wantHex {
		t.Fatalf("lookup miss: got %q ok=%v want %q ok=true", got, ok, wantHex)
	}

	// Miss case before a fresh ingest:
	if _, ok := idx.Lookup("does/not/exist.tar.gz"); ok {
		t.Error("expected miss for unknown URL")
	}

	// Add a second module after the index was built — Lookup should
	// rebuild on miss and find it.
	wantHex2 := writeSrc(t, root, "bar", "0.1.0",
		"https://github.com/bar/bar/archive/v0.1.0.tar.gz",
		[]byte("module bar bytes"),
	)
	got2, ok := idx.Lookup("github.com/bar/bar/archive/v0.1.0.tar.gz")
	if !ok || got2 != wantHex2 {
		t.Fatalf("post-ingest lookup miss: got %q ok=%v want %q ok=true", got2, ok, wantHex2)
	}
}

func TestURLKeyFromMirrorRequest(t *testing.T) {
	if got := urlKeyFromMirrorRequest("github.com/foo/foo.tar.gz", ""); got != "github.com/foo/foo.tar.gz" {
		t.Errorf("got %q", got)
	}
	if got := urlKeyFromMirrorRequest("/github.com/x.tar.gz", "token=abc"); got != "github.com/x.tar.gz?token=abc" {
		t.Errorf("got %q", got)
	}
}
