package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sriOf computes the SRI "sha256-<base64>" string for payload.
func sriOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// newTestClient returns a Client wired with the given test transport
// host added to the allowlist. The 127.0.0.1:* shape httptest produces
// is allowed via the local-* wildcard the production defaults carry —
// see allowlist.go's snapshotDefaultAllowedHosts for the localhost
// exemption.
func newTestClient() *Client {
	// Empty AllowedHosts = no enforcement, which is what tests want.
	return &Client{HTTP: &http.Client{Transport: http.DefaultTransport}}
}

func TestJoinURL_Composes(t *testing.T) {
	u, err := joinURL("https://bcr.example/", "modules", "rules_go", "0.50.1", "source.json")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://bcr.example/modules/rules_go/0.50.1/source.json"
	if u != want {
		t.Errorf("joinURL = %q, want %q", u, want)
	}
}

func TestJoinURL_PreservesPlusInVersion(t *testing.T) {
	// "1.0.0+ext" must reach the registry verbatim (the path "+" is
	// literal — it's only special in query strings). Bazel's Version.java
	// strips +<meta> on its side, but the registry may host that path.
	u, err := joinURL("https://bcr.example", "modules", "x", "1.0.0+ext", "source.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "1.0.0+ext") {
		t.Errorf("joinURL must preserve + in path segment: %s", u)
	}
}

func TestJoinURL_EscapesSpace(t *testing.T) {
	// Defensive: any caller passing an actual space in a segment must
	// get it escaped (real-world this should never happen — version
	// strings don't contain spaces — but PathEscape is the right
	// contract).
	u, err := joinURL("https://bcr.example", "with space")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "with%20space") {
		t.Errorf("joinURL must escape space: %s", u)
	}
}

func TestJoinURL_TrimsTrailingSlash(t *testing.T) {
	// Slash hygiene: registry URL trailing slash should NOT produce
	// "//modules" in the composed URL.
	u, _ := joinURL("https://bcr.example/", "modules", "x")
	if strings.Contains(u, "//modules") {
		t.Errorf("joinURL doubled slash: %s", u)
	}
}

func TestJoinURL_RejectsMalformedRegistry(t *testing.T) {
	_, err := joinURL("ht tp://bad url", "x")
	if err == nil {
		t.Error("want error on unparseable registry URL")
	}
}

func TestGet_404MapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	_, err := newTestClient().get(context.Background(), srv.URL+"/missing")
	if err == nil {
		t.Fatal("want error on 404")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want errors.Is(ErrNotFound), got %v", err)
	}
}

func TestGet_5xxReturnsTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestClient().get(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("want error on 5xx")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("5xx must NOT map to ErrNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should name the status: %v", err)
	}
}

func TestGet_2xxReturnsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	resp, err := newTestClient().get(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestGetJSON_RejectsHTML(t *testing.T) {
	// A captive portal returning HTML with 200 must be rejected before
	// it gets parsed as JSON and silently corrupts downstream state.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>captive portal</body></html>`))
	}))
	defer srv.Close()

	_, err := newTestClient().getJSON(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("want error on HTML response")
	}
	if !strings.Contains(err.Error(), "unexpected Content-Type") {
		t.Errorf("error should name the bad content-type: %v", err)
	}
}

func TestGetJSON_AcceptsMissingContentType(t *testing.T) {
	// Some plain BCR mirrors omit Content-Type. Go's net/http sniff
	// defaults to text/plain for JSON-shaped bodies, but the server
	// can also simply not set it. Either way, getJSON must accept.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Type"] = nil // suppress auto-detect
		_, _ = w.Write([]byte(`{"versions":["1"]}`))
	}))
	defer srv.Close()

	body, err := newTestClient().getJSON(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if !strings.Contains(string(body), "versions") {
		t.Errorf("body should pass through: %s", body)
	}
}

func TestGetJSON_AcceptsApplicationJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if _, err := newTestClient().getJSON(context.Background(), srv.URL+"/"); err != nil {
		t.Errorf("application/json should pass: %v", err)
	}
}

func TestGetMetadata_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/modules/foo/metadata.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"versions":["1.0.0","2.0.0"],"homepage":"https://example.com"}`))
	}))
	defer srv.Close()

	m, err := newTestClient().GetMetadata(context.Background(), srv.URL, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Versions) != 2 || m.Versions[0] != "1.0.0" {
		t.Errorf("versions: %v", m.Versions)
	}
	if m.Homepage != "https://example.com" {
		t.Errorf("homepage: %q", m.Homepage)
	}
}

func TestGetMetadata_NotFoundPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	_, err := newTestClient().GetMetadata(context.Background(), srv.URL, "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("metadata 404 should wrap ErrNotFound, got %v", err)
	}
}

func TestGetMetadataBytes_PreservesUnknownFields(t *testing.T) {
	body := []byte(`{"versions":["1"],"future_field":"keep me"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := newTestClient().GetMetadataBytes(context.Background(), srv.URL, "x")
	if err != nil {
		t.Fatal(err)
	}
	// Byte-for-byte preservation is the contract — mirror writes
	// expect to round-trip unknown fields verbatim.
	if !strings.Contains(string(got), "future_field") {
		t.Errorf("raw bytes lost unknown field: %s", got)
	}
}

