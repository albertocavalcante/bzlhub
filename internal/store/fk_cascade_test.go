package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// The schema.sql FK on module_extension_usages references
// versions(module_name, version) positionally — the LOCAL columns are
// renamed to consumer_module/consumer_version for readability. The
// comment above the FK in schema.sql claims SQLite matches by
// position; this test verifies the cascade actually fires, so the
// rename doesn't silently break referential integrity.
//
// Lives in package store (not store_test) because it needs raw DB
// access to issue the parent-row DELETE.
func TestModuleExtensionUsages_FKCascadeOnVersionDelete(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "myapp", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "myapp", "1.0.0", []UseExtensionUsage{
		{ExtensionFile: "@x//:e.bzl", ExtensionName: "ext", TagIndex: 0, TagName: "t", TagAttrsJSON: `{}`},
	}); err != nil {
		t.Fatal(err)
	}

	// Pre-condition: row is there.
	pre, _ := s.GetUseExtensionUsagesForExtension(ctx, "@x//:e.bzl", "ext")
	if len(pre) != 1 {
		t.Fatalf("setup: pre-delete usage count = %d, want 1", len(pre))
	}

	// Delete the parent versions row. The renamed-column FK must
	// cascade to module_extension_usages.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM versions WHERE module_name = ? AND version = ?`,
		"myapp", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	post, _ := s.GetUseExtensionUsagesForExtension(ctx, "@x//:e.bzl", "ext")
	if len(post) != 0 {
		t.Errorf("FK cascade broken: post-delete usage count = %d, want 0", len(post))
	}
}
