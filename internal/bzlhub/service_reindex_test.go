package bzlhub

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
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// mirrorWithSource builds a minimal BCR-shape mirror holding one module's
// source tarball, the same shape internal/codenav unpacks.
func mirrorWithSource(t *testing.T, module, version string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	tw := tar.NewWriter(zw)
	prefix := module + "-" + version + "/"
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: prefix + name, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tarBytes := gz.Bytes()

	sum := sha256.Sum256(tarBytes)
	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, hex.EncodeToString(sum[:])), tarBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	modDir := filepath.Join(root, "modules", module, version)
	if err := os.MkdirAll(modDir, 0o750); err != nil {
		t.Fatal(err)
	}
	src, _ := json.Marshal(map[string]any{
		"url":          "https://example.invalid/x.tar.gz",
		"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(sum[:]),
		"strip_prefix": prefix[:len(prefix)-1],
	})
	if err := os.WriteFile(filepath.Join(modDir, "source.json"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func reindexService(t *testing.T, mirrorRoot string) (*Service, *store.Store) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "bzlhub.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Service{
		store:           db,
		MirrorRoot:      mirrorRoot,
		SourcesCacheDir: t.TempDir(),
	}, db
}

// The point of reindex: a blob stored under the old behaviour is replaced.
// Every blob currently on disk predates both the symbol-grammar fix and the
// transitive closure, so "the bytes changed" is the outcome to assert.
func TestReindexScip_ReplacesAStaleBlob(t *testing.T) {
	ctx := context.Background()
	mirror := mirrorWithSource(t, "mod", "1.0", map[string]string{
		"MODULE.bazel": `module(name = "mod", version = "1.0")`,
		"rules.bzl":    "def my_rule(name):\n    return name\n",
	})
	svc, db := reindexService(t, mirror)

	if err := db.WriteReport(ctx, &report.ModuleReport{Name: "mod", Version: "1.0"}); err != nil {
		t.Fatal(err)
	}
	stale := []byte("not a scip index at all")
	if err := db.WriteScipBlob(ctx, "mod", "1.0", stale); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ReindexScip(ctx, ReindexOptions{})
	if err != nil {
		t.Fatalf("ReindexScip: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1 (skips: %+v)", res.Reindexed, res.Skipped)
	}

	got, err := db.GetScipBlob(ctx, "mod", "1.0")
	if err != nil {
		t.Fatalf("GetScipBlob: %v", err)
	}
	if bytes.Equal(got, stale) {
		t.Error("the stale blob was left in place")
	}
	if len(got) == 0 {
		t.Error("the blob was replaced with nothing")
	}
}

// A dry run must report exactly what a real run would do and write nothing.
// Without this, "I'll check the scope first" is an act of faith.
func TestReindexScip_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	mirror := mirrorWithSource(t, "mod", "1.0", map[string]string{
		"MODULE.bazel": `module(name = "mod", version = "1.0")`,
		"rules.bzl":    "def r():\n    pass\n",
	})
	svc, db := reindexService(t, mirror)
	if err := db.WriteReport(ctx, &report.ModuleReport{Name: "mod", Version: "1.0"}); err != nil {
		t.Fatal(err)
	}
	stale := []byte("stale")
	if err := db.WriteScipBlob(ctx, "mod", "1.0", stale); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ReindexScip(ctx, ReindexOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ReindexScip: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("a dry run should still report what it WOULD do; got %d", res.Reindexed)
	}
	got, _ := db.GetScipBlob(ctx, "mod", "1.0")
	if !bytes.Equal(got, stale) {
		t.Error("dry run modified the store")
	}
}

// A module whose source cannot be materialized is skipped WITH A REASON, and
// the run continues. A silent skip is indistinguishable from success, which is
// the failure mode this whole session has been about.
func TestReindexScip_SkipsWithAReasonAndContinues(t *testing.T) {
	ctx := context.Background()
	mirror := mirrorWithSource(t, "ok", "1.0", map[string]string{
		"MODULE.bazel": `module(name = "ok", version = "1.0")`,
		"rules.bzl":    "def r():\n    pass\n",
	})
	svc, db := reindexService(t, mirror)

	for _, mv := range []struct{ name, version string }{{"ok", "1.0"}, {"not_mirrored", "9.9"}} {
		if err := db.WriteReport(ctx, &report.ModuleReport{Name: mv.name, Version: mv.version}); err != nil {
			t.Fatal(err)
		}
		if err := db.WriteScipBlob(ctx, mv.name, mv.version, []byte("stale")); err != nil {
			t.Fatal(err)
		}
	}

	res, err := svc.ReindexScip(ctx, ReindexOptions{})
	if err != nil {
		t.Fatalf("one unmirrored module must not abort the run: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1", res.Reindexed)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %+v, want exactly one entry", res.Skipped)
	}
	if res.Skipped[0].Module != "not_mirrored" {
		t.Errorf("skipped the wrong module: %+v", res.Skipped[0])
	}
	if res.Skipped[0].Reason == "" {
		t.Error("a skip with no reason cannot be acted on")
	}
	if res.Considered != 2 {
		t.Errorf("Considered = %d, want 2", res.Considered)
	}
}

