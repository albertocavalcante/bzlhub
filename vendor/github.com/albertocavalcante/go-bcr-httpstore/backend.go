package httpstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Backend is a read+write client for a BCR-shape HTTP store.
// The zero value is invalid; construct via New.
//
// Backend is safe for concurrent use by multiple goroutines.
// Each public method opens its own *http.Request on the
// caller-supplied client; the only shared state is the
// configured options.
type Backend struct {
	baseURL *url.URL
	auth    Auth
	http    *http.Client
	layout  Layout
	cache   Cache
}

// NewOptions is the constructor argument for New.
type NewOptions struct {
	// BaseURL is the root URL of the BCR-shape tree. The library
	// appends "/modules/<name>/..." paths to this verbatim.
	// Trailing slash is optional; the library normalises.
	BaseURL string

	// Auth supplies the request authentication scheme. Use
	// Anonymous{} for public stores; BearerAuth{Token: "..."}
	// for token-based stores. Nil is rejected — operators must
	// be explicit, even for public stores ("we chose anonymous"),
	// so the audit trail records the choice.
	Auth Auth

	// HTTP is the underlying client. Callers MUST provide one;
	// the library never constructs its own (so consumers like
	// canopy can wire their egress.Client + audit log + lint
	// check). A sensible default for non-canopy consumers:
	//
	//	&http.Client{Timeout: 30 * time.Second}
	HTTP *http.Client

	// Layout decides how ListModules / ListVersions resolve
	// against the store. Nil is rejected by New() — operators
	// must explicitly pick the substrate-appropriate Layout
	// (HTMLAutoindex for nginx/Caddy-fronted stores; custom
	// implementations for everything else). There is no universal
	// correct default for "how do I enumerate modules?"; a silent
	// default would be wrong for half the substrates.
	Layout Layout

	// Cache is an optional response cache. When non-nil, Backend
	// routes Read* methods through it with ETag-aware conditional
	// GET: hit + matching ETag returns cached body via 304; miss
	// or mismatched ETag refreshes the entry from a 200 response.
	//
	// Nil disables caching (every Read* re-fetches the body) —
	// the v0.0.5 default. Use NewMemoryCache for an in-process
	// LRU; supply your own Cache implementation for distributed
	// or persistent stores.
	//
	// One Cache instance is intended to be paired with one
	// Backend. Two Backends pointing at different upstreams MUST
	// NOT share a Cache — the cache key is relative path only,
	// with no BaseURL namespacing.
	//
	// ReadBlob bypasses the cache (streaming response, often
	// hundreds of MiB; buffering defeats the purpose). Stat and
	// Exists also bypass — they're HEAD probes with no body.
	Cache Cache
}

// New constructs a Backend. Returns ErrInvalidOptions wrapped
// with a context-bearing message when BaseURL, Auth, or HTTP is
// missing or malformed.
func New(opts NewOptions) (*Backend, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("%w: BaseURL is required", ErrInvalidOptions)
	}
	u, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: BaseURL %q is malformed: %v",
			ErrInvalidOptions, opts.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: BaseURL scheme must be http/https; got %q",
			ErrInvalidOptions, u.Scheme)
	}
	if opts.Auth == nil {
		return nil, fmt.Errorf("%w: Auth is required (use Anonymous{} for public stores)",
			ErrInvalidOptions)
	}
	if opts.HTTP == nil {
		return nil, fmt.Errorf("%w: HTTP client is required", ErrInvalidOptions)
	}
	// Normalise base URL: strip trailing slash so path joins are
	// always "<base>/<segment>" not "<base>//<segment>".
	u.Path = strings.TrimRight(u.Path, "/")

	if opts.Layout == nil {
		return nil, fmt.Errorf("%w: Layout is required (use HTMLAutoindex{} for nginx/Caddy-fronted stores, or write a Layout implementation matching your substrate)",
			ErrInvalidOptions)
	}

	return &Backend{
		baseURL: u,
		auth:    opts.Auth,
		http:    opts.HTTP,
		layout:  opts.Layout,
		cache:   opts.Cache,
	}, nil
}

