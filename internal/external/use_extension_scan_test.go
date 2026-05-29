package external_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/albertocavalcante/canopy/internal/external"
)

// ScanUseExtensions extracts use_extension declarations + their tag
// calls from a MODULE.bazel byte payload. Each returned site captures:
//   - the extension's .bzl label (e.g. "@rules_go//go:extensions.bzl")
//   - the extension's name within that .bzl (e.g. "go_sdk")
//   - the list of tag-class invocations attached to it, each with
//     attrs as a string-keyed map.
//
// Used by canopy ingest to build a cross-module index of
// "which canopy-indexed module uses which extension with which
// tag values" — the foundation of the consumer-corpus aggregation
// that fills in real tag values when re-driving a producer
// ruleset's module_extension impls.
func TestScanUseExtensions_BasicShape(t *testing.T) {
	const moduleBazel = `
module(name = "myapp", version = "1.0.0")
bazel_dep(name = "rules_go", version = "0.50.1")
bazel_dep(name = "rules_python", version = "0.34.0")

go_sdk = use_extension("@rules_go//go:extensions.bzl", "go_sdk")
go_sdk.download(version = "1.22.5")
go_sdk.download(version = "1.21.3", goos = "linux", goarch = "amd64")
use_repo(go_sdk, "go_default_sdk")

python = use_extension("@rules_python//python/extensions:python.bzl", "python")
python.toolchain(python_version = "3.11.6", is_default = True)
`
	sites, err := external.ScanUseExtensions([]byte(moduleBazel))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Sort by extension name for stable assertions.
	sort.Slice(sites, func(i, j int) bool {
		return sites[i].ExtensionName < sites[j].ExtensionName
	})
	if len(sites) != 2 {
		t.Fatalf("sites = %d (%+v), want 2", len(sites), sites)
	}

	goSdk := sites[0]
	if goSdk.ExtensionFile != "@rules_go//go:extensions.bzl" {
		t.Errorf("go_sdk.ExtensionFile = %q", goSdk.ExtensionFile)
	}
	if goSdk.ExtensionName != "go_sdk" {
		t.Errorf("go_sdk.ExtensionName = %q", goSdk.ExtensionName)
	}
	if len(goSdk.Tags) != 2 {
		t.Fatalf("go_sdk.Tags = %d (%+v), want 2", len(goSdk.Tags), goSdk.Tags)
	}
	// First tag: version="1.22.5"
	wantTag0 := map[string]any{"version": "1.22.5"}
	if !reflect.DeepEqual(goSdk.Tags[0].Attrs, wantTag0) {
		t.Errorf("go_sdk.Tags[0].Attrs = %+v, want %+v", goSdk.Tags[0].Attrs, wantTag0)
	}
	// Second tag: version + goos + goarch
	if goSdk.Tags[1].Attrs["version"] != "1.21.3" {
		t.Errorf("go_sdk.Tags[1].version = %v", goSdk.Tags[1].Attrs["version"])
	}
	if goSdk.Tags[1].Name != "download" {
		t.Errorf("go_sdk.Tags[1].Name = %q, want download", goSdk.Tags[1].Name)
	}

	python := sites[1]
	if python.ExtensionFile != "@rules_python//python/extensions:python.bzl" {
		t.Errorf("python.ExtensionFile = %q", python.ExtensionFile)
	}
	if len(python.Tags) != 1 || python.Tags[0].Name != "toolchain" {
		t.Errorf("python.Tags = %+v, want [{toolchain ...}]", python.Tags)
	}
}

// dev_dependency + isolate flags are preserved (they affect whether
// the resolver should expand this site's tag instances into
// ModuleSpec input — dev-only consumers shouldn't pollute the
// production URL surface).
func TestScanUseExtensions_DevDependencyAndIsolate(t *testing.T) {
	const moduleBazel = `
module(name = "x", version = "1.0")

# Dev-only extension.
testonly_ext = use_extension("@x//:t.bzl", "testonly_ext", dev_dependency = True)
testonly_ext.something(a = 1)

# Isolated extension.
iso_ext = use_extension("@x//:i.bzl", "iso_ext", isolate = True)
iso_ext.thing(b = 2)
`
	sites, err := external.ScanUseExtensions([]byte(moduleBazel))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("sites = %d", len(sites))
	}
	byName := map[string]external.UseExtensionSite{}
	for _, s := range sites {
		byName[s.ExtensionName] = s
	}
	if !byName["testonly_ext"].DevDependency {
		t.Error("testonly_ext should have DevDependency=true")
	}
	if byName["testonly_ext"].Isolate {
		t.Error("testonly_ext should have Isolate=false")
	}
	if byName["iso_ext"].DevDependency {
		t.Error("iso_ext should have DevDependency=false")
	}
	if !byName["iso_ext"].Isolate {
		t.Error("iso_ext should have Isolate=true")
	}
}

// Empty MODULE.bazel or one with no extensions returns an empty slice
// (not an error).
func TestScanUseExtensions_NoExtensionsReturnsEmpty(t *testing.T) {
	const moduleBazel = `
module(name = "x", version = "1.0")
bazel_dep(name = "rules_go", version = "0.50.1")
`
	sites, err := external.ScanUseExtensions([]byte(moduleBazel))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sites) != 0 {
		t.Errorf("sites = %d, want 0", len(sites))
	}
}

// Real-corpus regression check: scan every committed snapshot under
// testdata/use_extension_corpus and assert the scanner returns a
// plausible result (at least one site, all sites well-formed, at
// least one tag call across the file). The snapshots are real-world
// MODULE.bazel files (see testdata/use_extension_corpus/README.md);
// refresh them periodically to catch upstream syntax drift.
func TestScanUseExtensions_RealCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "use_extension_corpus")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}
	var ran int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".MODULE.bazel") {
			continue
		}
		ran++
		t.Run(e.Name(), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			sites, err := external.ScanUseExtensions(src)
			if err != nil {
				t.Fatalf("scan %s: %v", e.Name(), err)
			}
			if len(sites) == 0 {
				t.Fatalf("%s: expected ≥1 use_extension site (corpus is curated to have them)", e.Name())
			}
			var tagCount int
			for _, s := range sites {
				tagCount += len(s.Tags)
				if s.ExtensionFile == "" || s.ExtensionName == "" {
					t.Errorf("%s: malformed site: %+v", e.Name(), s)
				}
				// Bazel labels: either "@repo//pkg:target", "//pkg:target"
				// (same-repo), or ":target" (same-package). All three are
				// valid for use_extension's first arg.
				if !strings.ContainsAny(s.ExtensionFile, "@/") {
					t.Errorf("%s: ExtensionFile %q not a label", e.Name(), s.ExtensionFile)
				}
			}
			if tagCount == 0 {
				t.Errorf("%s: expected ≥1 tag call across sites", e.Name())
			}
			t.Logf("%s: %d sites, %d tag calls", e.Name(), len(sites), tagCount)
		})
	}
	if ran == 0 {
		t.Fatalf("no *.MODULE.bazel files in %s — corpus directory empty?", dir)
	}
}

// Parse error on malformed input — return error, not garbage.
func TestScanUseExtensions_ParseErrorPropagates(t *testing.T) {
	const moduleBazel = `module( ` // truncated
	_, err := external.ScanUseExtensions([]byte(moduleBazel))
	if err == nil {
		t.Error("expected parse error for malformed MODULE.bazel")
	}
}
