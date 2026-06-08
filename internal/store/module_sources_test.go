package store_test

import (
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// newSourcesTestStore returns a Store backed by a fresh on-disk SQLite
// — module_sources behavior depends on the schema CHECK constraint,
// so :memory: would still exercise it but on-disk lets us debug the
// table directly when something's off.
func newSourcesTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ----- LogModuleSource -----

// TestLogModuleSource_InsertIgnoreDedupesByPK: the PK is
// (module, version, source_url) so repeated logs with the same tuple
// are no-ops (INSERT OR IGNORE). Different source_kinds with the same
// PK also get deduped — what was written first wins. That matches the
// Plan 16 design: a (m, v, source_url) tuple is one provenance fact,
// not one fact per kind.
func TestLogModuleSource_InsertIgnoreDedupesByPK(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)

	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "https://up.example/r", store.SourceHTTPUpstream); err != nil {
		t.Fatalf("first log: %v", err)
	}
	// Same PK with a different kind — should be ignored, original
	// row survives.
	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "https://up.example/r", store.SourceCollisionShadowed); err != nil {
		t.Fatalf("dup log: %v", err)
	}
	// Different source_url — different PK; new row lands.
	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "https://other.example/r", store.SourceCollisionShadowed); err != nil {
		t.Fatalf("other-url log: %v", err)
	}

	// One collision visible: (m, v) with at least one shadowed row.
	count, err := s.GetCollisionsCount(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("collision count = %d, want 1 — the (foo, 1.0.0) pair has a shadow", count)
	}
}

func TestLogModuleSource_RejectsEmptyArgs(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)
	cases := []struct {
		name, module, version, url string
	}{
		{"empty module", "", "1.0.0", "u"},
		{"empty version", "m", "", "u"},
		{"empty url", "m", "1.0.0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.LogModuleSource(ctx, tc.module, tc.version, tc.url, store.SourceLocal)
			if err == nil {
				t.Errorf("expected error on %s", tc.name)
			}
		})
	}
}

// TestLogModuleSource_BadKindSilentlyDropped: schema's CHECK constraint
// on source_kind blocks unknown values, but the impl uses INSERT OR
// IGNORE so the violation is swallowed (not raised). That's fine in
// practice — callers pass typed constants — but pin the behavior so a
// future "raise on CHECK violation" change is a deliberate decision,
// not a silent regression.
func TestLogModuleSource_BadKindSilentlyDropped(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)
	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "u", store.ModuleSourceKind("bogus-kind")); err != nil {
		t.Errorf("INSERT OR IGNORE should swallow CHECK violations, got %v", err)
	}
	// And the row must NOT be in the table.
	count, err := s.GetCollisionsCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("bogus-kind row landed in table: count=%d", count)
	}
}

// ----- GetCollisionsCount -----

func TestGetCollisionsCount_EmptyStore(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)
	count, err := s.GetCollisionsCount(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("empty store count = %d, want 0", count)
	}
}

// TestGetCollisionsCount_DistinctMVAcrossManyShadows: count is
// (DISTINCT module_name || '@' || version), so a single (m, v) with
// multiple shadow rows counts ONCE.
func TestGetCollisionsCount_DistinctMVAcrossManyShadows(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)

	// foo@1.0.0 shadowed by two upstreams
	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "https://u1.example/r", store.SourceCollisionShadowed); err != nil {
		t.Fatal(err)
	}
	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "https://u2.example/r", store.SourceCollisionShadowed); err != nil {
		t.Fatal(err)
	}
	// bar@2.0.0 shadowed by one upstream
	if err := s.LogModuleSource(ctx, "bar", "2.0.0", "https://u1.example/r", store.SourceCollisionShadowed); err != nil {
		t.Fatal(err)
	}
	// baz@3.0.0 with NO shadow (only local) — must not bump count.
	if err := s.LogModuleSource(ctx, "baz", "3.0.0", "local", store.SourceLocal); err != nil {
		t.Fatal(err)
	}

	count, err := s.GetCollisionsCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("collision count = %d, want 2 (foo@1.0.0 and bar@2.0.0)", count)
	}
}

