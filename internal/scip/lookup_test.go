package scip

import (
	"context"
	"errors"
	"testing"

	scip "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// scipBlobForRef builds a SCIP index where `symbol` is referenced once
// in `file` (as a non-definition occurrence) plus optionally a
// definition occurrence in the same document.
func scipBlobForRef(t *testing.T, file, symbol string, includeDef bool) []byte {
	t.Helper()
	occs := []*scip.Occurrence{{
		Symbol:      symbol,
		Range:       []int32{10, 5, 10, 15},
		SymbolRoles: 0,
	}}
	if includeDef {
		occs = append(occs, &scip.Occurrence{
			Symbol:      symbol,
			Range:       []int32{1, 0, 1, 10},
			SymbolRoles: int32(scip.SymbolRole_Definition),
		})
	}
	idx := &scip.Index{
		Metadata: &scip.Metadata{Version: 0},
		Documents: []*scip.Document{{
			RelativePath: file,
			Occurrences:  occs,
		}},
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// fakeXRefStore implements both BlobReader and XRefsLister with an
// in-memory map. Tests construct it with the (module, version) → blob
// fixtures they want.
type fakeXRefStore struct {
	versions []ModuleVersion
	blobs    map[string][]byte // key: module+"@"+version
	// brokenKeys: when GetScipBlob is called for these, return a parse-
	// unsafe byte stream so we can exercise the "skip broken index" path.
	brokenKeys map[string]bool
}

func (f *fakeXRefStore) ListScipVersions(_ context.Context) ([]ModuleVersion, error) {
	return f.versions, nil
}

func (f *fakeXRefStore) GetScipBlob(_ context.Context, m, v string) ([]byte, error) {
	key := m + "@" + v
	if f.brokenKeys[key] {
		return []byte{0xff, 0xff, 0xff, 0xff}, nil
	}
	b, ok := f.blobs[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return b, nil
}

func TestLookupXRefs_NoIndexes(t *testing.T) {
	store := &fakeXRefStore{versions: nil, blobs: map[string][]byte{}}
	got, err := LookupXRefs(context.Background(), store, store, "bzlmod foo@1.0 a.bzl#x", false)
	if err != nil {
		t.Fatalf("LookupXRefs: %v", err)
	}
	if got.Count != 0 || len(got.Groups) != 0 {
		t.Errorf("expected empty result, got count=%d groups=%v", got.Count, got.Groups)
	}
}

func TestLookupXRefs_OneMatch(t *testing.T) {
	symbol := "bzlmod bazel_skylib@1.7.1 lib/paths.bzl#paths"
	store := &fakeXRefStore{
		versions: []ModuleVersion{
			{"bar", "2.0"},          // has the symbol
			{"unrelated", "0.0.1"}, // doesn't
		},
		blobs: map[string][]byte{
			"bar@2.0":           scipBlobForRef(t, "bar/use.bzl", symbol, false),
			"unrelated@0.0.1": scipBlobForRef(t, "u.bzl", "bzlmod other 1.0 x.bzl#x", false),
		},
	}
	got, err := LookupXRefs(context.Background(), store, store, symbol, false)
	if err != nil {
		t.Fatalf("LookupXRefs: %v", err)
	}
	if got.Count != 1 {
		t.Errorf("count = %d, want 1", got.Count)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(got.Groups))
	}
	g := got.Groups[0]
	if g.Module != "bar" || g.Version != "2.0" {
		t.Errorf("group=(%s,%s); want (bar,2.0)", g.Module, g.Version)
	}
	if len(g.References) != 1 || g.References[0].File != "bar/use.bzl" {
		t.Errorf("group refs = %v", g.References)
	}
}

func TestLookupXRefs_GroupsSortedByModuleThenVersion(t *testing.T) {
	symbol := "bzlmod skylib@1 lib/paths.bzl#paths"
	store := &fakeXRefStore{
		versions: []ModuleVersion{
			{"zebra", "1.0"},
			{"apple", "2.0"},
			{"apple", "1.0"},
		},
		blobs: map[string][]byte{
			"zebra@1.0": scipBlobForRef(t, "z.bzl", symbol, false),
			"apple@1.0": scipBlobForRef(t, "a1.bzl", symbol, false),
			"apple@2.0": scipBlobForRef(t, "a2.bzl", symbol, false),
		},
	}
	got, err := LookupXRefs(context.Background(), store, store, symbol, false)
	if err != nil {
		t.Fatalf("LookupXRefs: %v", err)
	}
	if got.Count != 3 {
		t.Errorf("count=%d, want 3", got.Count)
	}
	want := []struct{ m, v string }{
		{"apple", "1.0"},
		{"apple", "2.0"},
		{"zebra", "1.0"},
	}
	if len(got.Groups) != len(want) {
		t.Fatalf("groups=%d, want %d", len(got.Groups), len(want))
	}
	for i, g := range got.Groups {
		if g.Module != want[i].m || g.Version != want[i].v {
			t.Errorf("groups[%d]=(%s,%s); want (%s,%s)", i, g.Module, g.Version, want[i].m, want[i].v)
		}
	}
}

func TestLookupXRefs_ExcludesDefinitionByDefault(t *testing.T) {
	// The owning module's blob has BOTH a def occurrence and a ref
	// occurrence at different positions for the same symbol. With
	// includeDefinition=false, only the ref should surface.
	symbol := "bzlmod own@1.0 own.bzl#thing"
	store := &fakeXRefStore{
		versions: []ModuleVersion{{"own", "1.0"}},
		blobs: map[string][]byte{
			"own@1.0": scipBlobForRef(t, "own.bzl", symbol, true),
		},
	}
	got, err := LookupXRefs(context.Background(), store, store, symbol, false)
	if err != nil {
		t.Fatalf("LookupXRefs: %v", err)
	}
	if got.Count != 1 {
		t.Errorf("count=%d, want 1 (def excluded)", got.Count)
	}
}

func TestLookupXRefs_IncludesDefinitionWhenAsked(t *testing.T) {
	symbol := "bzlmod own@1.0 own.bzl#thing"
	store := &fakeXRefStore{
		versions: []ModuleVersion{{"own", "1.0"}},
		blobs: map[string][]byte{
			"own@1.0": scipBlobForRef(t, "own.bzl", symbol, true),
		},
	}
	got, err := LookupXRefs(context.Background(), store, store, symbol, true)
	if err != nil {
		t.Fatalf("LookupXRefs: %v", err)
	}
	if got.Count != 2 {
		t.Errorf("count=%d, want 2 (def + ref)", got.Count)
	}
}

func TestLookupXRefs_SkipsBrokenIndex(t *testing.T) {
	// One module's blob is corrupt. The walk must NOT abort — broken
	// indexes are operationally normal (ingest race, partial write) and
	// the consumer would rather see partial results than nothing.
	symbol := "bzlmod skylib@1.7.1 lib/paths.bzl#paths"
	store := &fakeXRefStore{
		versions: []ModuleVersion{
			{"broken", "1.0"},
			{"good", "1.0"},
		},
		blobs: map[string][]byte{
			"good@1.0": scipBlobForRef(t, "g.bzl", symbol, false),
		},
		brokenKeys: map[string]bool{"broken@1.0": true},
	}
	got, err := LookupXRefs(context.Background(), store, store, symbol, false)
	if err != nil {
		t.Fatalf("LookupXRefs: %v", err)
	}
	if got.Count != 1 || len(got.Groups) != 1 || got.Groups[0].Module != "good" {
		t.Errorf("expected one group from good@1.0, got %+v", got)
	}
	if got.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (broken@1.0 should be counted)", got.Skipped)
	}
}

func TestLookupXRefs_RejectsEmptySymbol(t *testing.T) {
	store := &fakeXRefStore{}
	_, err := LookupXRefs(context.Background(), store, store, "", false)
	if err == nil {
		t.Error("LookupXRefs(symbol=\"\") = nil err; want validation error")
	}
}
