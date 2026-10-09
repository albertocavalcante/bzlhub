package bzlhub

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// captureSlog routes the default slog logger into a buffer for the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func writeModuleDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestServiceIngestDir_WarnsOnUnresolvedRepos(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logs := captureSlog(t)

	dir := writeModuleDir(t, map[string]string{
		"MODULE.bazel": `module(name = "foo", version = "1.0.0")`,
		"defs.bzl":     "load(\"@missing//:x.bzl\", \"y\")\n\ndef helper():\n    y()\n",
	})
	svc := New(db)
	if _, err := svc.IngestDir(ctx, dir); err != nil {
		t.Fatalf("IngestDir: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "scip index has unresolved refs") || !strings.Contains(out, "missing") {
		t.Fatalf("expected unresolved-refs warning naming repo missing, got logs:\n%s", out)
	}
}

func TestGenerateAndStoreScip_ReturnsUnresolvedRepos(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(db)

	dir := writeModuleDir(t, map[string]string{
		"MODULE.bazel": `module(name = "foo", version = "1.0.0")`,
		"defs.bzl":     "load(\"@missing//:x.bzl\", \"y\")\n",
	})
	r, err := svc.IngestDir(ctx, dir)
	if err != nil {
		t.Fatalf("IngestDir: %v", err)
	}
	got, err := svc.generateAndStoreScip(ctx, dir, r)
	if err != nil {
		t.Fatalf("generateAndStoreScip: %v", err)
	}
	if want := []string{"@missing//:x.bzl"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unresolved = %v, want %v", got, want)
	}
}
