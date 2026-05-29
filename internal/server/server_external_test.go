package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/api/paths"
	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/server"
	"github.com/albertocavalcante/canopy/internal/store"
)

func TestExternalSurface_EndpointReturnsRefs(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Seed the (module, version) parent + a couple of refs.
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "fixture", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "fixture", "1.0.0",
		[]store.ExternalRef{
			{URL: "https://dl.google.com/go/x.tar.gz", Host: "dl.google.com", Class: "vendor-http", Mutability: "immutable", SHA256: "abc", RuleName: "go_sdk", Platform: "linux/amd64", File: "go.bzl"},
			{URL: "https://github.com/foo/bar/archive/v1.tar.gz", Host: "github.com", Class: "github-archive", Mutability: "mutable-host", RuleName: "foobar", Platform: "any", File: "deps.bzl"},
		},
		[]store.ExternalForkError{
			{Platform: "windows/amd64", Message: "not supported"},
		},
	); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.External("fixture", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", res.StatusCode, body)
	}

	var got api.ExternalSurfaceResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if got.Module != "fixture" || got.Version != "1.0.0" {
		t.Errorf("module/version = %q/%q", got.Module, got.Version)
	}
	if len(got.Refs) != 2 {
		t.Fatalf("refs = %d (%v), want 2", len(got.Refs), got.Refs)
	}
	if got.Refs[0].Class != "github-archive" {
		t.Errorf("first class = %q, want github-archive (sort)", got.Refs[0].Class)
	}
	if got.ClassCounts["github-archive"] != 1 || got.ClassCounts["vendor-http"] != 1 {
		t.Errorf("class_counts = %v, want vendor-http=1 github-archive=1", got.ClassCounts)
	}
	if len(got.ForkErrors) != 1 || got.ForkErrors[0].Platform != "windows/amd64" {
		t.Errorf("fork_errors = %v", got.ForkErrors)
	}
}

// Plan 14 Layer 1: filter query params on the external endpoint.
// The UI's URL-bound class/host/tainted/mutability filters must be
// curl-equivalent — sharing a URL with `?class=github-archive` and
// re-running it via curl returns only github-archive refs.
func TestExternalSurface_FilterByQueryParams(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://github.com/x/y.tar.gz", Host: "github.com", Class: "github-archive", Mutability: "mutable-host", Platform: "any"},
		{URL: "https://github.com/a/b.tar.gz", Host: "github.com", Class: "github-archive", Mutability: "mutable-host", Platform: "any"},
		{URL: "https://dl.google.com/go/x.tar.gz", Host: "dl.google.com", Class: "vendor-http", Mutability: "immutable", Platform: "any"},
		{URL: "https://opaque/foo", Host: "opaque", Class: "unknown", Mutability: "unknown", Platform: "any", Tainted: true},
	}, nil); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	cases := []struct {
		name      string
		query     string
		wantCount int
	}{
		{"no filter", "", 4},
		{"class single", "class=github-archive", 2},
		{"class multiple", "class=github-archive,vendor-http", 3},
		{"host single", "host=dl.google.com", 1},
		{"mutability filter", "mutability=immutable", 1},
		{"tainted=only", "tainted=only", 1},
		{"tainted=exclude", "tainted=exclude", 3},
		{"combined class+tainted", "class=github-archive&tainted=exclude", 2},
		{"invalid tainted value tolerated", "tainted=banana", 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := ts.URL + paths.External("m", "1")
			if c.query != "" {
				url += "?" + c.query
			}
			res, err := http.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status %d, body=%s", res.StatusCode, body)
			}
			var got api.ExternalSurfaceResponse
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal: %v body=%s", err, body)
			}
			if len(got.Refs) != c.wantCount {
				t.Errorf("got %d refs, want %d (URLs: %v)", len(got.Refs), c.wantCount, refURLs(got.Refs))
			}
			// ClassCounts must be consistent with the filtered Refs.
			recomputed := map[string]int{}
			for _, r := range got.Refs {
				if r.Class != "" {
					recomputed[r.Class]++
				}
			}
			for cls, n := range recomputed {
				if got.ClassCounts[cls] != n {
					t.Errorf("class_counts[%q] = %d, want %d", cls, got.ClassCounts[cls], n)
				}
			}
		})
	}
}

