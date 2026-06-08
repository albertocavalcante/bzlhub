package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/egress"
)

// SourceJSON mirrors the wire shape of modules/<n>/<v>/source.json — the
// subset canopy needs for fetching. JSON tags match go-bcr / bazel-oci-worker.
type SourceJSON struct {
	Type        string            `json:"type,omitempty"`
	URL         string            `json:"url,omitempty"`
	Integrity   string            `json:"integrity,omitempty"`
	StripPrefix string            `json:"strip_prefix,omitempty"`
	ArchiveType string            `json:"archive_type,omitempty"`
	Patches     map[string]string `json:"patches,omitempty"`
	PatchStrip  int               `json:"patch_strip,omitempty"`
}

// MetadataJSON is the subset of modules/<n>/metadata.json canopy reads.
type MetadataJSON struct {
	Versions       []string          `json:"versions"`
	YankedVersions map[string]string `json:"yanked_versions,omitempty"`
	Homepage       string            `json:"homepage,omitempty"`
}

// ErrNotFound is returned by Client methods when the registry responds
// with HTTP 404. Callers that want to distinguish "this module/version
// does not exist" from other transport failures can errors.Is against
// this sentinel; everything else (5xx, timeouts, bad TLS) surfaces as
// the underlying error.
var ErrNotFound = errors.New("registry: not found")

// MaxJSONResponseBytes caps body reads for upstream JSON / small text
// endpoints (source.json, MODULE.bazel, metadata.json). Even BCR
// modules with hundreds of versions ship <100KB metadata; 16MB is
// 100x headroom and well below the OOM threshold for canopy's
// typical deployment — but small enough that a compromised upstream
// can't sink the process by serving a 10GB body.
//
// FetchArchive bypasses this cap; archives flow through the
// archive package's own MaxExtractBytes (500MB) check.
const MaxJSONResponseBytes = 16 * 1024 * 1024

// ErrResponseTooLarge is returned when an upstream response body
// exceeds MaxJSONResponseBytes. Sentinel so callers can errors.Is
// to distinguish "bomb" from generic transport / parse failure.
var ErrResponseTooLarge = errors.New("fetch: response body exceeded cap")

// readAllCapped is io.ReadAll wrapped in an io.LimitReader sized to
// detect overflow. Reads up to maxBytes+1 so we can tell "fit exactly"
// from "ran over" without a separate length check from the upstream.
func readAllCapped(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrResponseTooLarge, len(body), maxBytes)
	}
	return body, nil
}

// Client fetches BCR-shape JSON + archives. Reuses a single http.Client so
// connections pool across calls.
//
// AllowedHosts is the egress allowlist: when non-empty, every
// outbound request's destination host MUST match one of these
// patterns (exact or "*.suffix" wildcard) or the request is
// rejected with ErrEgressDenied before any network is touched.
// Empty allowlist = no enforcement (default). Operator opts in by
// setting this at startup. See internal/fetch/allowlist.go for
// matching rules.
type Client struct {
	HTTP         *http.Client
	AllowedHosts []string
}

// NewClient returns a Client with sensible defaults.
//
// Timeout is 5 minutes because real archive fetches can take longer than
// the canonical "30s for an HTTP call" — GitHub-hosted release tarballs
// can be 50+ MB, and under concurrent ingest (workers > 4) we routinely
// observed context-deadline errors at 30s. 5 minutes is generous enough
// to absorb most legitimate slow downloads while still bounding hangs.
// Redirect policy is the stdlib default (max 10 hops), with every
// redirected request passing through the same host allowlist transport.
func NewClient() *Client {
	allowed := snapshotDefaultAllowedHosts()
	// Egress wraps fetch's existing host-allowlist transport.
	// fetch keeps its own AllowedHosts gate (per-instance,
	// caller-overridable, source-of-truth for the registry
	// allowlist). The egress policy is permissive here because
	// bzlhub serve binds the active profile policy in cmd/bzlhub/
	// at startup; this is the composition seam Plan 28 C5
	// introduces. See internal/egress/client.go for the wrap
	// semantics.
	hc := egress.NewHTTPClientWithTransport(
		egress.Policy{},
		allowlistTransport{base: http.DefaultTransport, allowedHosts: allowed},
	)
	hc.Timeout = 5 * time.Minute
	return &Client{
		HTTP:         hc,
		AllowedHosts: allowed,
	}
}

// GetMetadata fetches modules/<module>/metadata.json from the registry.
func (c *Client) GetMetadata(ctx context.Context, registryURL, module string) (*MetadataJSON, error) {
	u, err := joinURL(registryURL, "modules", module, "metadata.json")
	if err != nil {
		return nil, err
	}
	b, err := c.getJSON(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("get metadata %s: %w", module, err)
	}
	var m MetadataJSON
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse metadata %s: %w", module, err)
	}
	return &m, nil
}

// GetMetadataBytes returns the raw modules/<module>/metadata.json bytes
// without parsing. Useful for callers (mirror writers, summary tools)
// that want to preserve every upstream field round-trip without
// teaching this package about each one.
func (c *Client) GetMetadataBytes(ctx context.Context, registryURL, module string) ([]byte, error) {
	u, err := joinURL(registryURL, "modules", module, "metadata.json")
	if err != nil {
		return nil, err
	}
	b, err := c.getRaw(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("get metadata %s: %w", module, err)
	}
	return b, nil
}

