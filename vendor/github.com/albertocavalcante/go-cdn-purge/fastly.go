package cdnpurge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// defaultFastlyBaseURL is the production Fastly API base.
	// FastlyConfig.BaseURL overrides for tests + private proxies
	// (same posture as Cloudflare's BaseURL — test/proxy only).
	defaultFastlyBaseURL = "https://api.fastly.com"

	// Fastly's purge API:
	//   POST /service/<service_id>/purge/<url-encoded-url>
	// One URL per request — no batching equivalent to Cloudflare's
	// 30-URL bulk endpoint. Result: Requests == len(urls).
	//
	// Soft-purge (stale-while-revalidate behaviour) is a v0.1.x
	// addition behind a config flag; not v0.1.0.
)

// FastlyConfig configures a FastlyProvider. APIToken, ServiceID,
// and HTTPClient are required.
type FastlyConfig struct {
	// APIToken is the Fastly API token with "purge" permission
	// scoped to ServiceID. Operators should scope the token
	// narrowly per Fastly's documented permission model
	// (Engineer + purge_all/purge_select scopes).
	APIToken string

	// ServiceID is the Fastly service the URLs belong to.
	// Required — Fastly's API-form purge needs the service in
	// the path. Multi-service deployments construct multiple
	// FastlyProvider instances.
	ServiceID string

	// HTTPClient is the underlying client. Callers MUST provide
	// one; the library never constructs its own (matches the
	// portfolio's caller-wires-egress posture).
	HTTPClient *http.Client

	// BaseURL overrides the Fastly API base URL. Default
	// "https://api.fastly.com". Test-only / private-proxy-only
	// — don't override casually (same posture as Cloudflare's
	// BaseURL).
	BaseURL string
}

// FastlyProvider implements Provider against Fastly's
// /service/<service_id>/purge/<url> API.
//
// Unlike CloudflareProvider, FastlyProvider does NOT batch:
// Fastly's URL-purge API is one URL per request. A 100-URL
// Purge call produces Requests=100. Operators with high publish
// volume should consider Fastly's surrogate-key purge instead
// (which lands in v0.1.x's tag-based-purge slice).
//
// Safe for concurrent use after NewFastly returns. Per-token
// rate limits apply across goroutines.
type FastlyProvider struct {
	cfg     FastlyConfig
	baseURL string
}

// Compile-time guard.
var _ Provider = (*FastlyProvider)(nil)

// NewFastly constructs a FastlyProvider. Returns ErrInvalidOptions
// when any required field is missing.
func NewFastly(cfg FastlyConfig) (*FastlyProvider, error) {
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("%w: APIToken is required", ErrInvalidOptions)
	}
	if cfg.ServiceID == "" {
		return nil, fmt.Errorf("%w: ServiceID is required", ErrInvalidOptions)
	}
	if cfg.HTTPClient == nil {
		return nil, fmt.Errorf("%w: HTTPClient is required (caller wires egress + audit log)", ErrInvalidOptions)
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultFastlyBaseURL
	}
	return &FastlyProvider{
		cfg:     cfg,
		baseURL: strings.TrimRight(base, "/"),
	}, nil
}

// Name returns "fastly".
func (FastlyProvider) Name() string { return "fastly" }

// ServiceID returns the configured service ID. Useful for
// operator diagnostics + multi-service deployments.
func (p *FastlyProvider) ServiceID() string { return p.cfg.ServiceID }

// Purge invalidates the given URLs at Fastly's edge. Fastly's
// URL-purge API is one URL per request (no batching equivalent
// to Cloudflare's bulk endpoint), so Requests == len(deduped urls).
//
// Pre-validates URLs (same posture as CloudflareProvider): any
// malformed / empty / non-http(s) URL → ErrInvalidOptions at
// function level.
//
// Fails fast on call-level errors (rate limit, transport, 5xx)
// — remaining URLs NOT attempted. Returns partial PurgeResult
// (URLs purged before the error) alongside the error.
//
// Per-URL Fastly errors (200 with non-"ok" status) land in
// PurgeResult.Failures.
func (p *FastlyProvider) Purge(ctx context.Context, urls []string) (PurgeResult, error) {
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
	for _, u := range urls {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		callErr := p.callPurgeAPI(ctx, u, &result)
		result.Requests++
		if callErr != nil {
			// Fail-fast on call-level errors. Same posture as
			// Cloudflare provider — burning quota on subsequent
			// requests after a 429/5xx is wasteful.
			return result, callErr
		}
	}
	return result, nil
}

// callPurgeAPI issues one POST /service/<service>/purge/<encoded-url>
// and updates result with success or per-URL failure.
func (p *FastlyProvider) callPurgeAPI(ctx context.Context, targetURL string, result *PurgeResult) error {
	endpoint := fmt.Sprintf("%s/service/%s/purge/%s",
		p.baseURL, p.cfg.ServiceID, url.PathEscape(targetURL))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("cdnpurge: build request: %w", err)
	}
	req.Header.Set("Fastly-Key", p.cfg.APIToken)
	req.Header.Set("Accept", "application/json")
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: POST %s: %v", ErrServerUnavailable, endpoint, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return p.handleFastlySuccess(respBody, targetURL, result, endpoint)
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

// fastlyResponse models Fastly's single-URL purge response:
//
//	{"status": "ok", "id": "..."}
type fastlyResponse struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

// handleFastlySuccess parses a 2xx response. A response with
// `status: "ok"` puts the URL in Submitted; anything else
// (rare; Fastly typically uses non-2xx for failures) lands in
// per-URL Failures.
func (p *FastlyProvider) handleFastlySuccess(body []byte, targetURL string, result *PurgeResult, endpoint string) error {
	var resp fastlyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// Malformed response — surface as upstream-status with
		// the raw bytes for operator diagnostics.
		return fmt.Errorf("%w: %s: malformed JSON response: %v: %s",
			ErrUpstreamStatus, endpoint, err, snippet(body))
	}
	if !strings.EqualFold(resp.Status, "ok") {
		msg := "unspecified Fastly purge failure"
		if resp.Status != "" {
			msg = fmt.Sprintf("status=%q", resp.Status)
		}
		result.Failures[targetURL] = fmt.Errorf("%w: %s", ErrUpstreamStatus, msg)
		return nil
	}
	result.Submitted = append(result.Submitted, targetURL)
	return nil
}