func refURLs(refs []api.ExternalRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.URL
	}
	return out
}

// Per-module provenance + ?module= filter on the closure endpoint.
// Each ref in the closure surface should carry SourceModule populated
// from the closure walker (first-seen-wins under dedupe); ?module=
// then filters the response to refs contributed by named closure
// nodes. Accepts both fully-qualified "name@version" and bare "name"
// for ergonomics.
func TestAirgapSurface_PerModuleProvenance(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	leaf := &report.ModuleReport{Name: "leaf", Version: "1.0.0"}
	if err := s.WriteReport(ctx, leaf); err != nil {
		t.Fatal(err)
	}
	root := &report.ModuleReport{
		Name: "root", Version: "1.0.0",
		BazelDeps: []report.ModuleKey{{Name: "leaf", Version: "1.0.0"}},
	}
	if err := s.WriteReport(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "leaf", "1.0.0", []store.ExternalRef{
		{URL: "https://leaf.example/a.tar.gz", Host: "leaf.example", Class: "vendor-http", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "root", "1.0.0", []store.ExternalRef{
		{URL: "https://root.example/b.tar.gz", Host: "root.example", Class: "vendor-http", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	t.Run("provenance populated", func(t *testing.T) {
		res, err := http.Get(ts.URL + paths.AirgapSurface("root", "1.0.0"))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var got api.ClosureSurfaceResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal: %v body=%s", err, body)
		}
		if len(got.Refs) != 2 {
			t.Fatalf("refs = %d, want 2", len(got.Refs))
		}
		sources := map[string]string{}
		for _, r := range got.Refs {
			sources[r.URL] = r.SourceModule
		}
		if sources["https://leaf.example/a.tar.gz"] != "leaf@1.0.0" {
			t.Errorf("leaf ref source = %q, want leaf@1.0.0", sources["https://leaf.example/a.tar.gz"])
		}
		if sources["https://root.example/b.tar.gz"] != "root@1.0.0" {
			t.Errorf("root ref source = %q, want root@1.0.0", sources["https://root.example/b.tar.gz"])
		}
	})

	t.Run("module filter (bare name)", func(t *testing.T) {
		res, err := http.Get(ts.URL + paths.AirgapSurface("root", "1.0.0") + "?module=leaf")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var got api.ClosureSurfaceResponse
		_ = json.Unmarshal(body, &got)
		if len(got.Refs) != 1 || got.Refs[0].URL != "https://leaf.example/a.tar.gz" {
			t.Errorf("got %d refs (%v), want only the leaf ref", len(got.Refs), refURLs(got.Refs))
		}
	})

	t.Run("module filter (qualified name)", func(t *testing.T) {
		res, err := http.Get(ts.URL + paths.AirgapSurface("root", "1.0.0") + "?module=root@1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var got api.ClosureSurfaceResponse
		_ = json.Unmarshal(body, &got)
		if len(got.Refs) != 1 || got.Refs[0].URL != "https://root.example/b.tar.gz" {
			t.Errorf("got %d refs (%v), want only the root ref", len(got.Refs), refURLs(got.Refs))
		}
	})

	t.Run("module filter combines with class", func(t *testing.T) {
		// Both refs are class=vendor-http, so class filter alone keeps
		// both; module=leaf narrows to one.
		res, err := http.Get(ts.URL + paths.AirgapSurface("root", "1.0.0") + "?class=vendor-http&module=leaf")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var got api.ClosureSurfaceResponse
		_ = json.Unmarshal(body, &got)
		if len(got.Refs) != 1 {
			t.Errorf("class+module combined: got %d refs, want 1", len(got.Refs))
		}
	})
}

// Closure-wide aggregation: a root module bazel_dep's a leaf module;
// both have external refs; the airgap-surface endpoint returns the
// union across the closure with per-module counts + a single dedup'd
// ref list.
func TestAirgapSurface_ClosureUnion(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Seed two modules. Root depends on leaf via bazel_dep.
	leaf := &report.ModuleReport{Name: "leaf", Version: "1.0.0"}
	if err := s.WriteReport(ctx, leaf); err != nil {
		t.Fatal(err)
	}
	root := &report.ModuleReport{
		Name: "root", Version: "1.0.0",
		BazelDeps: []report.ModuleKey{{Name: "leaf", Version: "1.0.0"}},
	}
	if err := s.WriteReport(ctx, root); err != nil {
		t.Fatal(err)
	}

	if err := s.WriteExternalRefs(ctx, "leaf", "1.0.0", []store.ExternalRef{
		{URL: "https://example.com/leaf.tar.gz", Host: "example.com", Class: "vendor-http", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "root", "1.0.0", []store.ExternalRef{
		{URL: "https://example.com/root.tar.gz", Host: "example.com", Class: "vendor-http", Platform: "any"},
		// Duplicate of the leaf URL — should dedup.
		{URL: "https://example.com/leaf.tar.gz", Host: "example.com", Class: "vendor-http", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.AirgapSurface("root", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", res.StatusCode, body)
	}

	var got api.ClosureSurfaceResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if got.Root != "root@1.0.0" {
		t.Errorf("root = %q, want root@1.0.0", got.Root)
	}
	if len(got.Modules) != 2 {
		t.Errorf("modules = %d (%+v), want 2 (root+leaf)", len(got.Modules), got.Modules)
	}
	if len(got.Refs) != 2 {
		t.Errorf("refs = %d (%+v), want 2 (deduplicated across closure)", len(got.Refs), got.Refs)
	}
	if got.ClassCounts["vendor-http"] != 2 {
		t.Errorf("class_counts.vendor-http = %d, want 2", got.ClassCounts["vendor-http"])
	}
}

// Downloader-config emitter: produces a text payload with one
// `rewrite` line per unique source host, plus the metadata header
// and the `allow <mirror>` line.
func TestAirgapDownloaderConfig_RendersRewriteLines(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://github.com/x/y.tar.gz", Host: "github.com", Class: "github-archive", Platform: "any"},
		{URL: "https://dl.google.com/go/go.tar.gz", Host: "dl.google.com", Class: "vendor-http", Platform: "any"},
		{URL: "https://github.com/a/b.tar.gz", Host: "github.com", Class: "github-archive", Platform: "any"}, // dup host
		// Tainted ref — should land in MANUAL ATTENTION block.
		{URL: "https://opaque/foo", Host: "opaque", Class: "unknown", Platform: "any", Tainted: true},
	}, nil); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.AirgapDownloaderConfig("m", "1") + "?mirror=http://mirror.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", res.StatusCode, body)
	}
	text := string(body)

	// One rewrite line per unique host × scheme.
	want := []string{
		"rewrite https://github\\.com/(.*) http://mirror.example.com/github.com/$1",
		"rewrite https://dl\\.google\\.com/(.*) http://mirror.example.com/dl.google.com/$1",
		"allow mirror.example.com",
		"MANUAL ATTENTION REQUIRED",
		"https://opaque/foo", // tainted ref appears in the manual block
		// Header must guide operators across Bazel versions — the
		// experimental_-prefixed flag was renamed in the Bazel 9.0
		// pre-release line; 8.4+ also accepts --module_mirrors for the
		// registry slice.
		"--downloader_config",
		"--experimental_downloader_config",
		"--module_mirrors",
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("config missing %q\n---\n%s", w, text)
		}
	}

	// Content-Disposition for browser download UX.
	cd := res.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "canopy-downloader-config-m-1.txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

// JSON format wraps the same text with metadata.
func TestAirgapDownloaderConfig_JSONFormat(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://github.com/x/y.tar.gz", Host: "github.com", Class: "github-archive", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.AirgapDownloaderConfig("m", "1") + "?format=json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var got api.DownloaderConfig
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if got.Module != "m" || got.Version != "1" {
		t.Errorf("module/version = %q/%q", got.Module, got.Version)
	}
	if got.HostCount != 1 || got.URLCount != 1 {
		t.Errorf("HostCount=%d URLCount=%d, want 1/1", got.HostCount, got.URLCount)
	}
	if !strings.Contains(got.Text, "rewrite") {
		t.Errorf("text missing rewrite directive: %q", got.Text)
	}
}

// --module_mirrors emitter: produces a single `common ...` line with
// per-registry syntax, plus header comments documenting the Bazel
// version compatibility matrix and the relationship to the
// downloader-config artifact.
func TestAirgapModuleMirrors_RendersBazelrcLine(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.AirgapModuleMirrors("m", "1") + "?mirror=http://mirror.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", res.StatusCode, body)
	}
	text := string(body)

	want := []string{
		"common --module_mirrors=https://bcr.bazel.build/=http://mirror.example.com/",
		"Bazel version compatibility",
		">= 8.5",
		">= 8.4",
		"--downloader_config", // cross-reference to the sibling artifact
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("module-mirrors missing %q\n---\n%s", w, text)
		}
	}

	cd := res.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "canopy-module-mirrors-m-1.bazelrc") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

// A typo in the module name must NOT silently return 200 + an
// artifact citing the nonexistent module. The downloader-config
// emitter already 404s via its downstream surface call; module-mirrors
// templates strings only, so it needs an explicit existence guard.
func TestAirgapModuleMirrors_UnknownModuleReturns404(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// NOTE: no WriteReport — module is intentionally absent.
	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.AirgapModuleMirrors("nope", "0.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown module: status = %d, want 404", res.StatusCode)
	}
}

// Inputs that would break out of the templated `common --module_mirrors=...`
// line (newlines, control chars, non-http schemes) must return 400 and
// emit nothing — the .bazelrc artifact is downloaded by operators, so
// injection here lets an attacker smuggle arbitrary Bazel flags.
func TestAirgapModuleMirrors_RejectsInjection(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	bad := []string{
		"mirror=http://x/%0Acommon%20--evil",       // newline in mirror
		"mirror=http://x/%0D",                       // CR
		"mirror=javascript:alert(1)",                // non-http scheme
		"registry=http://r/%0Acommon%20--evil",      // newline in registry
		"registry=ftp://r/",                         // wrong scheme on registry
	}
	for _, q := range bad {
		res, err := http.Get(ts.URL + paths.AirgapModuleMirrors("m", "1") + "?" + q)
		if err != nil {
			t.Fatalf("GET ?%s: %v", q, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s: status = %d, want 400", q, res.StatusCode)
		}
	}
}

func TestAirgapModuleMirrors_CustomRegistry(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.AirgapModuleMirrors("m", "1") + "?mirror=http://m.example/&registry=https://internal.registry/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "common --module_mirrors=https://internal.registry/=http://m.example/") {
		t.Errorf("custom registry not honored:\n%s", body)
	}
}

// ETag conditional GET: first request gets 200 + ETag header; second
// request carrying If-None-Match=<etag> gets 304 with no body.
func TestExternalSurface_ConditionalGET_Returns304(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://x/y", Host: "x", Class: "unknown", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	// First request: 200 + ETag.
	req1, _ := http.NewRequest("GET", ts.URL+paths.External("m", "1"), nil)
	res1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(res1.Body)
	res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("first request: status=%d body=%s", res1.StatusCode, body1)
	}
	etag := res1.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag header on first response")
	}

	// Second request: same ETag → 304.
	req2, _ := http.NewRequest("GET", ts.URL+paths.External("m", "1"), nil)
	req2.Header.Set("If-None-Match", etag)
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified {
		t.Errorf("conditional GET with matching ETag: status=%d, want 304 body=%s", res2.StatusCode, body2)
	}
	if len(body2) != 0 {
		t.Errorf("304 response should have empty body, got %d bytes", len(body2))
	}

	// Mismatched If-None-Match: should fall through to 200 + body.
	req3, _ := http.NewRequest("GET", ts.URL+paths.External("m", "1"), nil)
	req3.Header.Set("If-None-Match", `"stale-etag"`)
	res3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(res3.Body)
	res3.Body.Close()
	if res3.StatusCode != http.StatusOK {
		t.Errorf("stale ETag should fall through to 200, got %d body=%s", res3.StatusCode, body3)
	}
}

