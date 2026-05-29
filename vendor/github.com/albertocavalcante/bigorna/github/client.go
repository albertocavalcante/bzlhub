// Package github implements bigorna.Forge against github.com (or GitHub
// Enterprise Server, via BaseURL).
//
// Library design notes:
//
//   - Auth flows through bigorna.Authorizer so non-Bearer schemes
//     (Bitbucket Cloud's Basic, future SigV4) plug in without changes
//     to this package.
//
//   - Hand-rolled HTTP — no go-github. The endpoint surface is small
//     and the retry / rate-limit / error semantics are tuned for
//     narrow PR-driven workflows.
//
//   - Retry policy, clock, and HTTP transport are all injectable.
//     Production code uses sensible defaults; tests use a manual clock
//     and a captured-response httptest.Server.
//
//   - Bodies built from bytes.Reader auto-populate req.GetBody. POST
//     paths also set GetBody explicitly as belt-and-suspenders so a
//     future change to bytes.Buffer doesn't silently break retries.
//
//   - Errors wrap a sentinel (bigorna.ErrNotFound / ErrUnauthorized) so
//     `errors.Is` works, AND carry method/path/status/body in an
//     HTTPError type accessible via `errors.As`.
package github

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
	"strings"
	"time"

	"github.com/albertocavalcante/bigorna"
)

const (
	defaultBaseURL    = "https://api.github.com"
	defaultGraphQLURL = "https://api.github.com/graphql"

	defaultHTTPTimeout = 30 * time.Second
	defaultUserAgent   = "bigorna-github/0"

	// rateLimitFloor is the X-RateLimit-Remaining value below which we
	// preemptively sleep until X-RateLimit-Reset. 50 is "burn the last
	// 50 in normal operation; pause to recover from there." Per-request
	// override is not exposed — operators tune their PAT budget instead.
	rateLimitFloor = 50
)

// Client is a github.com (or GHES, when BaseURL is set) Forge
// implementation. Construct via New. Safe for concurrent use after
// construction — all fields are either read-only or protected by
// mutexes.
type Client struct {
	http       *http.Client
	baseURL    string
	graphqlURL string
	auth       bigorna.Authorizer
	repo       bigorna.Repo
	logger     *slog.Logger
	ua         string
	retry      bigorna.RetryPolicy
	clock      bigorna.Clock
	rng        *mathrand.Rand
	disableIdempotency bool

	// nodeID memoizes the GraphQL node ID for the configured repo,
	// avoiding a round-trip on every OpenPR call.
	nodeID repoNodeIDCache
}

// Config configures a Client.
type Config struct {
	// Auth is required. Use bigorna.BearerAuth(token) for a fixed env-
	// var PAT; implement Authorizer directly for rotating credentials
	// (Vault, KMS, GitHub App installation tokens). Called once per
	// HTTP attempt.
	Auth bigorna.Authorizer

	// Repo identifies the registry repository. Required.
	Repo bigorna.Repo

	// BaseURL overrides https://api.github.com (for GitHub Enterprise
	// Server). Empty means github.com.
	BaseURL string

	// GraphQLURL overrides the GraphQL endpoint. Empty derives it from
	// BaseURL (api.github.com → /graphql; for GHES the operator sets
	// this explicitly, since GHES uses /api/graphql).
	GraphQLURL string

	// HTTPClient overrides the default 30s-timeout client. Use this to
	// inject a custom transport for corporate CA bundles, proxies, or
	// observability wrappers.
	HTTPClient *http.Client

	// UserAgent is sent on every request. GitHub rejects clients with
	// no User-Agent. Default: "bigorna-github/0". Override to
	// identify your tool downstream.
	UserAgent string

	// Retry is the retry policy for HTTP-level failures (5xx, 429,
	// network errors). Zero value → bigorna.DefaultRetryPolicy().
	Retry bigorna.RetryPolicy

	// Clock is used for retry backoff. Tests inject a manual clock for
	// fast retry exercises. Default: bigorna.RealClock{}.
	Clock bigorna.Clock

	// Logger receives structured warnings (label-add failure, rate-
	// limit preemptive sleep). Default: slog.Default().
	Logger *slog.Logger

	// DisableOpenPRIdempotency disables the pre-flight check that
	// looks for an existing open PR with the same HeadBranch before
	// creating. Default is false (idempotency on). Disable only when
	// the extra API call is unaffordable AND duplicates are tolerable.
	DisableOpenPRIdempotency bool
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Auth == nil {
		return nil, errors.New("github: Auth is required")
	}
	if cfg.Repo.Owner == "" || cfg.Repo.Name == "" {
		return nil, errors.New("github: Repo.Owner and Repo.Name are required")
	}
	c := &Client{
		auth:               cfg.Auth,
		repo:               cfg.Repo,
		baseURL:            cfg.BaseURL,
		graphqlURL:         cfg.GraphQLURL,
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
	if c.graphqlURL == "" {
		c.graphqlURL = defaultGraphQLURL
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
	// Per-Client RNG seeded from a non-deterministic source. We don't
	// need crypto-quality jitter — just enough independence between
	// client instances to break thundering herds.
	c.rng = mathrand.New(mathrand.NewPCG(
		uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano())>>1))
	return c, nil
}

// Health does a no-op GET /repos/{owner}/{name} to verify creds + URL.
// Call at host-process startup to fail fast on misconfiguration.
func (c *Client) Health(ctx context.Context) error {
	var repo struct {
		FullName string `json:"full_name"`
	}
	_, err := c.getJSON(ctx, c.repoBasePath(), "", &repo)
	return err
}

// repoBasePath returns /repos/<owner>/<name> with both segments URL-
// escaped — defense in depth against a misconfigured client config.
func (c *Client) repoBasePath() string {
	return "/repos/" + url.PathEscape(c.repo.Owner) + "/" + url.PathEscape(c.repo.Name)
}

// addRequestHeaders attaches the Authorization header, User-Agent,
// and the JSON Accept/Version headers. Called once per attempt inside
// doRequest so a rotating Authorizer can hand over a new credential
// between retries.
func (c *Client) addRequestHeaders(ctx context.Context, req *http.Request) error {
	value, err := c.auth.Authorize(ctx)
	if err != nil {
		return fmt.Errorf("github auth: %w", err)
	}
	if value != "" {
		req.Header.Set("Authorization", value)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.ua)
	return nil
}

// doRequest sends a request with retry-on-transient-error semantics.
// Honors Retry-After. Pre-emptively slows down when GitHub's X-RateLimit-
// Remaining drops below the floor.
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
					return nil, errors.New("github: cannot retry request without GetBody")
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
		// 429: respect Retry-After explicitly.
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
			lastErr = fmt.Errorf("github: rate-limited (429)")
			continue
		}
		// 5xx: retry. 4xx (except 429): return as-is for inspection.
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			resp.Body.Close()
			lastErr = fmt.Errorf("github: server error %d", resp.StatusCode)
			continue
		}
		// Preemptive rate-limit slowdown: if remaining is low, sleep
		// until reset before returning the response. The caller still
		// sees the original 2xx — we just don't queue another request
		// right behind it.
		c.maybePreemptiveSlowdown(ctx, resp.Header)
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("github: exhausted retries")
	}
	return nil, lastErr
}

