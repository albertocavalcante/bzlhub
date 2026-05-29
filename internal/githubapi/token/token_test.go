package token

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAnonymous_ReturnsEmpty(t *testing.T) {
	tok, err := Anonymous{}.Token(context.Background())
	if err != nil {
		t.Fatalf("Anonymous.Token errored: %v", err)
	}
	if tok != "" {
		t.Fatalf("Anonymous.Token = %q, want empty", tok)
	}
}

func TestPAT_ReadsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("ghp_demo"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANOPY_TEST_GH_TOKEN_FILE", path)
	tok, err := PAT{Env: "CANOPY_TEST_GH_TOKEN"}.Token(context.Background())
	if err != nil {
		t.Fatalf("PAT.Token errored: %v", err)
	}
	if tok != "ghp_demo" {
		t.Fatalf("PAT.Token = %q, want ghp_demo", tok)
	}
}

func TestPAT_EnvFallback(t *testing.T) {
	t.Setenv("CANOPY_TEST_GH_TOKEN", "literal-tok")
	tok, _ := PAT{Env: "CANOPY_TEST_GH_TOKEN"}.Token(context.Background())
	if tok != "literal-tok" {
		t.Fatalf("got %q, want literal-tok", tok)
	}
}

func TestPAT_EmptyEnvNameYieldsEmpty(t *testing.T) {
	tok, err := PAT{}.Token(context.Background())
	if err != nil || tok != "" {
		t.Fatalf("PAT{}.Token = (%q, %v), want (\"\", nil)", tok, err)
	}
}

func TestPAT_MissingFileFallsToEmpty(t *testing.T) {
	t.Setenv("CANOPY_TEST_GH_TOKEN_FILE", "/no/such/file")
	t.Setenv("CANOPY_TEST_GH_TOKEN", "should-not-leak")
	tok, _ := PAT{Env: "CANOPY_TEST_GH_TOKEN"}.Token(context.Background())
	if tok != "" {
		t.Fatalf("got %q, want empty when _FILE set but unreadable", tok)
	}
}

// Compile-time check that the impls satisfy the interface.
var (
	_ Provider = Anonymous{}
	_ Provider = PAT{}
)
