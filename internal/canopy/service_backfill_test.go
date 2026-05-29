package canopy

import (
	"context"
	"testing"

	"github.com/albertocavalcante/assay/report"

	scipproto "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// TestBackfillSourceIndexFlags_FlipsForBlobWithDocs covers the
// reconcile path: a row whose has_source_index flag is the column
// default (false) but whose SCIP blob contains at least one
// document should be flipped to true on backfill.
func TestBackfillSourceIndexFlags_FlipsForBlobWithDocs(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	// "alpha" has a non-empty SCIP blob but no SetHasSourceIndex
	// call — this mimics the pre-cache world where the column
	// didn't exist.
	if err := svc.store.WriteReport(ctx, &report.ModuleReport{Name: "alpha", Version: "1.0"}); err != nil {
		t.Fatalf("WriteReport alpha: %v", err)
	}
	if err := svc.store.WriteScipBlob(ctx, "alpha", "1.0", oneDocBlob(t)); err != nil {
		t.Fatalf("WriteScipBlob alpha: %v", err)
	}

	// "beta" has no SCIP blob at all — must stay false.
	if err := svc.store.WriteReport(ctx, &report.ModuleReport{Name: "beta", Version: "1.0"}); err != nil {
		t.Fatalf("WriteReport beta: %v", err)
	}

	// "gamma" was already reconciled (flag already true). Backfill
	// must skip it cheaply, not double-count.
	if err := svc.store.WriteReport(ctx, &report.ModuleReport{Name: "gamma", Version: "1.0"}); err != nil {
		t.Fatalf("WriteReport gamma: %v", err)
	}
	if err := svc.store.WriteScipBlob(ctx, "gamma", "1.0", oneDocBlob(t)); err != nil {
		t.Fatalf("WriteScipBlob gamma: %v", err)
	}
	if err := svc.store.SetHasSourceIndex(ctx, "gamma", "1.0", true); err != nil {
		t.Fatalf("SetHasSourceIndex gamma: %v", err)
	}

	n, err := svc.BackfillSourceIndexFlags(ctx)
	if err != nil {
		t.Fatalf("BackfillSourceIndexFlags: %v", err)
	}
	// alpha flipped → 1 update. beta + gamma → no change.
	if n != 1 {
		t.Errorf("rows updated = %d, want 1 (alpha only)", n)
	}

	checkFlag(t, ctx, svc, "alpha", "1.0", true)
	checkFlag(t, ctx, svc, "beta", "1.0", false)
	checkFlag(t, ctx, svc, "gamma", "1.0", true)
}

// TestBackfillSourceIndexFlags_EmptyBlobStaysFalse: a SCIP blob that
// parses but has zero documents (C-library wrapper modules whose
// tarball ships no .bzl files) must stay false. The backfill
// shouldn't lie about empty indexes.
func TestBackfillSourceIndexFlags_EmptyBlobStaysFalse(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	if err := svc.store.WriteReport(ctx, &report.ModuleReport{Name: "empty", Version: "1.0"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := svc.store.WriteScipBlob(ctx, "empty", "1.0", emptyBlob(t)); err != nil {
		t.Fatalf("WriteScipBlob: %v", err)
	}

	n, err := svc.BackfillSourceIndexFlags(ctx)
	if err != nil {
		t.Fatalf("BackfillSourceIndexFlags: %v", err)
	}
	if n != 0 {
		t.Errorf("empty-blob row should not count as updated; got n=%d", n)
	}
	checkFlag(t, ctx, svc, "empty", "1.0", false)
}

// TestBackfillSourceIndexFlags_NoRowsNoError: empty index → 0 updates
// + nil error. Guards against the "no versions" boot path.
func TestBackfillSourceIndexFlags_NoRowsNoError(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	n, err := svc.BackfillSourceIndexFlags(ctx)
	if err != nil {
		t.Errorf("empty index backfill should not error: %v", err)
	}
	if n != 0 {
		t.Errorf("empty index → 0 updates, got %d", n)
	}
}

func checkFlag(t *testing.T, ctx context.Context, svc *Service, module, version string, want bool) {
	t.Helper()
	got, err := svc.store.GetHasSourceIndex(ctx, module, version)
	if err != nil {
		t.Errorf("GetHasSourceIndex(%s@%s): %v", module, version, err)
		return
	}
	if got != want {
		t.Errorf("%s@%s: got has_source_index=%v, want %v", module, version, got, want)
	}
}

// oneDocBlob returns a minimal SCIP index with one Document carrying
// one Occurrence — enough that understory.Files() counts it as a
// non-empty document. A document with no occurrences gets filtered
// out by understory.
func oneDocBlob(t *testing.T) []byte {
	t.Helper()
	idx := &scipproto.Index{
		Metadata: &scipproto.Metadata{Version: 0},
		Documents: []*scipproto.Document{{
			RelativePath: "MODULE.bazel",
			Occurrences: []*scipproto.Occurrence{{
				Symbol:      "test",
				Range:       []int32{0, 0, 0, 1},
				SymbolRoles: int32(scipproto.SymbolRole_Definition),
			}},
		}},
	}
	out, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal scip: %v", err)
	}
	return out
}

// emptyBlob: parses successfully but zero documents.
func emptyBlob(t *testing.T) []byte {
	t.Helper()
	idx := &scipproto.Index{Metadata: &scipproto.Metadata{Version: 0}}
	out, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal scip: %v", err)
	}
	return out
}
