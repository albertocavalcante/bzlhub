package verify

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/canopy/internal/store"
)

// buildFakeMirror writes a small synthetic BCR-shape mirror tree under
// a t.TempDir() and (optionally) initializes a SQLite store with rows
// matching the layout. Returns the populated state aggregate plus the
// raw paths so individual tests can assert exit codes / paths.
//
// The layout struct is intentionally declarative — tests describe
// "what's on disk" in one literal, and the helper handles the busy work
// of hashing blobs, writing source.json with matching integrity, and
// keeping the SQLite index in sync (or deliberately out of sync, as the
// agreement-check tests need).
type mirrorLayout struct {
	modules     []moduleSpec
	indexRows   []indexRow
	extraBlobs  []extraBlob
	skipDB      bool // build without a *store.Store; tests for the no-db path
}

type moduleSpec struct {
	name        string
	version     string
	blobBytes   []byte // archive contents; SHA256 is auto-computed for integrity
	source      string // when non-empty, overrides the auto-generated source.json
	moduleBazel *string
	skipBlob    bool   // if true, source.json is written but blob is not
	indexed     bool   // also insert an index row for this (m, v)
	scipBlob    []byte // when non-nil, store this as the module's SCIP blob (requires indexed)
}

type indexRow struct {
	name    string
	version string
}

type extraBlob struct {
	name     string // raw filename inside blobs/ (allows non-hex names)
	contents []byte
}

type fakeMirror struct {
	root  string
	dbDir string
	dbPath string
	store *store.Store
}

func buildFakeMirror(t *testing.T, layout mirrorLayout) *fakeMirror {
	t.Helper()
	root := t.TempDir()

	// Ensure the canonical directories exist so tests can rely on them
	// even when a particular layout has zero modules / zero blobs.
	must(t, os.MkdirAll(filepath.Join(root, "modules"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "blobs"), 0o755))

	for _, m := range layout.modules {
		writeModule(t, root, m)
	}

	for _, b := range layout.extraBlobs {
		must(t, os.WriteFile(filepath.Join(root, "blobs", b.name), b.contents, 0o644))
	}

	fm := &fakeMirror{root: root}
	if layout.skipDB {
		return fm
	}

	dbPath := filepath.Join(t.TempDir(), "canopy.db")
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	fm.store = s
	fm.dbPath = dbPath

	// Insert rows for indexed: true modules first, then explicit
	// indexRows (which may name modules with no on-disk tree — that's
	// the "ghost row" case for agreement testing).
	for _, m := range layout.modules {
		if m.indexed {
			writeIndexRow(t, s, m.name, m.version)
		}
		if m.scipBlob != nil {
			if err := s.WriteScipBlob(context.Background(), m.name, m.version, m.scipBlob); err != nil {
				t.Fatalf("write scip blob %s@%s: %v", m.name, m.version, err)
			}
		}
	}
	for _, r := range layout.indexRows {
		writeIndexRow(t, s, r.name, r.version)
	}
	return fm
}

// writeModule materializes one module-version directory. When the spec
// supplies its own source string, it's used verbatim; otherwise a
// well-formed source.json is generated whose integrity matches the
// provided blob bytes. This makes the common case ("module + matching
// blob = no findings") a one-liner in tests.
func writeModule(t *testing.T, root string, m moduleSpec) {
	t.Helper()
	dir := filepath.Join(root, "modules", m.name, m.version)
	must(t, os.MkdirAll(dir, 0o755))

	// source.json
	var srcBytes []byte
	if m.source != "" {
		srcBytes = []byte(m.source)
	} else {
		sum := sha256.Sum256(m.blobBytes)
		sj := map[string]any{
			"type":         "archive",
			"url":          "https://example.invalid/" + m.name + "-" + m.version + ".tar.gz",
			"integrity":    "sha256-" + base64.StdEncoding.EncodeToString(sum[:]),
			"strip_prefix": "",
		}
		b, err := json.MarshalIndent(sj, "", "  ")
		if err != nil {
			t.Fatalf("marshal source.json: %v", err)
		}
		srcBytes = append(b, '\n')
	}
	must(t, os.WriteFile(filepath.Join(dir, "source.json"), srcBytes, 0o644))

	// MODULE.bazel — optional; nil pointer means "don't write the file",
	// empty string means "write zero-length file". The distinction
	// matters for the module_bazel_present tests.
	if m.moduleBazel != nil {
		must(t, os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte(*m.moduleBazel), 0o644))
	} else {
		// Default to a minimal valid MODULE.bazel so the common-case
		// test stays one line. Tests that want the "missing" case set
		// moduleBazel to a sentinel via the explicit nil path: they pass
		// m.moduleBazel == nil AND set skipModuleBazel through a
		// helper. For now: nil == "write a default valid stub".
		stub := "module(name = \"" + m.name + "\", version = \"" + m.version + "\")\n"
		must(t, os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte(stub), 0o644))
	}

	// blob: content-addressed by sha256 hex (no extension), matching
	// internal/mirror.BlobSink.Close().
	if !m.skipBlob {
		sum := sha256.Sum256(m.blobBytes)
		blobName := hex.EncodeToString(sum[:])
		must(t, os.WriteFile(filepath.Join(root, "blobs", blobName), m.blobBytes, 0o644))
	}
}

func writeIndexRow(t *testing.T, s *store.Store, name, version string) {
	t.Helper()
	r := &report.ModuleReport{Name: name, Version: version}
	if err := s.WriteReport(context.Background(), r); err != nil {
		t.Fatalf("write index row %s@%s: %v", name, version, err)
	}
}

// stringPtr is the standard "address-of literal" helper.
// moduleSpec.moduleBazel is *string so tests can distinguish "write
// this content" (non-nil) from "write the default" (nil pointer).
func stringPtr(s string) *string { return &s }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// blobBytesFor is the inverse of writeModule's auto-source-json: given
// raw blob bytes it returns the content-addressed filename, used by
// tests that want to refer to "the blob for module X" by path.
func blobBytesFor(b []byte) (filename, integrity string) {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// storeOpen opens a sqlite store at path, mirroring store.Open with a
// signature convenient for test fixtures (returns *store.Store
// directly; the t-aware fatal flow lives at the call site).
func storeOpen(t *testing.T, path string) (*store.Store, error) {
	t.Helper()
	return store.Open(context.Background(), path)
}