// End-to-end: producer ruleset declares a module_extension; canopy's
// corpus contains a consumer module that pins a tag value on it.
// ExternalSurface for the producer surfaces the corpus tag values in
// CorpusUsages so the UI can show "consumer X uses this extension
// with version=1.22.5" — the airgap analyst's bridge between
// "abstract default-tag drive" and "what actual consumers fetch."
func TestExternalSurface_SurfacesCorpusUsages(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Producer: rules_go declares a "go_sdk" module_extension in
	// go/extensions.bzl (mirrors the real-corpus shape).
	producer := &report.ModuleReport{
		Name: "rules_go", Version: "0.50.1",
		ModuleExtensions: []report.ModuleExtSpec{{
			Name:       "go_sdk",
			Provenance: report.Provenance{File: "go/extensions.bzl"},
		}},
	}
	if err := s.WriteReport(ctx, producer); err != nil {
		t.Fatal(err)
	}

	// Consumer: an app pinning two go_sdk download tag instances.
	consumer := &report.ModuleReport{Name: "myapp", Version: "1.0.0"}
	if err := s.WriteReport(ctx, consumer); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "myapp", "1.0.0", []store.UseExtensionUsage{
		{
			ExtensionFile: "@rules_go//go:extensions.bzl",
			ExtensionName: "go_sdk",
			TagIndex:      0, TagName: "download",
			TagAttrsJSON: `{"version":"1.22.5"}`,
		},
		{
			ExtensionFile: "@rules_go//go:extensions.bzl",
			ExtensionName: "go_sdk",
			TagIndex:      1, TagName: "download",
			TagAttrsJSON: `{"version":"1.21.3","goos":"linux"}`,
		},
	}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.External("rules_go", "0.50.1"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}

	var got api.ExternalSurfaceResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if len(got.CorpusUsages) != 1 {
		t.Fatalf("CorpusUsages = %d (%+v), want 1", len(got.CorpusUsages), got.CorpusUsages)
	}
	cu := got.CorpusUsages[0]
	if cu.ExtensionFile != "@rules_go//go:extensions.bzl" {
		t.Errorf("ExtensionFile = %q", cu.ExtensionFile)
	}
	if cu.ExtensionName != "go_sdk" {
		t.Errorf("ExtensionName = %q", cu.ExtensionName)
	}
	if len(cu.Consumers) != 2 {
		t.Fatalf("Consumers = %d, want 2 tag calls from myapp", len(cu.Consumers))
	}
	if cu.Consumers[0].ConsumerModule != "myapp" || cu.Consumers[0].ConsumerVersion != "1.0.0" {
		t.Errorf("Consumer[0] = %+v", cu.Consumers[0])
	}
	// TagAttrs JSON-deserialized for direct UI use.
	if cu.Consumers[0].TagAttrs["version"] != "1.22.5" {
		t.Errorf("Consumer[0].TagAttrs.version = %v, want 1.22.5", cu.Consumers[0].TagAttrs["version"])
	}
	if cu.Consumers[1].TagAttrs["goos"] != "linux" {
		t.Errorf("Consumer[1].TagAttrs.goos = %v, want linux", cu.Consumers[1].TagAttrs["goos"])
	}
}

