// Package bitbucketdc implements bigorna.Forge against a self-hosted
// Bitbucket Data Center instance.
//
// Library design notes (same posture as bigorna/github):
//
//   - Auth flows through bigorna.Authorizer so the eventual Bitbucket
//     Cloud / Basic-Auth path doesn't need changes to this package.
//   - Hand-rolled HTTP, no third-party REST client.
//   - Retry policy, clock, and HTTP transport are all injectable.
//   - Errors wrap forge sentinels for errors.Is AND carry HTTPError
//     metadata accessible via errors.As.
//   - InsecureTLS is exposed as a documented helper transport rather
//     than a Config bool, so the security trade-off is visible at the
//     call site.
package bitbucketdc

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/albertocavalcante/bigorna"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	defaultUserAgent   = "bigorna-bitbucketdc/0"
)

// Client is a Bitbucket Data Center Forge implementation. Construct
// via New. Safe for concurrent use after construction.
type Client struct {
	http               *http.Client
	baseURL            string
	auth               bigorna.Authorizer
	repo               bigorna.Repo // {Owner: projectKey, Name: repoSlug}
	logger             *slog.Logger
	ua                 string
	retry              bigorna.RetryPolicy
	clock              bigorna.Clock
	rng                *mathrand.Rand
	disableIdempotency bool
}

// Config configures a Client.
type Config struct {
	// Auth is required. Use bigorna.BearerAuth(token) for an HTTP PAT;
	// bigorna.BasicAuth(user, pass) covers legacy DC deployments that
	// still use HTTP Basic. Called once per HTTP attempt.
	Auth bigorna.Authorizer

	// Repo identifies the repository: Owner is the Bitbucket project
	// key (e.g. "BAZ"), Name is the repository slug.
	Repo bigorna.Repo

	// BaseURL is the Bitbucket DC root, e.g. https://bitbucket.example.com.
	// Required — there is no default. /rest/api/1.0 is appended by
	// request methods, never by the caller.
	BaseURL string

	// HTTPClient overrides the default 30s-timeout client. Use this to
	// inject a custom transport — most commonly, InsecureTransport()
	// for corporate self-signed certs (with the security implications
	// explicitly accepted by the caller).
	HTTPClient *http.Client

	// UserAgent is sent on every request. Default
	// "bigorna-bitbucketdc/0".
	UserAgent string

	// Retry policy. Zero value → bigorna.DefaultRetryPolicy().
	Retry bigorna.RetryPolicy

	// Clock for retry backoff. Default bigorna.RealClock{}.
	Clock bigorna.Clock

	// Logger for non-fatal warnings (label-add ignored, …). Default
	// slog.Default().
	Logger *slog.Logger

	// DisableOpenPRIdempotency disables the pre-flight scan for an
	// existing open PR with the same HeadBranch. Default false. Since
	// DC has no efficient head-ref filter, the idempotency check
	// paginates open PRs — disable on very large repos where this cost
	// outweighs duplicate-PR risk.
	DisableOpenPRIdempotency bool
}

// InsecureTransport returns an http.RoundTripper that skips TLS
// certificate verification. Use this when the host process can't
// carry the corporate CA bundle that signed the DC server cert.
//
// Security: this bypasses HTTPS authentication. The connection is
// still encrypted, but a network-position adversary can impersonate
// the DC server. Only safe inside a trusted network with no path-
// crossing third parties.
//
// Pair this with a logged warning at the call site so the trade-off
// is auditable.
func InsecureTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, // documented opt-in
	}
	return t
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Auth == nil {
		return nil, errors.New("bitbucketdc: Auth is required")
	}
	if cfg.Repo.Owner == "" || cfg.Repo.Name == "" {
		return nil, errors.New("bitbucketdc: Repo.Owner (project key) and Repo.Name (repo slug) are required")
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("bitbucketdc: BaseURL is required (no default on self-hosted)")
	}
	c := &Client{
		auth:               cfg.Auth,
		repo:               cfg.Repo,
		baseURL:            strings.TrimRight(cfg.BaseURL, "/"),
		http:               cfg.HTTPClient,
		ua:                 cfg.UserAgent,
		retry:              cfg.Retry,
		clock:              cfg.Clock,
		logger:             cfg.Logger,
		disableIdempotency: cfg.DisableOpenPRIdempotency,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if c.ua == "" {
		c.ua = defaultUserAgent
	}
	if c.retry == (bigorna.RetryPolicy{}) {
		c.retry = bigorna.DefaultRetryPolicy()
	}
	if c.clock == nil {
		c.clock = bigorna.RealClock{}
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	c.rng = mathrand.New(mathrand.NewPCG(
		uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano())>>1))
	return c, nil
}