// ----- GetCollisionsSample -----

func TestGetCollisionsSample_EmptyStore(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)
	sample, err := s.GetCollisionsSample(ctx, 10)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(sample) != 0 {
		t.Errorf("empty store sample length = %d, want 0", len(sample))
	}
}

// TestGetCollisionsSample_ShapeAndOrder: sample groups by (m, v),
// returns ServedFrom (the non-shadowed row) + Shadowed list (every
// collision-shadowed row).
func TestGetCollisionsSample_ShapeAndOrder(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)

	// Set up: rules_python@1.0.0 served from local, shadowed by BCR.
	if err := s.LogModuleSource(ctx, "rules_python", "1.0.0", "local", store.SourceLocal); err != nil {
		t.Fatal(err)
	}
	if err := s.LogModuleSource(ctx, "rules_python", "1.0.0", "https://bcr.bazel.build", store.SourceCollisionShadowed); err != nil {
		t.Fatal(err)
	}

	sample, err := s.GetCollisionsSample(ctx, 10)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(sample) != 1 {
		t.Fatalf("sample len = %d, want 1", len(sample))
	}
	got := sample[0]
	if got.Module != "rules_python" || got.Version != "1.0.0" {
		t.Errorf("module@version = %s@%s, want rules_python@1.0.0", got.Module, got.Version)
	}
	if got.ServedFrom != "local" {
		t.Errorf("ServedFrom = %q, want local", got.ServedFrom)
	}
	if len(got.Shadowed) != 1 || got.Shadowed[0] != "https://bcr.bazel.build" {
		t.Errorf("Shadowed = %v, want [https://bcr.bazel.build]", got.Shadowed)
	}
	if got.LastSeen == "" {
		t.Error("LastSeen should be populated")
	}
}

// TestGetCollisionsSample_LimitRespected: limit bounds the number of
// (m, v) groups returned.
func TestGetCollisionsSample_LimitRespected(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)

	// Seed three colliding (m, v) pairs.
	for _, mv := range []struct{ m, v string }{
		{"a", "1.0.0"},
		{"b", "1.0.0"},
		{"c", "1.0.0"},
	} {
		if err := s.LogModuleSource(ctx, mv.m, mv.v, "local", store.SourceLocal); err != nil {
			t.Fatal(err)
		}
		if err := s.LogModuleSource(ctx, mv.m, mv.v, "https://up.example/r", store.SourceCollisionShadowed); err != nil {
			t.Fatal(err)
		}
	}

	sample, err := s.GetCollisionsSample(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample) != 2 {
		t.Errorf("sample with limit=2 returned %d entries, want 2", len(sample))
	}
}

// TestGetCollisionsSample_MultipleShadowsPerPair: a single (m, v)
// shadowed by N upstreams collapses into one entry with N Shadowed
// URLs.
func TestGetCollisionsSample_MultipleShadowsPerPair(t *testing.T) {
	ctx := t.Context()
	s := newSourcesTestStore(t)

	if err := s.LogModuleSource(ctx, "foo", "1.0.0", "local", store.SourceLocal); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"https://u1.example/r",
		"https://u2.example/r",
		"https://u3.example/r",
	} {
		if err := s.LogModuleSource(ctx, "foo", "1.0.0", u, store.SourceCollisionShadowed); err != nil {
			t.Fatal(err)
		}
	}

	sample, err := s.GetCollisionsSample(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sample) != 1 {
		t.Fatalf("sample len = %d, want 1 (one (m, v) group)", len(sample))
	}
	if len(sample[0].Shadowed) != 3 {
		t.Errorf("Shadowed len = %d, want 3", len(sample[0].Shadowed))
	}
}
