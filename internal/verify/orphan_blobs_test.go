package verify

import "testing"

func TestCheckOrphanBlobs_AllReferenced(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkOrphanBlobs(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0; got %+v", got)
	}
}

func TestCheckOrphanBlobs_ExtraHexBlob(t *testing.T) {
	// One module referenced; one stray hex-named blob in the same dir
	// that no source.json references.
	orphanBytes := []byte("definitely orphaned bytes")
	orphanName, _ := blobBytesFor(orphanBytes)
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("real body")},
		},
		extraBlobs: []extraBlob{
			{name: orphanName, contents: orphanBytes},
		},
	})
	got := checkOrphanBlobs(mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1 finding; got %d: %+v", len(got), got)
	}
	if got[0].Kind != KindOrphanBlobs || got[0].Severity != SevInfo {
		t.Errorf("want Info orphan_blobs; got %s/%s", got[0].Severity, got[0].Kind)
	}
	if got[0].Details["size_bytes"].(int64) != int64(len(orphanBytes)) {
		t.Errorf("size_bytes mismatch: got %v", got[0].Details["size_bytes"])
	}
}

func TestCheckOrphanBlobs_NonCanonicalFilename(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		extraBlobs: []extraBlob{
			{name: "legacy-archive.tar.gz", contents: []byte("legacy")},
		},
	})
	got := checkOrphanBlobs(mustBuildState(t, fm))
	if len(got) != 1 {
		t.Fatalf("want 1; got %+v", got)
	}
	if !findingHasMessage(got, "non-canonical") {
		t.Errorf("want 'non-canonical' message; got %q", got[0].Message)
	}
}
