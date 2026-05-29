package verify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerify_HealthyMirror: end-to-end. A clean mirror produces a
// Report with zero findings (info/warn/error all 0) and the right
// examined counts.
func TestVerify_HealthyMirror(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body-foo"), indexed: true, scipBlob: []byte("scip-foo")},
			{name: "bar", version: "2.0.0", blobBytes: []byte("body-bar"), indexed: true, scipBlob: []byte("scip-bar")},
		},
	})
	r, err := Verify(context.Background(), Options{
		MirrorRoot: fm.root,
		DBPath:     fm.dbPath,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Errors != 0 || r.Warnings != 0 || r.Info != 0 {
		t.Errorf("want zero findings; got %+v", r)
	}
	if r.ModulesExamined != 2 || r.BlobsExamined != 2 {
		t.Errorf("counts: got modules=%d blobs=%d", r.ModulesExamined, r.BlobsExamined)
	}
}

// TestVerify_TamperedBlob: end-to-end. Hand-tamper one blob and verify
// reports exactly one Error finding of kind blob_integrity.
func TestVerify_TamperedBlob(t *testing.T) {
	body := []byte("original")
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: body, indexed: true},
		},
	})
	hexname, _ := blobBytesFor(body)
	must(t, os.WriteFile(filepath.Join(fm.root, "blobs", hexname), []byte("GARBAGE"), 0o644))

	r, err := Verify(context.Background(), Options{
		MirrorRoot: fm.root,
		DBPath:     fm.dbPath,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Errors != 1 {
		t.Errorf("want 1 error; got %d (%+v)", r.Errors, r.Findings)
	}
	if r.Findings[0].Kind != KindBlobIntegrity {
		t.Errorf("kind: want blob_integrity; got %s", r.Findings[0].Kind)
	}
}

// TestVerify_JSONRoundtrip: the Report must be encodable+decodable as
// JSON without losing structure — the JSON CLI output relies on it.
func TestVerify_JSONRoundtrip(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true},
		},
	})
	r, err := Verify(context.Background(), Options{MirrorRoot: fm.root, DBPath: fm.dbPath})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.MirrorRoot != r.MirrorRoot || back.ModulesExamined != r.ModulesExamined {
		t.Errorf("roundtrip changed fields: %+v vs %+v", back, r)
	}
}

// TestVerify_ChecksFilter: passing Checks restricts which checks run.
func TestVerify_ChecksFilter(t *testing.T) {
	// A module with a missing MODULE.bazel (would normally fire
	// module_bazel_present) plus an orphan blob (would fire orphan_blobs).
	// Restricting to only orphan_blobs should hide the module finding.
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true},
		},
		extraBlobs: []extraBlob{
			{name: "legacy.tar.gz", contents: []byte("legacy")},
		},
	})
	// Drop MODULE.bazel after the fact
	must(t, os.Remove(filepath.Join(fm.root, "modules", "foo", "1.0.0", "MODULE.bazel")))

	r, err := Verify(context.Background(), Options{
		MirrorRoot: fm.root,
		DBPath:     fm.dbPath,
		Checks:     []Kind{KindOrphanBlobs},
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, f := range r.Findings {
		if f.Kind != KindOrphanBlobs {
			t.Errorf("filter not honored: got %s finding", f.Kind)
		}
	}
}

// TestVerify_MissingRoot: tool-level error path.
func TestVerify_MissingRoot(t *testing.T) {
	_, err := Verify(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "MirrorRoot") {
		t.Fatalf("want MirrorRoot error; got %v", err)
	}
}

// TestVerify_BadDB: a path that can't be opened as SQLite surfaces as
// a tool-level error (exit 1 territory for the CLI), not a Finding.
func TestVerify_BadDB(t *testing.T) {
	// Point --db at a directory; sqlite open will fail.
	dir := t.TempDir()
	_, err := Verify(context.Background(), Options{
		MirrorRoot: dir,
		DBPath:     dir, // directory, not a file
	})
	if err == nil {
		t.Fatalf("want error for bad DB path; got nil")
	}
	if !strings.Contains(err.Error(), "open db") {
		t.Errorf("want 'open db' in error; got %v", err)
	}
}

// TestVerify_FailOnAnyEquivalent: Verify itself doesn't translate to
// exit codes (the CLI does), but the Report's Info / Warnings counters
// must be correct so the CLI can compute exit status. One info-only
// finding produces Info=1, Warnings=0, Errors=0 — the difference that
// matters for --fail-on-any.
func TestVerify_CounterTallies(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true, scipBlob: []byte("scip-foo")},
		},
		extraBlobs: []extraBlob{
			{name: "legacy.tar.gz", contents: []byte("x")},
		},
	})
	r, err := Verify(context.Background(), Options{MirrorRoot: fm.root, DBPath: fm.dbPath})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Errors != 0 || r.Warnings != 0 {
		t.Errorf("want zero err/warn, only info; got %+v", r)
	}
	if r.Info < 1 {
		t.Errorf("want at least one info; got %d", r.Info)
	}
}

// TestVerify_NoDB: --db is optional; the DB-dependent agreement check
// silently no-ops, others still run.
func TestVerify_NoDB(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		skipDB: true,
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	r, err := Verify(context.Background(), Options{MirrorRoot: fm.root})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Healthy module + no DB → zero findings (no agreement check fires).
	if r.Errors != 0 || r.Warnings != 0 {
		t.Errorf("want zero err/warn; got %+v", r)
	}
}
