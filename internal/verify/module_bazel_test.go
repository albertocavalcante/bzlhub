package verify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckModuleBazelPresent_Good(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	got := checkModuleBazelPresent(mustBuildState(t, fm))
	if len(got) != 0 {
		t.Fatalf("want 0; got %d: %+v", len(got), got)
	}
}

func TestCheckModuleBazelPresent_Missing(t *testing.T) {
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body")},
		},
	})
	// Remove the auto-generated MODULE.bazel to simulate the "missing" case.
	must(t, os.Remove(filepath.Join(fm.root, "modules", "foo", "1.0.0", "MODULE.bazel")))

	got := checkModuleBazelPresent(mustBuildState(t, fm))
	if len(got) != 1 || got[0].Kind != KindModuleBazelPresent || got[0].Severity != SevError {
		t.Fatalf("want 1 error finding; got %+v", got)
	}
}

func TestCheckModuleBazelPresent_Unparseable(t *testing.T) {
	garbage := "this is not a valid MODULE.bazel\xff\x00\x01"
	fm := buildFakeMirror(t, mirrorLayout{
		modules: []moduleSpec{
			{name: "foo", version: "1.0.0", blobBytes: []byte("body"), moduleBazel: stringPtr(garbage)},
		},
	})
	got := checkModuleBazelPresent(mustBuildState(t, fm))
	if len(got) != 1 || got[0].Kind != KindModuleBazelPresent {
		t.Fatalf("want 1 finding; got %+v", got)
	}
	if !findingHasMessage(got, "does not parse") {
		t.Errorf("want 'does not parse' message; got %q", got[0].Message)
	}
}