// Missing configuration is an error, not an empty success. A reindex that
// reports "0 reindexed" because the mirror was never configured looks exactly
// like "nothing needed reindexing".
func TestReindexScip_RequiresMirrorAndCache(t *testing.T) {
	ctx := context.Background()
	for name, svc := range map[string]*Service{
		"no mirror": {SourcesCacheDir: t.TempDir()},
		"no cache":  {MirrorRoot: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.ReindexScip(ctx, ReindexOptions{}); err == nil {
				t.Error("missing configuration was accepted")
			}
		})
	}
}

func TestReindexScip_OnlyRestrictsTheSet(t *testing.T) {
	ctx := context.Background()
	mirror := mirrorWithSource(t, "a", "1.0", map[string]string{
		"MODULE.bazel": `module(name = "a", version = "1.0")`,
		"rules.bzl":    "def r():\n    pass\n",
	})
	svc, db := reindexService(t, mirror)
	for _, n := range []string{"a", "b"} {
		if err := db.WriteReport(ctx, &report.ModuleReport{Name: n, Version: "1.0"}); err != nil {
			t.Fatal(err)
		}
		if err := db.WriteScipBlob(ctx, n, "1.0", []byte("stale")); err != nil {
			t.Fatal(err)
		}
	}

	res, err := svc.ReindexScip(ctx, ReindexOptions{
		Only: []store.ModuleVersion{{Module: "a", Version: "1.0"}},
	})
	if err != nil {
		t.Fatalf("ReindexScip: %v", err)
	}
	if res.Considered != 1 {
		t.Errorf("Considered = %d, want only the requested one", res.Considered)
	}
	if got, _ := db.GetScipBlob(ctx, "b", "1.0"); !bytes.Equal(got, []byte("stale")) {
		t.Error("a module outside --only was reindexed")
	}
}

// A reindex that resolves everything and one that leaves half the closure
// unplaced both currently print "reindexed N of N". The result must say which,
// or an incompletely mirrored deployment looks finished.
func TestReindexScip_ReportsUnresolvedReferences(t *testing.T) {
	ctx := context.Background()
	mirror := mirrorWithSource(t, "mod", "1.0", map[string]string{
		"MODULE.bazel": `module(name = "mod", version = "1.0")`,
		"rules.bzl": `load("@not_mirrored//:defs.bzl", "helper")

def my_rule(name):
    return helper(name)
`,
	})
	svc, db := reindexService(t, mirror)
	if err := db.WriteReport(ctx, &report.ModuleReport{Name: "mod", Version: "1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteScipBlob(ctx, "mod", "1.0", []byte("stale")); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ReindexScip(ctx, ReindexOptions{})
	if err != nil {
		t.Fatalf("ReindexScip: %v", err)
	}
	if res.Reindexed != 1 {
		t.Fatalf("Reindexed = %d, want 1", res.Reindexed)
	}
	if len(res.Unresolved) != 1 {
		t.Fatalf("Unresolved = %+v, want one entry naming the unmirrored dep", res.Unresolved)
	}
	u := res.Unresolved[0]
	if u.Module != "mod" || u.Version != "1.0" {
		t.Errorf("wrong coordinate: %+v", u)
	}
	if len(u.Repos) != 1 || u.Repos[0] != "@not_mirrored//:defs.bzl" {
		t.Errorf("Repos = %v, want the raw load target", u.Repos)
	}
}

// A fully resolved reindex must report nothing, or the field is noise that gets
// ignored.
func TestReindexScip_NoUnresolvedWhenEverythingPlaced(t *testing.T) {
	ctx := context.Background()
	mirror := mirrorWithSource(t, "mod", "1.0", map[string]string{
		"MODULE.bazel": `module(name = "mod", version = "1.0")`,
		"rules.bzl":    "def my_rule(name):\n    return name\n",
	})
	svc, db := reindexService(t, mirror)
	if err := db.WriteReport(ctx, &report.ModuleReport{Name: "mod", Version: "1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteScipBlob(ctx, "mod", "1.0", []byte("stale")); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ReindexScip(ctx, ReindexOptions{})
	if err != nil {
		t.Fatalf("ReindexScip: %v", err)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("Unresolved = %+v, want none", res.Unresolved)
	}
}
