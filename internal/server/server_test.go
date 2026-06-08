package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"
	scipproto "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/api/paths"
	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/featureflags"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// TestMirrorServeRouting builds a content-addressed mirror in a temp dir,
// then hits /m/* end-to-end. Verifies:
//   - bazel_registry.json advertises the mirror
//   - /m/<host+path> redirects to the right content-addressed blob (200)
//   - /m/<unknown> returns a clean 404 with our "not in mirror" message
//
// We use a temp dir because the on-disk testdata/registry-tree fixture
// dates from before content-addressing — its blobs are named by URL
// basename, which doesn't match the post-CAS lookup scheme. Building
// fixture in-test keeps the assertion honest.
func TestMirrorServeRouting(t *testing.T) {
	root := t.TempDir()
	payload := []byte("imagine bazel_skylib 1.7.1 tarball bytes here")
	sum := sha256.Sum256(payload)
	sri := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	hexName := hex.EncodeToString(sum[:])

	// modules/foo/1.0.0/source.json pointing at an upstream URL.
	upstreamURL := "https://github.com/foo/foo/releases/download/1.0.0/foo-1.0.0.tar.gz"
	modDir := filepath.Join(root, "modules", "foo", "1.0.0")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcJSON := []byte(`{"url":"` + upstreamURL + `","integrity":"` + sri + `"}`)
	if err := os.WriteFile(filepath.Join(modDir, "source.json"), srcJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	// MODULE.bazel + metadata.json so the BCR side doesn't 404 on probes.
	_ = os.WriteFile(filepath.Join(modDir, "MODULE.bazel"), []byte("module(name=\"foo\",version=\"1.0.0\")\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "modules", "foo", "metadata.json"), []byte(`{"versions":["1.0.0"]}`), 0o644)
	// bazel_registry.json on disk (will be overridden by mirror options at serve time anyway).
	_ = os.WriteFile(filepath.Join(root, "bazel_registry.json"), []byte("{}"), 0o644)

	// Content-addressed blob.
	blobsDir := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobsDir, hexName), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	b := backend.NewFile(root)
	ts := httptest.NewServer(server.NewWithOptions(b, nil, nil, server.Options{
		MirrorBaseURL: "http://canopy/m/",
		MirrorRoot:    root,
	}))
	t.Cleanup(ts.Close)

	// bazel_registry.json should advertise the mirror.
	res, err := http.Get(ts.URL + "/bazel_registry.json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"mirrors"`) {
		t.Fatalf("bazel_registry.json missing mirrors: %s", body)
	}

	// /m/<upstream-host+path> should serve the content-addressed blob.
	res2, err := http.Get(ts.URL + "/m/github.com/foo/foo/releases/download/1.0.0/foo-1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != 200 {
		t.Fatalf("expected 200, got %d body=%s", res2.StatusCode, body2)
	}
	if !bytes.Equal(body2, payload) {
		t.Fatalf("served bytes don't match payload: got %q want %q", body2, payload)
	}

	// /m/<unknown> should return 404 with our custom message.
	res3, err := http.Get(ts.URL + "/m/unknown.example/foo.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	body3, _ := io.ReadAll(res3.Body)
	res3.Body.Close()
	if res3.StatusCode != 404 {
		t.Fatalf("expected 404, got %d body=%s", res3.StatusCode, body3)
	}
	if !strings.Contains(string(body3), "not in mirror") {
		t.Fatalf("expected 'not in mirror' message, got: %s", body3)
	}
}

func TestServerBCREndpoints(t *testing.T) {
	b := backend.NewFile("../../testdata/registry-tree")
	ts := httptest.NewServer(server.New(b, nil, nil))
	t.Cleanup(ts.Close)

	cases := []struct {
		path          string
		wantStatus    int
		wantSubstring string
		wantCType     string
	}{
		{"/healthz", 200, "ok", ""},
		{"/readyz", 200, "ok", ""},
		{"/bazel_registry.json", 200, "{}", "application/json"},
		{"/modules/foo/metadata.json", 200, `"versions"`, "application/json"},
		{"/modules/foo/1.0.0/MODULE.bazel", 200, `module(name="foo"`, "text/plain"},
		{"/modules/foo/1.0.0/source.json", 200, `"integrity"`, "application/json"},
		{"/modules/foo/1.0.0.1/source.json", 200, `"strip_prefix": "foo-1.0.0.1"`, "application/json"},
		{"/blobs/foo-1.0.0.tar.gz", 200, "", "application/gzip"},
		{"/modules/foo/9.9.9/MODULE.bazel", 404, "", ""},
		{"/modules/ghost/metadata.json", 404, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantCType != "" && !strings.Contains(resp.Header.Get("Content-Type"), tc.wantCType) {
				t.Errorf("Content-Type: got %q, want substring %q", resp.Header.Get("Content-Type"), tc.wantCType)
			}
			if tc.wantSubstring != "" {
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), tc.wantSubstring) {
					t.Errorf("body missing %q; got %q", tc.wantSubstring, string(body))
				}
			}
		})
	}
}