// maybePreemptiveSlowdown inspects X-RateLimit-Remaining / -Reset and,
// if remaining is below the floor, sleeps until reset (capped by
// MaxBackoff). Best-effort — failures to parse the header silently
// skip the slowdown.
func (c *Client) maybePreemptiveSlowdown(ctx context.Context, h http.Header) {
	remStr := h.Get("X-RateLimit-Remaining")
	resetStr := h.Get("X-RateLimit-Reset")
	if remStr == "" || resetStr == "" {
		return
	}
	rem, err := strconv.Atoi(remStr)
	if err != nil || rem >= rateLimitFloor {
		return
	}
	resetUnix, err := strconv.ParseInt(resetStr, 10, 64)
	if err != nil {
		return
	}
	until := time.Until(time.Unix(resetUnix, 0))
	if until <= 0 {
		return
	}
	if until > c.retry.MaxBackoff {
		until = c.retry.MaxBackoff
	}
	c.logger.Warn("github: rate-limit floor reached; preemptive slowdown",
		"remaining", rem, "sleep", until.String())
	_ = c.clock.Sleep(ctx, until)
}

// getJSON does a GET, conditionally with If-None-Match if etag is non-
// empty. On 304 it returns notModified=true and the original etag.
// On 200 it decodes into out and returns the new etag.
func (c *Client) getJSON(ctx context.Context, path, etag string, out any) (newETag string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.doRequest(ctx, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return "", fmt.Errorf("decode %s: %w", path, err)
			}
		}
		return resp.Header.Get("ETag"), nil
	case http.StatusNotModified:
		return etag, errNotModified
	}
	return "", c.errorFromResponse(resp, "GET", path)
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
	// Explicit GetBody — belt-and-suspenders. bytes.NewReader already
	// sets this via http.NewRequest, but pinning it locally protects
	// against future refactors switching to bytes.Buffer.
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

// graphql posts a GraphQL query and parses the standard data/errors
// envelope. Errors in the GraphQL "errors" array surface as a single
// error containing all messages.
func (c *Client) graphql(ctx context.Context, query string, variables map[string]any, out any) error {
	payload := map[string]any{"query": query, "variables": variables}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode graphql body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.graphqlURL, bytes.NewReader(bodyBytes))
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
	if resp.StatusCode != http.StatusOK {
		return c.errorFromResponse(resp, "POST", "/graphql")
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode graphql: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("github graphql: %s", strings.Join(msgs, ", "))
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode graphql data: %w", err)
		}
	}
	return nil
}

// errorFromResponse builds an HTTPError that wraps the appropriate
// sentinel and carries the truncated response body for debugging.
func (c *Client) errorFromResponse(resp *http.Response, method, path string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var inner error
	switch resp.StatusCode {
	case http.StatusNotFound:
		inner = bigorna.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		inner = bigorna.ErrUnauthorized
	default:
		inner = fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	return &bigorna.HTTPError{
		Method: method,
		Path:   path,
		Status: resp.StatusCode,
		Body:   truncate(string(body), 256),
		Inner:  inner,
	}
}

// errNotModified is the sentinel returned by getJSON on a 304.
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