// GetSourceJSON fetches modules/<module>/<version>/source.json and returns
// the parsed struct.
func (c *Client) GetSourceJSON(ctx context.Context, registryURL, module, version string) (*SourceJSON, error) {
	b, err := c.GetSourceJSONBytes(ctx, registryURL, module, version)
	if err != nil {
		return nil, err
	}
	return ParseSourceJSON(b)
}

// GetSourceJSONBytes returns the raw source.json bytes (useful when the
// caller needs to persist the wire form byte-for-byte, e.g. mirror writes).
func (c *Client) GetSourceJSONBytes(ctx context.Context, registryURL, module, version string) ([]byte, error) {
	u, err := joinURL(registryURL, "modules", module, version, "source.json")
	if err != nil {
		return nil, err
	}
	b, err := c.getRaw(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("get source.json %s@%s: %w", module, version, err)
	}
	return b, nil
}

// ParseSourceJSON unmarshals raw source.json bytes.
func ParseSourceJSON(b []byte) (*SourceJSON, error) {
	var s SourceJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse source.json: %w", err)
	}
	return &s, nil
}

// GetModuleBazel fetches the registry's copy of MODULE.bazel for a version.
// Returns raw bytes — parsing is the caller's job.
func (c *Client) GetModuleBazel(ctx context.Context, registryURL, module, version string) ([]byte, error) {
	u, err := joinURL(registryURL, "modules", module, version, "MODULE.bazel")
	if err != nil {
		return nil, err
	}
	return c.getRaw(ctx, u)
}

// FetchArchive opens a streaming reader to the source archive named by src.
// The returned reader verifies SRI integrity on close via the embedded
// *VerifyingReader; the caller MUST call vr.Verify() after fully reading.
//
// The returned io.ReadCloser closes the underlying HTTP body when closed.
func (c *Client) FetchArchive(ctx context.Context, src *SourceJSON) (io.ReadCloser, *VerifyingReader, error) {
	if src.URL == "" {
		return nil, nil, errors.New("source.json has no url")
	}
	resp, err := c.get(ctx, src.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", src.URL, err)
	}
	vr := NewVerifyingReader(resp.Body, src.Integrity)
	rc := &verifyingCloser{body: resp.Body, vr: vr}
	return rc, vr, nil
}

// verifyingCloser ties the http.Response.Body lifecycle to the
// VerifyingReader so Close() drains and closes the underlying body.
type verifyingCloser struct {
	body io.ReadCloser
	vr   *VerifyingReader
}

func (v *verifyingCloser) Read(p []byte) (int, error) { return v.vr.Read(p) }
func (v *verifyingCloser) Close() error               { return v.body.Close() }

func (c *Client) getJSON(ctx context.Context, u string) ([]byte, error) {
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Content-Type guard: reject obviously-wrong types so a
	// misconfigured upstream or captive portal returning HTTP 200 with
	// HTML can't slip through and unmarshal as an empty JSON struct.
	// We only reject html/xml explicitly — many real BCR mirrors omit
	// Content-Type or serve JSON via Go's net/http sniff default
	// (text/plain), and json.Unmarshal will reject true garbage on its
	// own.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		mt, _, _ := strings.Cut(ct, ";")
		mt = strings.TrimSpace(strings.ToLower(mt))
		if strings.Contains(mt, "html") || strings.Contains(mt, "xml") {
			return nil, fmt.Errorf("GET %s: unexpected Content-Type %q (want JSON)", u, ct)
		}
	}
	return readAllCapped(resp.Body, MaxJSONResponseBytes)
}

func (c *Client) getRaw(ctx context.Context, u string) ([]byte, error) {
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readAllCapped(resp.Body, MaxJSONResponseBytes)
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	// Egress allowlist check — gates every outbound request through
	// fetch.Client. No-op when AllowedHosts is empty (default).
	if !c.hostAllowed(u) {
		return nil, fmt.Errorf("GET %s: %w", u, ErrEgressDenied)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "canopy/0.0")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			// Wrap so the message still reads naturally
			// ("GET <url>: registry: not found") while callers can
			// errors.Is(err, fetch.ErrNotFound).
			return nil, fmt.Errorf("GET %s: %w", u, ErrNotFound)
		}
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return resp, nil
}

// joinURL joins registry URL + path segments. Uses URL.JoinPath so
// segments are treated as raw text (no double-escaping of %-encoded
// chars) and the final String() call applies path-encoding exactly
// once. "+" in version strings stays literal (valid in URL paths;
// only special in query); spaces, if they ever appeared, would be
// escaped to %20.
//
// Module names and versions don't contain slashes in practice, so we
// don't need the segment-level escaping the previous PathEscape pass
// provided — but if a "/" did show up in a segment it would now be
// treated as a path separator. That matches the intent (any future
// schema with embedded slashes is outside canopy's BCR mapping).
func joinURL(registry string, segs ...string) (string, error) {
	base, err := url.Parse(strings.TrimRight(registry, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid registry URL %q: %w", registry, err)
	}
	return base.JoinPath(segs...).String(), nil
}