func TestPathTraversalGuarded(t *testing.T) {
	b := backend.NewFile("../../testdata/registry-tree")
	ts := httptest.NewServer(server.New(b, nil, nil))
	t.Cleanup(ts.Close)

	// chi normalizes %2F to /, so the path becomes /blobs/../etc/passwd which
	// chi treats as a routing miss (handler not matched). Either 404 or
	// the request being routed elsewhere — never serving /etc/passwd.
	resp, err := http.Get(ts.URL + "/blobs/..%2Fetc%2Fpasswd")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Errorf("expected non-200, got 200 (potential traversal!)")
	}
}

// Compile-time check that *backend.File satisfies the Backend contract used
// by server.New (no test body needed; it's a type assertion).
var _ backend.Backend = (*backend.File)(nil)

// TestVersion_EndpointReturnsJSON pins the wire shape of /api/version.
// The values themselves are -ldflags-injected at build time; tests run
// against the "dev"/"unknown" sentinels. We only assert the three
// fields are present and Content-Type is JSON — both apps (canopy +
// understory) ship the same contract so tooling can probe either.
func TestVersion_EndpointReturnsJSON(t *testing.T) {
	ts := httptest.NewServer(server.New(nil, nil, nil))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + paths.SystemVersion())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q; want application/json...", ct)
	}
	var got struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		BuiltAt string `json:"built_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version == "" {
		t.Errorf("version is empty; want non-empty (sentinel or injected)")
	}
	if got.Commit == "" {
		t.Errorf("commit is empty; want non-empty (sentinel or injected)")
	}
	if got.BuiltAt == "" {
		t.Errorf("built_at is empty; want non-empty (sentinel or injected)")
	}
}

// TestSPA_APIPath_Returns404JSON pins that /api/* unknown routes return a
// JSON 404, not the SvelteKit SPA shell. API consumers expect structured
// errors; serving HTML for /api/whatever silently breaks them.
func TestSPA_APIPath_Returns404JSON(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/api/this-route-does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("status: got %d, want 404; body=%s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type: got %q, want application/json", ct)
	}
}

// TestSearch_EmptyResult_ReturnsEmptyArray pins the wire contract: hits is
// always a JSON array, never null. The UI's `results.hits.length` crashes
// on null; the server must emit `"hits":[]` for zero matches.
func TestSearch_EmptyResult_ReturnsEmptyArray(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.Search() + "?q=zzznomatchzzz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status: got %d, body=%s", res.StatusCode, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	hits, ok := raw["hits"]
	if !ok {
		t.Fatalf("response missing hits field: %s", body)
	}
	if string(hits) != "[]" {
		t.Fatalf("hits: got %s, want []", hits)
	}
}

// TestXRefs_NoSymbol_Returns400 — the endpoint is a query-string GET,
// so a missing/empty symbol is unambiguous client error. JSON shape
// matches the rest of canopy's API error envelope.
func TestXRefs_NoSymbol_Returns400(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.XRefs())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; want 400", res.StatusCode, body)
	}
	var env map[string]string
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if env["error"] == "" {
		t.Errorf("expected error field, got %v", env)
	}
}

// TestXRefs_EmptyStore_ReturnsEmptyGroups — when no SCIP blobs are
// indexed, the response must still parse cleanly: count=0 and an
// empty (not null) groups array. The UI dereferences `.groups.length`.
func TestXRefs_EmptyStore_ReturnsEmptyGroups(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.XRefs() + "?symbol=bzlmod+foo%401.0+a.bzl%23x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s; want 200", res.StatusCode, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if string(raw["count"]) != "0" {
		t.Errorf("count=%s; want 0", raw["count"])
	}
	if string(raw["groups"]) != "[]" {
		t.Errorf("groups=%s; want []", raw["groups"])
	}
}

// TestListModules_EmptyStore_ReturnsEmptyArray pins the
// `modules:[]` (not null) contract — the UI dereferences
// `.modules.length` to decide between empty-state + listing.
func TestListModules_EmptyStore_ReturnsEmptyArray(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.ModulesIndex())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if string(raw["modules"]) != "[]" {
		t.Errorf("modules=%s; want []", raw["modules"])
	}
}

// TestListModules_GroupsAndCountsVersions verifies the per-module
// roll-up: latest version, count, sorted output.
func TestListModules_GroupsAndCountsVersions(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Two modules, one with two versions, one with three. Order
	// of inserts deliberately scrambled — output should sort.
	for _, mv := range [][2]string{
		{"alpha", "1.0.0"},
		{"zebra", "0.0.4"},
		{"alpha", "1.2.0"},
		{"zebra", "0.0.10"},
		{"alpha", "1.1.0"},
		{"zebra", "0.0.7"},
	} {
		r := &report.ModuleReport{Name: mv[0], Version: mv[1]}
		if err := s.WriteReport(ctx, r); err != nil {
			t.Fatalf("WriteReport: %v", err)
		}
	}

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.ModulesIndex())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var got struct {
		Modules []api.ModuleSummary `json:"modules"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(got.Modules))
	}
	// Sorted by name ASC: alpha then zebra.
	if got.Modules[0].Name != "alpha" || got.Modules[1].Name != "zebra" {
		t.Errorf("order: %s, %s", got.Modules[0].Name, got.Modules[1].Name)
	}
	if got.Modules[0].VersionCount != 3 || got.Modules[1].VersionCount != 3 {
		t.Errorf("version counts: %+v", got.Modules)
	}
	// "Latest" is the last version in ASC order — for lexical
	// sort that's "1.2.0" for alpha and "0.0.7" for zebra (since
	// "0.0.10" < "0.0.7" lexically). This matches the store's
	// existing sort behaviour; the UI uses canopy-cli for version
	// ordering, not this endpoint.
	if got.Modules[0].LatestVersion != "1.2.0" {
		t.Errorf("alpha latest = %q, want 1.2.0", got.Modules[0].LatestVersion)
	}
}

