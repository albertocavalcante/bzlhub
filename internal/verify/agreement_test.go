package verify

import "testing"

func TestCheckIndexMirrorAgreement_BothSidesAligned(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true},
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0 findings; got %+v", got)
	}
}

func TestCheckIndexMirrorAgreement_IndexOnly(t *testing.T) {
	// ghost row: indexed but no on-disk tree
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true},
		},
		indexRows: []indexRow{
			{name: "ghost", version: "1.2.3"},
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1; got %d: %+v", len(got), got)
	}
	if got[0].Module != "ghost" || got[0].Severity != SevWarning {
		t.Errorf("want ghost@1.2.3 warning; got %+v", got[0])
	}
}

func TestCheckIndexMirrorAgreement_MirrorOnly(t *testing.T) {
	// on-disk but not indexed
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")}, // indexed=false
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1; got %d: %+v", len(got), got)
	}
	if got[0].Module != "foo" || got[0].Severity != SevWarning {
		t.Errorf("want foo@1.0.0 warning; got %+v", got[0])
	}
	if !findingHasMessage(got, "missing from index") {
		t.Errorf("want 'missing from index' message; got %q", got[0].Message)
	}
}

// TestCheckIndexMirrorAgreement_SameNameMultipleVersions: forces the
// version-tiebreak path in the sort comparator (different versions of
// the same module names).
func TestCheckIndexMirrorAgreement_SameNameMultipleVersions(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "shared", version: "1.0.0", blobBytes: []byte("a")},
			{name: "shared", version: "2.0.0", blobBytes: []byte("b")},
		},
		indexRows: []indexRow{
			{name: "shared", version: "1.0.0"},
			{name: "shared", version: "2.0.0"},
			{name: "shared", version: "3.0.0"}, // ghost row
			{name: "shared", version: "4.0.0"}, // ghost row
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	// Want 2 ghost-row warnings, no mirror-only warnings.
	if len(got) != 2 {
		t.Fatalf("want 2; got %d: %+v", len(got), got)
	}
	if got[0].Version != "3.0.0" || got[1].Version != "4.0.0" {
		t.Errorf("version ordering: got %s, %s", got[0].Version, got[1].Version)
	}
}

// TestCheckIndexMirrorAgreement_NRowsGap: 4 rows in the index, 3 on
// disk → exactly one Warning for the missing tree, naming the gap.
func TestCheckIndexMirrorAgreement_NRowsGap(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "a", version: "1.0.0", blobBytes: []byte("a"), indexed: true},
			{name: "b", version: "1.0.0", blobBytes: []byte("b"), indexed: true},
			{name: "c", version: "1.0.0", blobBytes: []byte("c"), indexed: true},
		},
		indexRows: []indexRow{
			{name: "missing-on-disk", version: "9.9.9"},
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	if len(got) != 1 || got[0].Module != "missing-on-disk" {
		t.Fatalf("want 1 warning for missing-on-disk; got %+v", got)
	}
}

// TestCheckIndexMirrorAgreement_SameNameMultipleVersions_MirrorOnly:
// forces the version-tiebreak on the mirror-only side.
func TestCheckIndexMirrorAgreement_SameNameMultipleVersions_MirrorOnly(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "shared", version: "1.0.0", blobBytes: []byte("a")},
			{name: "shared", version: "2.0.0", blobBytes: []byte("b")},
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	if len(got) != 2 {
		t.Fatalf("want 2; got %d: %+v", len(got), got)
	}
	if got[0].Version != "1.0.0" || got[1].Version != "2.0.0" {
		t.Errorf("version ordering: got %s, %s", got[0].Version, got[1].Version)
	}
}

func TestCheckIndexMirrorAgreement_NoStore(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		skipDB: true,
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkIndexMirrorAgreement(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("with no DB, want 0 findings; got %+v", got)
	}
}