// The full corpus-driven flow: producer ruleset has extension-impl
// source stored, corpus has consumer tag values for the extension,
// ExternalSurface for the producer drives the extension with those
// real specs AND returns the resulting URLs in Refs (marked with
// APIName="corpus:<ext_name>").
func TestExternalSurface_CorpusDrivesRealURLs(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Producer: rules_go-like ruleset with a templated download URL.
	producer := &report.ModuleReport{
		Name: "rules_go", Version: "0.50.1",
		ModuleExtensions: []report.ModuleExtSpec{{
			Name:       "go_sdk",
			Provenance: report.Provenance{File: "go/extensions.bzl"},
		}},
	}
	if err := s.WriteReport(ctx, producer); err != nil {
		t.Fatal(err)
	}
	const extSrc = `
def _repo_impl(ctx):
    url = "https://dl.google.com/go/go" + ctx.attr.version + ".linux-amd64.tar.gz"
    ctx.download_and_extract(url = url, sha256 = "x")

_sdk_repo = repository_rule(
    implementation = _repo_impl,
    attrs = {"version": attr.string()},
)

def _ext_impl(module_ctx):
    for mod in module_ctx.modules:
        for tag in mod.tags.download:
            _sdk_repo(name = "go_sdk_" + tag.version, version = tag.version)

go_sdk = module_extension(
    implementation = _ext_impl,
    tag_classes = {"download": tag_class(attrs = {"version": attr.string()})},
)
`
	if err := s.WriteModuleExtensionSources(ctx, "rules_go", "0.50.1", []store.ModuleExtensionSource{
		{File: "go/extensions.bzl", Content: []byte(extSrc)},
	}); err != nil {
		t.Fatal(err)
	}

	// Consumer pins go1.22.5.
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "myapp", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteUseExtensionUsages(ctx, "myapp", "1.0.0", []store.UseExtensionUsage{
		{
			ExtensionFile: "@rules_go//go:extensions.bzl",
			ExtensionName: "go_sdk",
			TagIndex:      0, TagName: "download",
			TagAttrsJSON: `{"version":"1.22.5"}`,
		},
	}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.External("rules_go", "0.50.1"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d body=%s", res.StatusCode, body)
	}

	var got api.ExternalSurfaceResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	// The corpus-driven ref should be in got.Refs with APIName="corpus:go_sdk".
	wantURL := "https://dl.google.com/go/go1.22.5.linux-amd64.tar.gz"
	found := false
	for _, r := range got.Refs {
		if r.URL == wantURL {
			found = true
			if r.APIName != "corpus:go_sdk" {
				t.Errorf("APIName = %q, want corpus:go_sdk (so UI can mark it as corpus-derived)", r.APIName)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected corpus-driven URL %q in Refs; got refs=%+v", wantURL, got.Refs)
	}
}

// When the producer has extensions but no consumers exist in the
// corpus yet, CorpusUsages is empty (not nil-vs-set distinction
// matters less than "no rows" semantically).
func TestExternalSurface_NoCorpusConsumersOmitsField(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{
		Name: "lonely_ruleset", Version: "0.1.0",
		ModuleExtensions: []report.ModuleExtSpec{{
			Name:       "lonely_ext",
			Provenance: report.Provenance{File: "ext.bzl"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + paths.External("lonely_ruleset", "0.1.0"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var got api.ExternalSurfaceResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.CorpusUsages) != 0 {
		t.Errorf("CorpusUsages = %d, want 0 with no consumers", len(got.CorpusUsages))
	}
}

func TestExternalSurface_EmptyStore_ReturnsEmptyRefs(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.External("unknown", "0.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", res.StatusCode, body)
	}
	var got api.ExternalSurfaceResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if len(got.Refs) != 0 {
		t.Errorf("expected empty refs, got %v", got.Refs)
	}
}