// Plan 14 Layer 1: filter query params on /modules. The UI's
// ?q=&source=true&sort= must be curl-equivalent so a shared URL
// renders the same module subset.
func TestListModules_FilterByQueryParams(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, mv := range [][2]string{
		{"alpha", "1.0.0"},
		{"alpha", "1.1.0"},
		{"beta", "2.0.0"},
		{"gamma", "3.0.0"},
	} {
		if err := s.WriteReport(ctx, &report.ModuleReport{Name: mv[0], Version: mv[1]}); err != nil {
			t.Fatal(err)
		}
	}

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	cases := []struct {
		name      string
		query     string
		wantNames []string // order matters
	}{
		{"no filter (default sort)", "", []string{"alpha", "beta", "gamma"}},
		{"q= substring filter", "q=al", []string{"alpha"}},
		{"q= case-insensitive", "q=ALPHA", []string{"alpha"}},
		{"sort=name asc", "sort=name", []string{"alpha", "beta", "gamma"}},
		{"sort=versions desc (alpha has 2, others 1)", "sort=versions", []string{"alpha", "beta", "gamma"}},
		{"unknown sort key falls back to name", "sort=xyzzy", []string{"alpha", "beta", "gamma"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := ts.URL + paths.ModulesIndex()
			if c.query != "" {
				url += "?" + c.query
			}
			res, err := http.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			var got struct {
				Modules []api.ModuleSummary `json:"modules"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal: %v body=%s", err, body)
			}
			gotNames := make([]string, len(got.Modules))
			for i, m := range got.Modules {
				gotNames[i] = m.Name
			}
			if !equalStrSlice(gotNames, c.wantNames) {
				t.Errorf("got %v, want %v", gotNames, c.wantNames)
			}
		})
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCodeNavLatest_RedirectsToLatestVersion exercises the
// version-less `/modules/<m>/code-nav` URL — server resolves the
// latest indexed version and 302s. Without this redirect the URL
// falls through to the SPA fallback and SvelteKit renders an
// unhelpful client-side 404 (the user-visible "do not work" bug).
func TestCodeNavLatest_RedirectsToLatestVersion(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		if err := s.WriteReport(ctx, &report.ModuleReport{Name: "rules_shell", Version: v}); err != nil {
			t.Fatalf("WriteReport: %v", err)
		}
	}

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	// Use a non-following client so we can assert the 302 location.
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Get(ts.URL + "/modules/rules_shell/code-nav")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status=%d; want 302", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	want := "/modules/rules_shell/0.3.0/code-nav/"
	if loc != want {
		t.Errorf("Location=%q; want %q", loc, want)
	}

	// Sub-path preservation: /modules/<m>/code-nav/file/foo.bzl
	// should redirect carrying the sub-path through.
	res2, err := client.Get(ts.URL + "/modules/rules_shell/code-nav/file/lib.bzl")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusFound {
		t.Fatalf("sub-path status=%d", res2.StatusCode)
	}
	if got := res2.Header.Get("Location"); got != "/modules/rules_shell/0.3.0/code-nav/file/lib.bzl" {
		t.Errorf("sub-path Location=%q", got)
	}
}

// TestCodeNavLatest_UnknownModuleRendersFriendlyPage — pointing at a
// module that has never been indexed renders the friendly 404 page
// (not a raw 404 or 503).
func TestCodeNavLatest_UnknownModuleRendersFriendlyPage(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/modules/never_indexed/code-nav")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d; want 404", res.StatusCode)
	}
	if !bytes.Contains(body, []byte("Not indexed")) {
		t.Errorf("friendly 404 page expected; body=%s", body)
	}
}

// TestListModules_HasSourceIndexReflectsScipDocCount — modules whose
// SCIP blob has zero documents (e.g. zlib's source tarball ships no
// Starlark files) report has_source_index=false. The UI uses this
// to suppress "Code →" affordances for those modules so users don't
// click into an empty file tree.
func TestListModules_HasSourceIndexReflectsScipDocCount(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Two modules: "alpha" gets a non-empty SCIP blob, "beta" gets none.
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "alpha", Version: "1.0"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "beta", Version: "1.0"}); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if err := s.WriteScipBlob(ctx, "alpha", "1.0", scipBlobWithOneDoc(t)); err != nil {
		t.Fatalf("WriteScipBlob: %v", err)
	}
	// has_source_index is now cached on the versions row; production
	// writes both via Service.Bump's ingest path. This test reaches
	// into the store directly, so we must also flip the cached flag
	// to match — otherwise the listing reads the column default (0)
	// regardless of what the blob actually contains.
	if err := s.SetHasSourceIndex(ctx, "alpha", "1.0", true); err != nil {
		t.Fatalf("SetHasSourceIndex: %v", err)
	}
	// beta: no blob written. Service handles the missing case as false.

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.ModulesIndex())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var got struct {
		Modules []api.ModuleSummary `json:"modules"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(got.Modules))
	}
	for _, m := range got.Modules {
		switch m.Name {
		case "alpha":
			if !m.HasSourceIndex {
				t.Error("alpha has SCIP doc → expected has_source_index=true")
			}
		case "beta":
			if m.HasSourceIndex {
				t.Error("beta has no SCIP blob → expected has_source_index=false")
			}
		}
	}
}

// scipBlobWithOneDoc returns the bytes of a minimal SCIP index with
// one Document — enough that understory.OpenBytes() + Index.Files()
// return a non-empty list.
func scipBlobWithOneDoc(t *testing.T) []byte {
	t.Helper()
	idx := &scipproto.Index{
		Metadata: &scipproto.Metadata{Version: 0},
		Documents: []*scipproto.Document{{
			RelativePath: "MODULE.bazel",
			Occurrences: []*scipproto.Occurrence{{
				Symbol:      "test",
				Range:       []int32{0, 0, 0, 1},
				SymbolRoles: int32(scipproto.SymbolRole_Definition),
			}},
		}},
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// ============================================================================
// Feature flag + ingest gate tests.
// ----------------------------------------------------------------------------
// These pin the contract that protects ingest writes:
//   - /api/v1/system/features always returns the UI-safe snapshot
//   - POST /api/v1/actions/ingest/recursive 503s when the flag is disabled
//   - It 429s when the per-IP rate limit fires
//   - body.upstream is dropped unless explicitly opted in (SSRF guard)
// ============================================================================

func TestApiFeatures_PublicSnapshot(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.NewWithOptions(nil, bzlhub.New(s), nil, server.Options{
		Flags: featureflags.Flags{
			IngestWriteEnabled: true,
			RegistryURL:        "https://internal-only.example",
			DemoMode:           true,
			DemoBanner:         "public demo",
		},
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.SystemFeatures())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	var snap struct {
		IngestWriteEnabled bool   `json:"ingest_write_enabled"`
		DemoMode           bool   `json:"demo_mode"`
		DemoBanner         string `json:"demo_banner"`
		RegistryURL        string `json:"registry_url"` // must be absent
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if !snap.IngestWriteEnabled {
		t.Errorf("ingest_write_enabled = false, want true")
	}
	if !snap.DemoMode {
		t.Errorf("demo_mode = false, want true")
	}
	if snap.DemoBanner != "public demo" {
		t.Errorf("demo_banner = %q, want public demo", snap.DemoBanner)
	}
	if snap.RegistryURL != "" {
		t.Errorf("registry_url leaked: %q (must never appear in public snapshot)", snap.RegistryURL)
	}
	// Belt-and-suspenders: the raw JSON must not even contain the key.
	if strings.Contains(string(body), "internal-only") {
		t.Errorf("public snapshot leaked server-only registry URL: %s", body)
	}
}

func TestApiIngest_Disabled_Returns503(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// IngestWriteEnabled defaults to false — explicit zero value here
	// to make the intent obvious.
	ts := httptest.NewServer(server.NewWithOptions(nil, bzlhub.New(s), nil, server.Options{
		Flags: featureflags.Flags{IngestWriteEnabled: false},
	}))
	t.Cleanup(ts.Close)

	res, err := http.Post(ts.URL+paths.ActionIngestRecursive(), "application/json",
		strings.NewReader(`{"module":"x","version":"1.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d (want 503) body=%s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "disabled") {
		t.Errorf("body should explain why: %s", body)
	}
}

func TestApiIngest_RateLimit_Returns429(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.NewWithOptions(nil, bzlhub.New(s), nil, server.Options{
		Flags: featureflags.Flags{
			IngestWriteEnabled:    true,
			IngestRateLimitPerMin: 1,
			IngestMaxConcurrent:   10, // generous; we're testing rate, not concurrency
		},
	}))
	t.Cleanup(ts.Close)

	// First call burns the only token. It will likely 5xx because we
	// gave bogus module/version; that's fine — what matters is the
	// limiter consumed a token before the handler decoded the body.
	// Actually no: the limiter releases on return, so we need to do
	// the first request all the way through before the second hits.
	_, _ = http.Post(ts.URL+paths.ActionIngestRecursive(), "application/json",
		strings.NewReader(`{"module":"x","version":"1.0.0"}`))

	// Second call from the same IP should 429.
	res, err := http.Post(ts.URL+paths.ActionIngestRecursive(), "application/json",
		strings.NewReader(`{"module":"x","version":"1.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d (want 429) body=%s", res.StatusCode, body)
	}
	if got := res.Header.Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
}

// ============================================================================
// BCR probe endpoint tests.
// ----------------------------------------------------------------------------
// Pin the wire shape the UI consumes:
//   - module + version both required (400 otherwise)
//   - successful probe returns the structured Result with version_exists
//   - non-404 upstream errors surface as 502
// ============================================================================

func TestApiBCRProbe_Required(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.NewWithOptions(nil, bzlhub.New(s), nil, server.Options{}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.SystemBCRProbe() + "?module=foo")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when version is missing", res.StatusCode)
	}
}

// TestApiBCRProbe_HitsUpstream pins that the handler actually hits the
// configured RegistryURL and shapes the response from the upstream's
// metadata.json. We point RegistryURL at an httptest server returning
// canned BCR-shape responses.
func TestApiBCRProbe_HitsUpstream(t *testing.T) {
	// Upstream stub: serve a 404 for source.json (version not found)
	// and a metadata.json listing two versions for the module.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/modules/rules_go/0.49.0/source.json":
			http.NotFound(w, r)
		case "/modules/rules_go/metadata.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"versions":["0.47.0","0.50.1"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.NewWithOptions(nil, bzlhub.New(s), nil, server.Options{
		Flags: featureflags.Flags{RegistryURL: upstream.URL},
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.SystemBCRProbe() + "?module=rules_go&version=0.49.0")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", res.StatusCode, body)
	}
	var got struct {
		Module            string   `json:"module"`
		Version           string   `json:"version"`
		VersionExists     bool     `json:"version_exists"`
		ModuleExists      bool     `json:"module_exists"`
		VersionsAvailable []string `json:"versions_available"`
		LatestVersion     string   `json:"latest_version"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if got.VersionExists {
		t.Error("VersionExists = true, want false")
	}
	if !got.ModuleExists {
		t.Error("ModuleExists = false, want true")
	}
	if got.LatestVersion != "0.50.1" {
		t.Errorf("LatestVersion = %q, want 0.50.1", got.LatestVersion)
	}
}
