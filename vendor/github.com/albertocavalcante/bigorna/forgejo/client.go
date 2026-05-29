// Package forgejo implements bigorna.Forge against Forgejo instances
// (e.g., codeberg.org, self-hosted Forgejo).
//
// Forgejo's REST API lives under /api/v1/ and is similar in shape to
// Gitea's, but this package targets Forgejo specifically — Gitea
// compatibility is incidental, not a contract. Differences from the
// github / gitlab clients (all verified against codeberg.org):
//
//   - BaseURL is required. There is no canonical Forgejo.com; each
//     instance is independent.
//
//   - The pulls list endpoint silently ignores the documented `head`
//     filter. OpenPR idempotency therefore walks open PRs and filters
//     client-side by head.ref. Pagination is capped to avoid pathological
//     scans on registries with many open MRs.
//
//   - Forgejo's commits endpoint does NOT expose ETag / If-None-Match.
//     ListNewCommits walks commits newest-first and stops at sinceSHA;
//     when the newest remote commit equals sinceSHA, the result is
//     empty and the client returns notModified=true (echoing the input
//     etag so the bigorna contract holds).
//
//   - Label IDs on PR create are integers, not names. Forgejo's
//     add-labels endpoint also wants IDs. Rather than add a per-call
//     "lookup labels by name" round trip, MVP skips label-add and
//     logs a warning when opts.Labels is non-empty — matching the
//     bitbucketdc client's no-label policy.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/albertocavalcante/bigorna"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	defaultUserAgent   = "bigorna-forgejo/0"
)

// Client is a Forgejo Forge implementation. Construct via New. Safe
// for concurrent use after construction.
type Client struct {
	http               *http.Client
	baseURL            string
	auth               bigorna.Authorizer
	repo               bigorna.Repo
	logger             *slog.Logger
	ua                 string
	retry              bigorna.RetryPolicy
	clock              bigorna.Clock
	rng                *mathrand.Rand
	disableIdempotency bool
}

// Config configures a Client.
type Config struct {
	// Auth is required. Use bigorna.BearerAuth(token) for a PAT.
	Auth bigorna.Authorizer

	// Repo identifies the registry repository.
	Repo bigorna.Repo

	// BaseURL is required (e.g., "https://codeberg.org" or a self-
	// hosted instance). Must not include /api/v1 — the client appends
	// it on every request.
	BaseURL string

	HTTPClient *http.Client
	UserAgent  string
	Retry      bigorna.RetryPolicy
	Clock      bigorna.Clock
	Logger     *slog.Logger

	// DisableOpenPRIdempotency disables the pre-flight scan for an
	// existing open PR with the same head branch.
	DisableOpenPRIdempotency bool
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Auth == nil {
		return nil, errors.New("forgejo: Auth is required")
	}
	if cfg.Repo.Owner == "" || cfg.Repo.Name == "" {
		return nil, errors.New("forgejo: Repo.Owner and Repo.Name are required")
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("forgejo: BaseURL is required (no canonical instance)")
	}
	c := &Client{
		auth:               cfg.Auth,
		repo:               cfg.Repo,
		baseURL:            cfg.BaseURL,
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

// Health does a no-op GET on the repo endpoint to verify creds + URL.
func (c *Client) Health(ctx context.Context) error {
	var repo struct {
		FullName string `json:"full_name"`
	}
	_, err := c.getJSON(ctx, c.repoBasePath(), &repo)
	return err
}

// repoBasePath returns /api/v1/repos/<owner>/<name> with both segments
// URL-escaped — defense in depth against an exotic repo name landing
// in a client config.
func (c *Client) repoBasePath() string {
	return "/api/v1/repos/" + url.PathEscape(c.repo.Owner) + "/" + url.PathEscape(c.repo.Name)
}

func (c *Client) addRequestHeaders(ctx context.Context, req *http.Request) error {
	value, err := c.auth.Authorize(ctx)
	if err != nil {
		return fmt.Errorf("forgejo auth: %w", err)
	}
	if value != "" {
		req.Header.Set("Authorization", value)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	return nil
}

// doRequest sends a request with retry-on-transient-error semantics.
// 5xx → retry. 429 → respect Retry-After. 4xx (except 429) → return.
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
					return nil, errors.New("forgejo: cannot retry request without GetBody")
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
			lastErr = fmt.Errorf("forgejo: rate-limited (429)")
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			resp.Body.Close()
			lastErr = fmt.Errorf("forgejo: server error %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("forgejo: exhausted retries")
	}
	return nil, lastErr
}

// getJSON does a GET and decodes the JSON body into out (if non-nil).
// Unlike the github / gitlab clients, no If-None-Match is sent —
// Forgejo's endpoints (verified against codeberg.org) do not expose
// ETag headers, so conditional GET is not applicable.
func (c *Client) getJSON(ctx context.Context, path string, out any) (status int, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, c.errorFromResponse(resp, "GET", path)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// postJSON does a POST with a JSON body. If out is non-nil, decodes
// the response body into it.
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

func (c *Client) errorFromResponse(resp *http.Response, method, path string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var inner error
	switch resp.StatusCode {
	case http.StatusNotFound:
		inner = bigorna.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		inner = bigorna.ErrUnauthorized
	default:
		inner = fmt.Errorf("forgejo: HTTP %d", resp.StatusCode)
	}
	return &bigorna.HTTPError{
		Method: method,
		Path:   path,
		Status: resp.StatusCode,
		Body:   truncate(string(body), 256),
		Inner:  inner,
	}
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
