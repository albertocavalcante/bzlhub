package verify

import "testing"

// TestSortFindings_OrdersBySeverityThenKindThenModule confirms the
// deterministic wire order: errors come before warnings before infos,
// and within a tier kind/module/version/path sort lexically.
func TestSortFindings_OrdersBySeverityThenKindThenModule(t *testing.T) {
	in := []Finding{
		{Kind: KindOrphanBlobs, Severity: SevInfo, Path: "blobs/z"},
		{Kind: KindBlobIntegrity, Severity: SevError, Module: "b"},
		{Kind: KindIndexMirrorAgreement, Severity: SevWarning, Module: "c"},
		{Kind: KindBlobIntegrity, Severity: SevError, Module: "a"},
		{Kind: KindOrphanBlobs, Severity: SevInfo, Path: "blobs/a"},
		{Kind: KindBlobMissing, Severity: SevError, Module: "a"},
	}
	got := sortFindings(in)
	// expect: a-b errors (by kind: blob_integrity then blob_missing within same module),
	// then warning, then infos by path
	wantOrder := []struct {
		kind     Kind
		severity Severity
		module   string
		path     string
	}{
		{KindBlobIntegrity, SevError, "a", ""},
		{KindBlobIntegrity, SevError, "b", ""},
		{KindBlobMissing, SevError, "a", ""},
		{KindIndexMirrorAgreement, SevWarning, "c", ""},
		{KindOrphanBlobs, SevInfo, "", "blobs/a"},
		{KindOrphanBlobs, SevInfo, "", "blobs/z"},
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("length mismatch: %d vs %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].Kind != w.kind || got[i].Severity != w.severity || got[i].Module != w.module || got[i].Path != w.path {
			t.Errorf("position %d: got %+v want %+v", i, got[i], w)
		}
	}
}

// TestRunCheck_UnknownKindReturnsEmpty: defensive — if a Kind not in
// the switch sneaks in (e.g. from a future Options struct passing
// through old code), we return nil rather than crashing.
func TestRunCheck_UnknownKindReturnsEmpty(t *testing.T) {
	got := runCheck(Kind("not-a-real-kind"), &state{})
	if got != nil {
		t.Errorf("want nil; got %+v", got)
	}
}

// TestSortFindings_StableForEqualEntries: equal-key entries keep input
// order (Stable sort), so the upstream-collected order is preserved.
func TestSortFindings_StableForEqualEntries(t *testing.T) {
	a := Finding{Kind: KindBlobIntegrity, Severity: SevError, Module: "x", Version: "1", Message: "first"}
	b := Finding{Kind: KindBlobIntegrity, Severity: SevError, Module: "x", Version: "1", Message: "second"}
	out := sortFindings([]Finding{a, b})
	if out[0].Message != "first" || out[1].Message != "second" {
		t.Errorf("stable sort violated: %+v", out)
	}
}
