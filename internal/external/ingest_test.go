package external_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/external"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// End-to-end: synthesize a workspace with a single .bzl that defines a
// repository_rule with a literal download URL. IngestModule should
// populate external_refs.
func TestIngestModule_LiteralURL_PopulatesStore(t *testing.T) {
	ctx := t.Context()

	dir := t.TempDir()
	bzl := `
def _impl(ctx):
    ctx.download(url = "https://example.com/foo.tar.gz", sha256 = "deadbeef")

my_repo = repository_rule(implementation = _impl)
`
	if err := os.WriteFile(filepath.Join(dir, "defs.bzl"), []byte(bzl), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Seed the parent (module, version) row that external_refs FK refers to.
	rep := &report.ModuleReport{Name: "fixture", Version: "1.0.0"}
	if err := s.WriteReport(ctx, rep); err != nil {
		t.Fatalf("seed report: %v", err)
	}

	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	refs, err := s.GetExternalRefs(ctx, "fixture", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %d (%+v), want 1", len(refs), refs)
	}
	if refs[0].URL != "https://example.com/foo.tar.gz" {
		t.Errorf("url = %q", refs[0].URL)
	}
	if refs[0].SHA256 != "deadbeef" {
		t.Errorf("sha256 = %q", refs[0].SHA256)
	}
	if refs[0].RuleName != "my_repo" {
		t.Errorf("rule_name = %q, want my_repo", refs[0].RuleName)
	}
	if refs[0].Mutability != "immutable" {
		t.Errorf("mutability = %q, want immutable (sha256 pinned)", refs[0].Mutability)
	}
}

// Filtering: when the report's RepositoryRules + ModuleExtensions
// point at a subset of the workspace's .bzl files, Analyze only
// evaluates those — files outside the list are skipped even if they
// contain repository_rule definitions.
func TestIngestModule_RelevantFilesFilter(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	mustWrite := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("relevant.bzl", `
def _impl(ctx):
    ctx.download(url = "https://example.com/RELEVANT.tar.gz", sha256 = "abc")
keep = repository_rule(implementation = _impl)
`)
	mustWrite("skipped.bzl", `
def _impl(ctx):
    ctx.download(url = "https://example.com/SKIPPED.tar.gz", sha256 = "xyz")
drop = repository_rule(implementation = _impl)
`)

	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Report lists ONLY relevant.bzl as a rule-bearing file.
	rep := &report.ModuleReport{
		Name: "m", Version: "1",
		RepositoryRules: []report.RepoRuleSpec{
			{Name: "keep", Provenance: report.Provenance{File: "relevant.bzl"}},
		},
	}
	if err := s.WriteReport(ctx, rep); err != nil {
		t.Fatal(err)
	}

	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	refs, err := s.GetExternalRefs(ctx, "m", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %d (%+v), want exactly the relevant URL", len(refs), refs)
	}
	if !strings.Contains(refs[0].URL, "RELEVANT") {
		t.Errorf("captured wrong URL — RelevantFiles filter didn't work: %q", refs[0].URL)
	}
	for _, r := range refs {
		if strings.Contains(r.URL, "SKIPPED") {
			t.Errorf("skipped.bzl was evaluated despite not being in RelevantFiles: %q", r.URL)
		}
	}
}

// Ingest also captures use_extension call sites from MODULE.bazel into
// the cross-module corpus index. Round-trip via the store's query API.
func TestIngestModule_CapturesUseExtensionUsages(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	const moduleBazel = `
module(name = "myapp", version = "1.0.0")
bazel_dep(name = "rules_go", version = "0.50.1")

go_sdk = use_extension("@rules_go//go:extensions.bzl", "go_sdk")
go_sdk.download(version = "1.22.5")
go_sdk.download(version = "1.21.3", goos = "linux", goarch = "amd64")
`
	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"), []byte(moduleBazel), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rep := &report.ModuleReport{Name: "myapp", Version: "1.0.0"}
	if err := s.WriteReport(ctx, rep); err != nil {
		t.Fatal(err)
	}
	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	usages, err := s.GetUseExtensionUsagesForExtension(ctx,
		"@rules_go//go:extensions.bzl", "go_sdk")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 2 {
		t.Fatalf("usages = %d (%+v), want 2 tag calls captured", len(usages), usages)
	}
	if usages[0].ConsumerModule != "myapp" {
		t.Errorf("ConsumerModule = %q", usages[0].ConsumerModule)
	}
	if usages[0].TagName != "download" {
		t.Errorf("TagName = %q", usages[0].TagName)
	}
	// TagAttrsJSON should round-trip the version field.
	if !strings.Contains(usages[0].TagAttrsJSON, `"version"`) {
		t.Errorf("TagAttrsJSON missing version: %q", usages[0].TagAttrsJSON)
	}
}

// Ingest captures the .bzl source of every file declaring a
// module_extension into the store, so query-time re-drive doesn't
// need access to the original tarball.
func TestIngestModule_CapturesModuleExtensionSources(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	// Producer ruleset declares an extension in go/extensions.bzl.
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"),
		[]byte(`module(name = "rules_go", version = "0.50.1")`), 0o644); err != nil {
		t.Fatal(err)
	}
	const extBzl = `def _impl(ctx):
    pass
go_sdk = module_extension(implementation = _impl)
`
	if err := os.WriteFile(filepath.Join(dir, "go", "extensions.bzl"), []byte(extBzl), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Synthesize a report that mirrors what assay would produce — we
	// only need ModuleExtensions to have one entry pointing at the file.
	rep := &report.ModuleReport{
		Name: "rules_go", Version: "0.50.1",
		ModuleExtensions: []report.ModuleExtSpec{{
			Name:       "go_sdk",
			Provenance: report.Provenance{File: "go/extensions.bzl"},
		}},
	}
	if err := s.WriteReport(ctx, rep); err != nil {
		t.Fatal(err)
	}
	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	srcs, err := s.GetModuleExtensionSources(ctx, "rules_go", "0.50.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 {
		t.Fatalf("sources = %d (%+v), want 1", len(srcs), srcs)
	}
	if srcs[0].File != "go/extensions.bzl" {
		t.Errorf("file = %q", srcs[0].File)
	}
	if !strings.Contains(string(srcs[0].Content), "go_sdk = module_extension") {
		t.Errorf("content roundtrip failed: %s", srcs[0].Content)
	}
}

// Path-traversal guard: a producer-supplied Provenance.File pointing
// outside moduleDir (e.g. "../../etc/passwd") must NOT be read.
func TestIngestModule_RejectsNonLocalExtensionPath(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	// Create a "secret" file outside the moduleDir.
	parent := filepath.Dir(dir)
	secretPath := filepath.Join(parent, "secret-outside.bzl")
	if err := os.WriteFile(secretPath, []byte("# evil content"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })

	if err := os.WriteFile(filepath.Join(dir, "MODULE.bazel"),
		[]byte(`module(name = "m", version = "1")`), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Report claims an extension at a path that escapes moduleDir.
	rep := &report.ModuleReport{
		Name: "m", Version: "1",
		ModuleExtensions: []report.ModuleExtSpec{{
			Name:       "evil_ext",
			Provenance: report.Provenance{File: "../secret-outside.bzl"},
		}},
	}
	if err := s.WriteReport(ctx, rep); err != nil {
		t.Fatal(err)
	}
	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	srcs, _ := s.GetModuleExtensionSources(ctx, "m", "1")
	for _, src := range srcs {
		if strings.Contains(string(src.Content), "evil content") {
			t.Errorf("path-traversal succeeded — file outside moduleDir was read: %s", src.File)
		}
	}
	if len(srcs) != 0 {
		t.Errorf("expected zero stored sources (path was non-local), got %d", len(srcs))
	}
}

// Re-ingest with different .bzl content wipes prior rows.
func TestIngestModule_ReIngestReplaces(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	mk := func(url string) {
		bzl := `
def _impl(ctx):
    ctx.download(url = "` + url + `", sha256 = "x")

my_repo = repository_rule(implementation = _impl)
`
		if err := os.WriteFile(filepath.Join(dir, "defs.bzl"), []byte(bzl), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rep := &report.ModuleReport{Name: "m", Version: "1"}
	if err := s.WriteReport(ctx, rep); err != nil {
		t.Fatal(err)
	}

	mk("https://old.example.com/")
	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatal(err)
	}
	mk("https://new.example.com/")
	if err := external.IngestModule(ctx, s, dir, rep); err != nil {
		t.Fatal(err)
	}

	refs, _ := s.GetExternalRefs(ctx, "m", "1")
	if len(refs) != 1 || refs[0].Host != "new.example.com" {
		t.Errorf("re-ingest didn't replace: %+v", refs)
	}
}
