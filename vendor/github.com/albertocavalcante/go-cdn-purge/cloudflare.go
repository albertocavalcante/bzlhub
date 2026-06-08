package cdnpurge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// defaultCloudflareBaseURL is the production Cloudflare API
	// base. CloudflareConfig.BaseURL overrides for tests + private
	// proxies (per re-review Δ9 — operators don't override casually).
	defaultCloudflareBaseURL = "https://api.cloudflare.com/client/v4"

	// defaultMaxURLsPerRequest matches Cloudflare's free-tier
	// limit for /purge_cache. Paid tiers can go up to 500 — set
	// CloudflareConfig.MaxURLsPerRequest explicitly.
	defaultMaxURLsPerRequest = 30
)

// CloudflareConfig configures a CloudflareProvider. APIToken,
// ZoneID, and HTTPClient are required; the rest have defaults.
type CloudflareConfig struct {
	// APIToken is the Cloudflare API token with "Zone — Cache
	// Purge — Purge" permission scoped to ZoneID. Operators
	// should scope the token narrowly per Cloudflare's
	// documented permission model.
	APIToken string

	// ZoneID is the Cloudflare zone the URLs belong to.
	// Cloudflare doesn't accept cross-zone purges in one
	// request; multi-zone deployments construct multiple
	// CloudflareProvider instances.
	ZoneID string

	// HTTPClient is the underlying client. Callers MUST provide
	// one; the library never constructs its own (matches
	// httpstore's posture — caller wires its egress + audit log
	// + lint check).
	HTTPClient *http.Client

	// MaxURLsPerRequest caps the number of URLs in a single
	// Cloudflare API request. Cloudflare's documented limits:
	//   - Free tier: 30
	//   - Pro / Business / Enterprise: 500
	//
	// Default 30 (free-tier safe). Operators on paid tiers can
	// increase to reduce API request count.
	MaxURLsPerRequest int

	// BaseURL overrides the Cloudflare API base URL.
	// Default "https://api.cloudflare.com/client/v4".
	//
	// DO NOT override casually — pointing at a non-Cloudflare
	// endpoint will silently misbehave. Designed for:
	//   - Test mocks via httptest.Server
	//   - Operators proxying Cloudflare API through a corporate
	//     egress gateway
	// If you don't have one of those use cases, leave it empty.
	BaseURL string
}

// CloudflareProvider implements Provider against Cloudflare's
// /zones/{ZoneID}/purge_cache endpoint.
//
// Safe for concurrent use after NewCloudflare returns. Note that
// per-token rate limits apply across goroutines — if operators
// need rate-limit coordination, wrap a single Provider in a
// serialised queue at the call site (re-review Δ5).
type CloudflareProvider struct {
	cfg     CloudflareConfig
	baseURL string
	maxURLs int
}

// Compile-time guard.
var _ Provider = (*CloudflareProvider)(nil)

// NewCloudflare constructs a CloudflareProvider. Validates that
// APIToken, ZoneID, and HTTPClient are set; returns
// ErrInvalidOptions otherwise. Empty BaseURL defaults to
// Cloudflare's production endpoint; empty MaxURLsPerRequest
// defaults to 30 (free-tier safe).
func NewCloudflare(cfg CloudflareConfig) (*CloudflareProvider, error) {
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("%w: APIToken is required", ErrInvalidOptions)
	}
	if cfg.ZoneID == "" {
		return nil, fmt.Errorf("%w: ZoneID is required", ErrInvalidOptions)
	}
	if cfg.HTTPClient == nil {
		return nil, fmt.Errorf("%w: HTTPClient is required (caller wires egress + audit log)", ErrInvalidOptions)
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultCloudflareBaseURL
	}
	max := cfg.MaxURLsPerRequest
	if max <= 0 {
		max = defaultMaxURLsPerRequest
	}
	return &CloudflareProvider{
		cfg:     cfg,
		baseURL: strings.TrimRight(base, "/"),
		maxURLs: max,
	}, nil
}

// Name returns "cloudflare".
func (CloudflareProvider) Name() string { return "cloudflare" }

// ZoneID returns the configured zone ID. Useful for operator
// diagnostics + multi-zone deployments.
func (p *CloudflareProvider) ZoneID() string { return p.cfg.ZoneID }

// MaxURLsPerRequest returns the configured batch limit.
func (p *CloudflareProvider) MaxURLsPerRequest() int { return p.maxURLs }

// Purge invalidates the given URLs at Cloudflare's edge.
//
// Pre-validates URLs (per re-review Δ2): any malformed / empty /
// non-http(s) URL → ErrInvalidOptions at function level (whole-
// batch reject — caller scrubs their URL list). Saves rate-limit
// quota on caller-side bugs.
//
// Auto-batches into ceil(len(urls) / MaxURLsPerRequest) API
// calls. Each call POSTs JSON to /zones/<ZoneID>/purge_cache
// with body {"files": [...]}.
//
// On call-level failure (rate limit, transport, 5xx), fails
// fast — remaining batches NOT attempted. Returns partial
// PurgeResult (URLs from successful batches in Submitted)
// alongside the error.
//
// On per-URL Cloudflare errors (rare; returned as part of a 200
// response's `errors` array), per-URL entries land in
// PurgeResult.Failures while the function-level error stays nil.
func (p *CloudflareProvider) Purge(ctx context.Context, urls []string) (PurgeResult, error) {
	if err := validateURLs(urls); err != nil {
		return PurgeResult{}, err
	}
	urls = dedupeURLs(urls)
	result := PurgeResult{
		Failures: map[string]error{},
	}
	if len(urls) == 0 {
		return result, nil
	}
	for i := 0; i < len(urls); i += p.maxURLs {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		end := i + p.maxURLs
		if end > len(urls) {
			end = len(urls)
		}
		batch := urls[i:end]
		callErr := p.callPurgeAPI(ctx, batch, &result)
		result.Requests++
		if callErr != nil {
			// Fail-fast on call-level errors. Don't attempt
			// remaining batches. Caller decides whether to retry.
			return result, callErr
		}
	}
	return result, nil
}

