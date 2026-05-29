package verify

import "testing"

func TestCheckScipPresent_AllIndexedHaveScip(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true, scipBlob: []byte("fake-scip-foo")},
			{name: "bar", version: "2.0.0", blobBytes: []byte("body"), indexed: true, scipBlob: []byte("fake-scip-bar")},
		},
	})
	got := checkScipPresent(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0 findings; got %+v", got)
	}
}

func TestCheckScipPresent_MissingScipBlob(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			// indexed but no scipBlob set — the legacy "ingested before
			// scip-bazel wiring" case.
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), indexed: true},
		},
	})
	got := checkScipPresent(mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1 finding; got %d: %+v", len(got), got)
	}
	if got[0].Kind != KindScipMissing || got[0].Severity != SevWarning {
		t.Errorf("kind/severity: got %s/%s; want scip_missing/warning", got[0].Kind, got[0].Severity)
	}
	if got[0].Module != "foo" || got[0].Version != "1.0.0" {
		t.Errorf("identity: got %s@%s; want foo@1.0.0", got[0].Module, got[0].Version)
	}
	if !findingHasMessage(got, "no SCIP blob") {
		t.Errorf("want 'no SCIP blob' in message; got %q", got[0].Message)
	}
	if got[0].Fix == "" {
		t.Errorf("want non-empty Fix hint; got empty")
	}
}

func TestCheckScipPresent_OrderingDeterministic(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			// All indexed, none with scip blob → all three should appear,
			// sorted by name then version.
			{name: "zeta", version: "1.0.0", blobBytes: []byte("z"), indexed: true},
			{name: "alpha", version: "2.0.0", blobBytes: []byte("a2"), indexed: true},
			{name: "alpha", version: "1.0.0", blobBytes: []byte("a1"), indexed: true},
		},
	})
	got := checkScipPresent(mustBuildState(t, fm))
	if len(got) != 3 {
		t.Fatalf("want 3 findings; got %d: %+v", len(got), got)
	}
	if got[0].Module != "alpha" || got[0].Version != "1.0.0" {
		t.Errorf("got[0]: got %s@%s; want alpha@1.0.0", got[0].Module, got[0].Version)
	}
	if got[1].Module != "alpha" || got[1].Version != "2.0.0" {
		t.Errorf("got[1]: got %s@%s; want alpha@2.0.0", got[1].Module, got[1].Version)
	}
	if got[2].Module != "zeta" || got[2].Version != "1.0.0" {
		t.Errorf("got[2]: got %s@%s; want zeta@1.0.0", got[2].Module, got[2].Version)
	}
}

func TestCheckScipPresent_PartialCoverage(t *testing.T) {
	// Mixed: one has a SCIP blob, one doesn't → exactly one finding for
	// the missing case.
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "covered", version: "1.0.0", blobBytes: []byte("c"), indexed: true, scipBlob: []byte("scip")},
			{name: "missing", version: "1.0.0", blobBytes: []byte("m"), indexed: true},
		},
	})
	got := checkScipPresent(mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1 finding; got %d: %+v", len(got), got)
	}
	if got[0].Module != "missing" {
		t.Errorf("want missing@1.0.0; got %s@%s", got[0].Module, got[0].Version)
	}
}

func TestCheckScipPresent_OnDiskOnlyIgnored(t *testing.T) {
	// A module present on disk but NOT in the index — the agreement
	// check surfaces it; scip_present should stay silent (nothing to
	// check against until the module is actually indexed).
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "ondisk", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkScipPresent(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0 findings (not indexed → not our concern); got %+v", got)
	}
}

func TestCheckScipPresent_NoStore(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		skipDB: true,
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkScipPresent(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("with no DB, want 0 findings; got %+v", got)
	}
}