// BaseURL returns the normalised root URL the Backend reads from.
// Exposed so Layout implementations + diagnostics can name where
// reads are landing.
func (b *Backend) BaseURL() *url.URL {
	// Return a defensive copy so callers can't mutate our state.
	cp := *b.baseURL
	return &cp
}

// AuthName returns the configured Auth's Name() — useful for
// audit log lines and operator diagnostics.
func (b *Backend) AuthName() string {
	return b.auth.Name()
}

// ReadIndex reads bytes at relPath under BaseURL. Designed for
// Layout implementations that need to fetch their listing files
// (e.g. an autoindex HTML page, a JSON index, etc.). Routes
// through the configured Cache so Layout's index also benefits
// from ETag-aware caching.
//
// Returns ErrUpstream404-wrapped error on 404 (Layout impls map to
// the appropriate BCR sentinel themselves), ErrUpstreamStatus on
// other non-2xx, body bytes on 2xx — same semantics as the
// BCR-typed Read* methods but path-generic.
//
// This is Backend's implementation of the Reader interface that
// Layout receives.
func (b *Backend) ReadIndex(ctx context.Context, relPath string) ([]byte, error) {
	return b.getBytes(ctx, relPath)
}

// Compile-time guard: Backend implements Reader.
var _ Reader = (*Backend)(nil)

// ---- internal: shared transport ---------------------------------

// ErrUpstream404 is the path-generic 404 sentinel that Backend's
// getBytes / Stat / ReadIndex return on a 404 response. BCR-typed
// public methods (ReadMetadataJSON, ReadSourceJSON, etc.) translate
// it to the operator-meaningful ErrModuleNotFound /
// ErrVersionNotFound / ErrPatchNotFound based on the caller's intent.
//
// External Layout implementations use this to detect "the index
// file is missing" cases — see e.g. the artifactory package's
// ListModules soft-fail-on-404 contract.
var ErrUpstream404 = errors.New("httpstore: upstream 404")

// do builds and executes one request against baseURL + relPath.
// Returns the response (caller closes Body) plus the full URL used
// (for error wrapping). Auth is applied; per-call headers can carry
// If-None-Match, If-Match, Content-Type, etc. body is optional —
// nil means no request body (GET/HEAD).
//
// Internal — public methods translate response status (304 / 404 /
// non-2xx) into the appropriate BCR-shaped sentinel based on
// caller intent. do() itself is status-neutral except for transport
// errors.
//
// Callers MUST close the returned Response.Body when err is nil.
func (b *Backend) do(
	ctx context.Context,
	method, relPath string,
	headers http.Header,
	body io.Reader,
) (*http.Response, *url.URL, error) {
	return b.doWithQuery(ctx, method, relPath, nil, headers, body)
}

// doWithQuery is do() plus URL query-string support. Internal —
// the public Do method wraps this with a cleaner return shape.
// query is nil-safe; empty Values produces no "?..." suffix.
func (b *Backend) doWithQuery(
	ctx context.Context,
	method, relPath string,
	query url.Values,
	headers http.Header,
	body io.Reader,
) (*http.Response, *url.URL, error) {
	u := *b.baseURL
	if relPath != "" {
		u.Path = u.Path + "/" + strings.TrimLeft(relPath, "/")
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, &u, fmt.Errorf("httpstore: build request: %w", err)
	}
	if err := b.auth.Apply(req); err != nil {
		return nil, &u, fmt.Errorf("httpstore: apply auth %s: %w", b.auth.Name(), err)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, &u, fmt.Errorf("httpstore: %s %s: %w", method, u.String(), err)
	}
	return resp, &u, nil
}