// callPurgeAPI issues one POST /zones/<zone>/purge_cache request
// and updates result with successes + per-URL failures.
func (p *CloudflareProvider) callPurgeAPI(ctx context.Context, batch []string, result *PurgeResult) error {
	body := map[string]any{"files": batch}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("cdnpurge: marshal purge body: %w", err)
	}
	endpoint := fmt.Sprintf("%s/zones/%s/purge_cache", p.baseURL, p.cfg.ZoneID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cdnpurge: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: POST %s: %v", ErrServerUnavailable, endpoint, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return p.handleSuccess(respBody, batch, result, endpoint)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s: %s", ErrUnauthorized, endpoint, snippet(respBody))
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s: %s", ErrForbidden, endpoint, snippet(respBody))
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s: %s", ErrRateLimited, endpoint, snippet(respBody))
	default:
		return fmt.Errorf("%w: %s -> %d %s: %s",
			ErrUpstreamStatus, endpoint, resp.StatusCode, resp.Status, snippet(respBody))
	}
}

// cloudflareResponse models Cloudflare's purge response shape.
// Fields we care about: success bool + errors array (may carry
// per-URL failures even on 200 OK — re-review Δ6 flagged this
// shape as worth verifying against live Cloudflare API; v0.0.1
// parses the documented shape and will adjust in v0.0.2 if
// real-world responses diverge).
type cloudflareResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// handleSuccess parses the response body of a 2xx Cloudflare
// response and updates result. Per-file errors (Code != 0) land
// in result.Failures keyed by the offending URL; everything else
// in result.Submitted.
//
// Cloudflare's error-response shape doesn't always indicate WHICH
// URL failed in the message — v0.0.1 conservatively treats any
// errors array entry as "the whole batch had a problem", marking
// every batch URL as a Failure. Refinement to per-URL targeting
// is a v0.0.2 follow-up when real-world responses are observed
// (per re-review Δ6).
func (p *CloudflareProvider) handleSuccess(body []byte, batch []string, result *PurgeResult, endpoint string) error {
	var resp cloudflareResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// Malformed response — surface as upstream-status with the
		// raw bytes for operator diagnostics.
		return fmt.Errorf("%w: %s: malformed JSON response: %v: %s",
			ErrUpstreamStatus, endpoint, err, snippet(body))
	}
	// Cloudflare's success-with-errors edge case: HTTP 200 but
	// success: false and an errors array. Treat as per-URL
	// failures across the batch.
	if !resp.Success {
		msg := "unspecified Cloudflare error"
		if len(resp.Errors) > 0 {
			msg = fmt.Sprintf("code=%d: %s", resp.Errors[0].Code, resp.Errors[0].Message)
		}
		for _, u := range batch {
			result.Failures[u] = fmt.Errorf("%w: %s", ErrUpstreamStatus, msg)
		}
		return nil
	}
	// Fully successful batch.
	result.Submitted = append(result.Submitted, batch...)
	return nil
}

// validateURLs pre-validates the URL list per re-review Δ2.
// Returns ErrInvalidOptions for any empty / whitespace-only /
// malformed / non-http(s) URL. Whole-batch reject — don't
// silently send some-valid-some-invalid (Cloudflare would burn
// quota on the invalid ones).
func validateURLs(urls []string) error {
	for i, u := range urls {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("%w: URL at index %d is empty/whitespace", ErrInvalidOptions, i)
		}
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("%w: URL at index %d %q is malformed: %v", ErrInvalidOptions, i, u, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("%w: URL at index %d %q must be http/https; got scheme %q",
				ErrInvalidOptions, i, u, parsed.Scheme)
		}
		if parsed.Host == "" {
			return fmt.Errorf("%w: URL at index %d %q has empty host", ErrInvalidOptions, i, u)
		}
	}
	return nil
}

// dedupeURLs returns urls with duplicates removed, preserving
// first-occurrence order. Case-sensitive byte equality per
// re-review Δ8 (URLs are case-sensitive per RFC 3986; operators
// wanting hostname normalization do it before passing to Purge).
func dedupeURLs(urls []string) []string {
	if len(urls) <= 1 {
		return urls
	}
	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

// snippet returns a truncated view of a byte slice for inclusion
// in error messages. Caps at 200 bytes; replaces newlines with
// spaces for log-line cleanliness.
func snippet(b []byte) string {
	const max = 200
	s := strings.ReplaceAll(string(b), "\n", " ")
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