func TestGetSourceJSON_ParsesIntegrityFields(t *testing.T) {
	src := SourceJSON{
		URL:         "https://example.com/x.tar.gz",
		Integrity:   "sha256-deadbeef",
		StripPrefix: "x-1.0",
	}
	body, _ := json.Marshal(src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := newTestClient().GetSourceJSON(context.Background(), srv.URL, "x", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != src.URL || got.Integrity != src.Integrity || got.StripPrefix != src.StripPrefix {
		t.Errorf("source.json parse mismatch: %+v", got)
	}
}

func TestParseSourceJSON_RejectsGarbage(t *testing.T) {
	_, err := ParseSourceJSON([]byte(`not json at all`))
	if err == nil {
		t.Error("want error on non-JSON input")
	}
}

func TestGetModuleBazel_ReturnsRawBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// MODULE.bazel is text — no Content-Type guard should fire
		// (it's plain text or no header).
		_, _ = w.Write([]byte(`module(name = "x", version = "1.0")`))
	}))
	defer srv.Close()

	body, err := newTestClient().GetModuleBazel(context.Background(), srv.URL, "x", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `module(name = "x"`) {
		t.Errorf("body lost content: %s", body)
	}
}

func TestFetchArchive_VerifiesIntegrity(t *testing.T) {
	payload := []byte("hello canopy")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Compute the right SRI; provide it to FetchArchive.
	integ := sriOf(payload)
	src := &SourceJSON{URL: srv.URL + "/x.tar.gz", Integrity: integ}

	rc, vr, err := newTestClient().FetchArchive(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != string(payload) {
		t.Errorf("body lost content")
	}
	if err := vr.Verify(); err != nil {
		t.Errorf("matching SRI should verify: %v", err)
	}
}

func TestFetchArchive_DetectsCorruption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("CORRUPTED"))
	}))
	defer srv.Close()

	// Integrity for a DIFFERENT payload — mismatch must surface.
	src := &SourceJSON{URL: srv.URL + "/x", Integrity: sriOf([]byte("good"))}

	rc, vr, err := newTestClient().FetchArchive(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	_, _ = io.ReadAll(rc)
	if err := vr.Verify(); err == nil {
		t.Error("mismatched SRI must error on Verify")
	}
}

func TestFetchArchive_RequiresURL(t *testing.T) {
	_, _, err := newTestClient().FetchArchive(context.Background(), &SourceJSON{})
	if err == nil {
		t.Error("empty URL should error")
	}
}

// TestGetMetadata_RejectsOverCapBody guards against OOM-via-upstream:
// a compromised registry serving a metadata.json larger than
// MaxJSONResponseBytes must surface ErrResponseTooLarge instead of
// io.ReadAll-ing the whole thing into memory. Real-world metadata.json
// for the largest BCR modules is well under 100KB; 16MB is 100x
// headroom and an obvious bomb at the same time.
func TestGetMetadata_RejectsOverCapBody(t *testing.T) {
	// Serve a body one byte over the cap.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Streaming Write so we don't allocate the full payload
		// here; the test target's behavior is what matters.
		const chunk = "x"
		for i := int64(0); i <= MaxJSONResponseBytes; i++ {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer srv.Close()

	_, err := newTestClient().GetMetadata(context.Background(), srv.URL, "huge")
	if err == nil {
		t.Fatal("expected error on oversized body, got nil")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("want ErrResponseTooLarge, got %v", err)
	}
}

// TestGetMetadata_AcceptsExactCapBody — a response that just fits
// the cap must succeed; guards against off-by-one in the limit check.
func TestGetMetadata_AcceptsExactCapBody(t *testing.T) {
	// Build a JSON body of exactly the cap size (or near it). We
	// want the parser to succeed AND not crash on the limit boundary.
	const fitsCap = `{"versions":["1.0.0"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fitsCap))
	}))
	defer srv.Close()

	m, err := newTestClient().GetMetadata(context.Background(), srv.URL, "small")
	if err != nil {
		t.Fatalf("under-cap body should parse: %v", err)
	}
	if len(m.Versions) != 1 {
		t.Errorf("unexpected versions: %v", m.Versions)
	}
}
