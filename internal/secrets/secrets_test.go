package secrets

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead_FileWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANOPY_TEST_TOKEN_FILE", path)
	t.Setenv("CANOPY_TEST_TOKEN", "from-env")
	got := Read("CANOPY_TEST_TOKEN")
	if got != "from-file" {
		t.Fatalf("got %q, want %q", got, "from-file")
	}
}

func TestRead_EnvFallback(t *testing.T) {
	t.Setenv("CANOPY_TEST_TOKEN", "literal")
	if got := Read("CANOPY_TEST_TOKEN"); got != "literal" {
		t.Fatalf("got %q, want literal", got)
	}
}

func TestRead_NeitherSet(t *testing.T) {
	if got := Read("CANOPY_TEST_TOKEN_NONE"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestRead_FileUnreadable(t *testing.T) {
	t.Setenv("CANOPY_TEST_TOKEN_FILE", "/nonexistent/never-was/a/file")
	// Should NOT fall through to the literal env value; the user
	// pointed us at a file, the file is missing, that's an error
	// state — return empty so the consumer surfaces "no token" cleanly.
	t.Setenv("CANOPY_TEST_TOKEN", "this-should-not-leak")
	if got := Read("CANOPY_TEST_TOKEN"); got != "" {
		t.Fatalf("got %q, want empty when file path is set but unreadable", got)
	}
}

func TestRead_FileUnreadableLogsDiagnosticWithoutSecretValue(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	path := filepath.Join(t.TempDir(), "missing-token")
	t.Setenv("CANOPY_TEST_TOKEN_FILE", path)
	t.Setenv("CANOPY_TEST_TOKEN", "literal-secret-value")

	if got := Read("CANOPY_TEST_TOKEN"); got != "" {
		t.Fatalf("got %q, want empty when file path is set but unreadable", got)
	}
	logged := buf.String()
	for _, want := range []string{"secret file unreadable", "CANOPY_TEST_TOKEN_FILE", path} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log missing %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "literal-secret-value") {
		t.Fatalf("log leaked fallback env secret:\n%s", logged)
	}
}

func TestRead_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok")
	if err := os.WriteFile(path, []byte("  padded  \n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANOPY_TEST_TOKEN_FILE", path)
	if got := Read("CANOPY_TEST_TOKEN"); got != "padded" {
		t.Fatalf("got %q, want trimmed 'padded'", got)
	}
}

func TestLazyRead_PicksUpRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tok")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANOPY_TEST_TOKEN_FILE", path)
	f := LazyRead("CANOPY_TEST_TOKEN")
	if got := f(); got != "v1" {
		t.Fatalf("initial: got %q, want v1", got)
	}
	// Rotate.
	if err := os.WriteFile(path, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f(); got != "v2" {
		t.Fatalf("after rotation: got %q, want v2", got)
	}
}