// Health verifies creds + URL by hitting /projects/<key>/repos/<slug>.
func (c *Client) Health(ctx context.Context) error {
	var out struct {
		Slug string `json:"slug"`
	}
	return c.getJSON(ctx, c.repoPath(c.repo), &out)
}

// repoPath returns /rest/api/1.0/projects/<key>/repos/<slug> with both
// segments URL-escaped — defense in depth against a misconfigured
// client config.
func (c *Client) repoPath(r bigorna.Repo) string {
	return fmt.Sprintf("/rest/api/1.0/projects/%s/repos/%s",
		url.PathEscape(r.Owner), url.PathEscape(r.Name))
}

// addRequestHeaders attaches the Authorization header, User-Agent,
// and the JSON Accept header. Called once per attempt so a rotating
// Authorizer can hand over a new credential between retries.
func (c *Client) addRequestHeaders(ctx context.Context, req *http.Request) error {
	value, err := c.auth.Authorize(ctx)
	if err != nil {
		return fmt.Errorf("bitbucketdc auth: %w", err)
	}
	if value != "" {
		req.Header.Set("Authorization", value)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	return nil
}

// doRequest is the retry+body-rewind loop. Structurally identical to
// the GitHub impl; kept duplicated because per-request bits (auth
// headers, error parsers) genuinely differ between forges. Extract to
// a shared package only when a third forge lands.
func (c *Client) doRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	maxAttempts := max(c.retry.MaxAttempts, 1)
	for attempt := range maxAttempts {
		if attempt > 0 {
			d := c.retry.BackoffFor(attempt, c.rng)
			if err := c.clock.Sleep(ctx, d); err != nil {
				return nil, err
			}
			if req.Body != nil {
				if req.GetBody == nil {
					return nil, errors.New("bitbucketdc: cannot retry request without GetBody")
				}
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("rewind body: %w", err)
				}
				req.Body = body
			}
		}
		if err := c.addRequestHeaders(ctx, req); err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			if wait <= 0 {
				wait = c.retry.BackoffFor(attempt+1, c.rng)
			}
			if wait > c.retry.MaxBackoff {
				wait = c.retry.MaxBackoff
			}
			if err := c.clock.Sleep(ctx, wait); err != nil {
				return nil, err
			}
			lastErr = fmt.Errorf("bitbucketdc: rate-limited (429)")
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			resp.Body.Close()
			lastErr = fmt.Errorf("bitbucketdc: server error %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("bitbucketdc: exhausted retries")
	}
	return nil, lastErr
}

// getJSON does a GET and decodes JSON into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.errorFromResponse(resp, "GET", path)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

// postJSON POSTs a JSON body, optionally decoding the response.
func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	bodyBytes, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}
	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.errorFromResponse(resp, "POST", path)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

// errorFromResponse builds an HTTPError that wraps the appropriate
// sentinel and surfaces the parsed DC error message when available.
func (c *Client) errorFromResponse(resp *http.Response, method, path string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var inner error
	switch resp.StatusCode {
	case http.StatusNotFound:
		inner = bigorna.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		inner = bigorna.ErrUnauthorized
	default:
		inner = fmt.Errorf("bitbucketdc: HTTP %d", resp.StatusCode)
	}
	display := truncate(string(body), 256)
	if msg := parseDCError(body); msg != "" {
		display = msg
	}
	return &bigorna.HTTPError{
		Method: method,
		Path:   path,
		Status: resp.StatusCode,
		Body:   display,
		Inner:  inner,
	}
}

// parseDCError extracts a flat message from Bitbucket DC's error
// envelope: {"errors": [{"context": ..., "message": "..."}, ...]}.
// Multiple errors are joined with "; ".
func parseDCError(body []byte) string {
	var env struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	if len(env.Errors) == 0 {
		return ""
	}
	parts := make([]string, len(env.Errors))
	for i, e := range env.Errors {
		parts[i] = e.Message
	}
	return strings.Join(parts, "; ")
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if n, err := strconv.Atoi(header); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// Compile-time check that Client satisfies bigorna.Forge lives in
// commits.go — after every interface method is defined.
