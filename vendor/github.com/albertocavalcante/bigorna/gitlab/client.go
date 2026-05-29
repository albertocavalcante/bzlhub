// Package gitlab implements bigorna.Forge against gitlab.com (or
// self-hosted GitLab, via BaseURL).
//
// Library design notes:
//
//   - Auth flows through bigorna.Authorizer. GitLab accepts personal
//     access tokens via either the PRIVATE-TOKEN header or
//     Authorization: Bearer. We use Bearer to keep parity with the
//     github and bitbucketdc clients — one wire path, one auth model.
//
//   - REST v4 only. GitLab has GraphQL but its REST surface is complete
//     for our use cases (MRs, commits, notes) and keeps wire decode
//     code uniform across forges.
//
//   - Project ID encoding: GitLab paths accept either numeric ID or the
//     URL-encoded "owner/name". We use the encoded form so callers
//     don't need a pre-resolution step.
//
//   - ETag quirk: gitlab.com's commits endpoint honors If-None-Match
//     but returns 200 with an empty body instead of 304. The client
//     maps both signals to notModified=true so callers can rely on the
//     bigorna contract.
package gitlab

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
	defaultBaseURL     = "https://gitlab.com"
	defaultHTTPTimeout = 30 * time.Second
	defaultUserAgent   = "bigorna-gitlab/0"
)

// Client is a GitLab Forge implementation. Construct via New. Safe for
// concurrent use after construction.
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

	// Repo identifies the registry project. Owner is the namespace
	// path (group, subgroup, or user), Name is the project path.
	Repo bigorna.Repo

	// BaseURL overrides https://gitlab.com (for self-hosted). Must not
	// include /api/v4 — the client appends it.
	BaseURL string

	// HTTPClient overrides the default 30s-timeout client.
	HTTPClient *http.Client

	// UserAgent is sent on every request. Default: "bigorna-gitlab/0".
	UserAgent string

	// Retry is the retry policy. Zero value → DefaultRetryPolicy.
	Retry bigorna.RetryPolicy

	// Clock for retry backoff. Default: bigorna.RealClock{}.
	Clock bigorna.Clock

	// Logger receives structured warnings. Default: slog.Default().
	Logger *slog.Logger

	// DisableOpenPRIdempotency disables the pre-flight check for an
	// existing open MR with the same source branch. Default false.
	DisableOpenPRIdempotency bool
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Auth == nil {
		return nil, errors.New("gitlab: Auth is required")
	}
	if cfg.Repo.Owner == "" || cfg.Repo.Name == "" {
		return nil, errors.New("gitlab: Repo.Owner and Repo.Name are required")
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
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
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

// Health does a no-op GET /api/v4/projects/:id to verify creds + URL.
func (c *Client) Health(ctx context.Context) error {
	var p struct {
		PathWithNamespace string `json:"path_with_namespace"`
	}
	_, _, err := c.getJSON(ctx, c.projectPath(), "", &p)
	return err
}

// projectPath returns /api/v4/projects/<url-escaped owner/name>.
// GitLab accepts both numeric ID and URL-encoded path; the path form
// avoids a pre-resolution step.
func (c *Client) projectPath() string {
	return "/api/v4/projects/" + url.PathEscape(c.repo.Owner+"/"+c.repo.Name)
}

func (c *Client) addRequestHeaders(ctx context.Context, req *http.Request) error {
	value, err := c.auth.Authorize(ctx)
	if err != nil {
		return fmt.Errorf("gitlab auth: %w", err)
	}
	if value != "" {
		req.Header.Set("Authorization", value)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	return nil
}

// doRequest sends a request with retry-on-transient-error semantics.
// Honors Retry-After on 429. 5xx retries, 4xx returns as-is.
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
					return nil, errors.New("gitlab: cannot retry request without GetBody")
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
			lastErr = fmt.Errorf("gitlab: rate-limited (429)")
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			resp.Body.Close()
			lastErr = fmt.Errorf("gitlab: server error %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("gitlab: exhausted retries")
	}
	return nil, lastErr
}

// getJSON does a GET, conditionally with If-None-Match if etag is non-
// empty. Returns (newETag, raw response bytes, error). The raw bytes
// let callers detect GitLab's "200 + empty body" not-modified signal,
// which differs from the standard 304.
//
// On 304 it returns errNotModified with the original etag echoed back.
func (c *Client) getJSON(ctx context.Context, path, etag string, out any) (newETag string, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return "", nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", path, err)
		}
		if out != nil && len(body) > 0 {
			if err := json.Unmarshal(body, out); err != nil {
				return "", nil, fmt.Errorf("decode %s: %w", path, err)
			}
		}
		return resp.Header.Get("ETag"), body, nil
	case http.StatusNotModified:
		return etag, nil, errNotModified
	}
	return "", nil, c.errorFromResponse(resp, "GET", path)
}

// postJSON does a POST with a JSON body. If out is non-nil, the
// response body is decoded into it.
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
		inner = fmt.Errorf("gitlab: HTTP %d", resp.StatusCode)
	}
	return &bigorna.HTTPError{
		Method: method,
		Path:   path,
		Status: resp.StatusCode,
		Body:   truncate(string(body), 256),
		Inner:  inner,
	}
}

var errNotModified = errors.New("not modified")

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