// Do issues an HTTP request to <BaseURL>/<relPath>?<query> with
// the given method, headers, and optional body. Auth is applied.
// The configured Cache is NOT consulted — cache routing is
// reserved for the BCR-typed Read* methods, which understand the
// path shapes that are safe to cache.
//
// Designed for extension libraries (vendor adapters with non-BCR
// path shapes or vendor-specific endpoints) that need to issue
// requests through the Backend's configured auth + HTTP client
// without re-implementing transport. See go-bcr-artifactory's
// Properties type for the canonical use case.
//
// query is nil-safe — empty Values produces no "?..." suffix on
// the URL. headers is nil-safe. body is nil for GET/HEAD/DELETE.
//
// Returns the underlying transport error (build / send) on
// failure. Non-2xx responses are NOT translated into typed
// sentinels here; Do is status-neutral. The caller inspects
// resp.StatusCode and maps to its own sentinels.
//
// Caller MUST close the returned Response.Body when err is nil.
func (b *Backend) Do(
	ctx context.Context,
	method, relPath string,
	query url.Values,
	headers http.Header,
	body io.Reader,
) (*http.Response, error) {
	resp, _, err := b.doWithQuery(ctx, method, relPath, query, headers, body)
	return resp, err
}

// cacheLookup is a nil-safe wrapper around b.cache.Get — returns
// (zero Entry, false) when no cache is configured.
func (b *Backend) cacheLookup(ctx context.Context, relPath string) (Entry, bool) {
	if b.cache == nil {
		return Entry{}, false
	}
	return b.cache.Get(ctx, relPath)
}

// cacheStore is a nil-safe wrapper around b.cache.Put — builds an
// Entry from the response headers and body. No-op when no cache is
// configured.
func (b *Backend) cacheStore(ctx context.Context, relPath string, body []byte, resp *http.Response) {
	if b.cache == nil {
		return
	}
	entry := Entry{
		Body: body,
		ETag: resp.Header.Get("ETag"),
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, perr := http.ParseTime(lm); perr == nil {
			entry.LastModified = t
		}
	}
	b.cache.Put(ctx, relPath, entry)
}

// contentPath prepends the configured Layout's ContentPathPrefix to
// relPath so content reads + writes land at
// `<BaseURL>/<prefix>/<relPath>`. Empty prefix (the autoindex /
// CanopyIndex case) returns relPath unchanged.
//
// Used by every Read* and Write* method in read.go / write.go.
// Layout-driven URL construction (ReadIndex, Stat, Exists) does NOT
// go through this — Layouts encode their own paths (e.g., Artifactory's
// `/api/storage/<repo>/...`) and need no further prefixing.
func (b *Backend) contentPath(relPath string) string {
	prefix := strings.Trim(b.layout.ContentPathPrefix(), "/")
	if prefix == "" {
		return relPath
	}
	return path.Join(prefix, relPath)
}

// getBytes is the workhorse cached GET — consults the cache,
// issues a conditional GET when there's a hit with an ETag,
// handles 304 / 404 / non-2xx, and stores fresh 200 responses
// in the cache. Internal-only; layout implementations use it
// via the Backend pointer they're passed.
//
// Cache flow (when b.cache != nil):
//   - cache miss: plain GET, store the 200 response under relPath.
//   - cache hit + ETag: GET with If-None-Match; 304 returns the
//     stored body unchanged, 200 refreshes the entry.
//   - cache hit + no ETag: plain GET; refresh the entry on 200.
//   - 404 leaves the cache untouched (BCR content is immutable
//     per version — same key shouldn't 404 next call; if it does,
//     the operator removed an entry and the stale cache is the
//     least of their worries).
//   - non-2xx-non-404 surfaces unchanged; cache untouched.
func (b *Backend) getBytes(ctx context.Context, relPath string) ([]byte, error) {
	cached, hit := b.cacheLookup(ctx, relPath)
	var headers http.Header
	if hit && cached.ETag != "" {
		headers = http.Header{"If-None-Match": []string{cached.ETag}}
	}
	resp, u, err := b.do(ctx, http.MethodGet, relPath, headers, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		if !hit {
			return nil, fmt.Errorf("%w: GET %s -> 304 without prior cache hit",
				ErrUpstreamStatus, u.String())
		}
		return cached.Body, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrUpstream404
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: GET %s -> %d %s",
			ErrUpstreamStatus, u.String(), resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("httpstore: read body from %s: %w", u.String(), err)
	}
	b.cacheStore(ctx, relPath, body, resp)
	return body, nil
}
