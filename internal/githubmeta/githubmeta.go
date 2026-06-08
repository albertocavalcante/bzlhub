// Package githubmeta fetches the "social signals + languages" subset
// of repo information from the GitHub REST API:
//
//   - stars, forks, watchers, open issues
//   - default branch, description
//   - primary language + per-language byte counts (for the languages bar)
//
// Two endpoints, both unauthenticated-friendly:
//
//	GET /repos/{owner}/{repo}            → repo descriptor
//	GET /repos/{owner}/{repo}/languages  → map[language]bytes
//
// Anonymous access works at GitHub's default 60 req/h per IP, which
// is enough for canopy's small-corpus default but not for active
// refresh sweeps. Operators set GITHUB_TOKEN (file-mounted) for the
// 5000 req/h authenticated bucket; the TokenProvider abstraction lets
// the Sprint-4 GitHubApp slot in later without consumer changes.
//
// All requests honor fetch.Client's egress allowlist when configured,
// so corporate deployments that restrict outbound traffic to
// `api.github.com` (and the upstream BCR) keep that posture even
// while these calls run.
package githubmeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/egress"
	"github.com/albertocavalcante/bzlhub/internal/githubapi/token"
)

// Meta is the canopy-internal shape we persist + render. It's a
// projection of GitHub's repo + languages endpoints into the fields
// the UI consumes; new GitHub fields don't reach canopy unless
// surfaced here.
type Meta struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`

	Description     string `json:"description,omitempty"`
	DefaultBranch   string `json:"default_branch,omitempty"`
	PrimaryLanguage string `json:"primary_language,omitempty"`

	Stars      int `json:"stars"`
	Forks      int `json:"forks"`
	Watchers   int `json:"watchers"`
	OpenIssues int `json:"open_issues,omitempty"`

	// Languages is GitHub's per-language byte distribution.
	// Preserves the raw counts so the UI can render the bar at its
	// preferred granularity; the languages endpoint returns a
	// declining-bytes ordering already.
	Languages map[string]int `json:"languages,omitempty"`

	// ETag returned by GitHub on the /repos call. Used as the
	// If-None-Match header on the next refresh so a 304 keeps the
	// request from counting against the hourly bucket.
	ETag string `json:"etag,omitempty"`

	// FetchedAt is when canopy last received a 200 (fresh data) for
	// this repo. RFC3339Nano UTC.
	FetchedAt time.Time `json:"fetched_at"`
}

// ErrNotFound is returned when GitHub responds 404 to the repo
// lookup — the repository was renamed, deleted, or never existed.
// Callers persist this state so the UI can render "repo not on
// GitHub" rather than re-trying every refresh.
var ErrNotFound = errors.New("githubmeta: repo not found")

// ErrRateLimited is returned when GitHub responds 403/429 with a
// rate-limit signal. Callers serve stale and back off.
var ErrRateLimited = errors.New("githubmeta: rate limited")

// ErrNotModified is returned when the conditional request (using a
// previously-stored ETag) succeeds with 304. Caller refreshes the
// FetchedAt timestamp on the existing row and skips the languages
// call (which doesn't carry the same etag — see Fetch).
var ErrNotModified = errors.New("githubmeta: not modified")

// MaxJSONResponseBytes caps body reads against compromised /
// misbehaving GitHub proxies serving multi-GB JSON. Real repo
// responses are <2KB; languages responses <10KB. 16MB is orders
// of magnitude above legitimate use but well below the OOM
// threshold for canopy's typical deployment.
const MaxJSONResponseBytes = 16 * 1024 * 1024

// ErrResponseTooLarge is returned when an upstream response body
// exceeds MaxJSONResponseBytes. Sentinel for errors.Is.
var ErrResponseTooLarge = errors.New("githubmeta: response body exceeded cap")

// decodeCapped wraps res.Body in an io.LimitReader sized to detect
// overflow, then runs json.Decode. A field-value or array streaming
// past the cap surfaces ErrResponseTooLarge instead of OOMing the
// process.
func decodeCapped(r io.Reader, v any, maxBytes int64) error {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("%w: %d > %d", ErrResponseTooLarge, len(body), maxBytes)
	}
	return json.Unmarshal(body, v)
}

// ParseRepoLabel splits "owner/repo" into its parts. Returns false
// for anything that doesn't look like a github-style "owner/repo"
// (multiple slashes, empty halves, etc.) so callers don't issue an
// API call we know will 404. Whitespace is trimmed.
func ParseRepoLabel(label string) (owner, repo string, ok bool) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", "", false
	}
	i := strings.IndexByte(label, '/')
	if i <= 0 || i == len(label)-1 {
		return "", "", false
	}
	owner = label[:i]
	repo = label[i+1:]
	if strings.ContainsAny(owner, "/ \t") || strings.ContainsAny(repo, "/ \t") {
		return "", "", false
	}
	return owner, repo, true
}

// Client is the GitHub-meta HTTP client. Reuses one http.Client
// across calls so connection pooling kicks in across the refresh
// sweep.
type Client struct {
	HTTP     *http.Client
	Token    token.Provider
	BaseURL  string // override for tests; defaults to "https://api.github.com"
	UserAgent string // optional; defaults to "canopy"
}

// NewClient returns a Client wired with sensible defaults. The
// Token provider defaults to Anonymous when nil.
func NewClient(tp token.Provider) *Client {
	if tp == nil {
		tp = token.Anonymous{}
	}
	hc := egress.NewHTTPClient(egress.Policy{})
	hc.Timeout = 30 * time.Second
	return &Client{
		HTTP:      hc,
		Token:     tp,
		BaseURL:   "https://api.github.com",
		UserAgent: "canopy",
	}
}

// Fetch retrieves the repo descriptor + languages and projects them
// into a Meta. priorETag, when non-empty, is sent as If-None-Match
// on the /repos call; a 304 returns ErrNotModified so callers can
// keep their existing Meta and refresh only FetchedAt.
//
// On 404 returns ErrNotFound. On 403/429 with a rate-limit signal,
// returns ErrRateLimited.
func (c *Client) Fetch(ctx context.Context, owner, repo, priorETag string) (*Meta, error) {
	repoBody, etag, err := c.getRepo(ctx, owner, repo, priorETag)
	if err != nil {
		return nil, err
	}
	langs, err := c.getLanguages(ctx, owner, repo)
	if err != nil {
		// Languages-call failure is non-fatal: we still have the
		// /repos response. Surface as empty map; the UI will hide
		// the languages bar.
		langs = nil
	}
	m := &Meta{
		Owner:           owner,
		Repo:            repo,
		Description:     repoBody.Description,
		DefaultBranch:   repoBody.DefaultBranch,
		PrimaryLanguage: repoBody.Language,
		Stars:           repoBody.Stargazers,
		Forks:           repoBody.Forks,
		Watchers:        repoBody.Watchers,
		OpenIssues:      repoBody.OpenIssues,
		Languages:       langs,
		ETag:            etag,
		FetchedAt:       time.Now().UTC(),
	}
	return m, nil
}

// repoResponse is the subset of GitHub's /repos/{owner}/{repo}
// response we read. Field names match GitHub's JSON exactly.
type repoResponse struct {
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Language      string `json:"language"`
	Stargazers    int    `json:"stargazers_count"`
	Forks         int    `json:"forks_count"`
	Watchers      int    `json:"subscribers_count"`
	OpenIssues    int    `json:"open_issues_count"`
}

func (c *Client) getRepo(ctx context.Context, owner, repo, priorETag string) (*repoResponse, string, error) {
	u := c.BaseURL + "/repos/" + owner + "/" + repo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	c.setHeaders(ctx, req)
	if priorETag != "" {
		req.Header.Set("If-None-Match", priorETag)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		var body repoResponse
		if err := decodeCapped(res.Body, &body, MaxJSONResponseBytes); err != nil {
			return nil, "", fmt.Errorf("decode repo: %w", err)
		}
		return &body, res.Header.Get("ETag"), nil
	case http.StatusNotModified:
		return nil, "", ErrNotModified
	case http.StatusNotFound:
		return nil, "", ErrNotFound
	case http.StatusForbidden, http.StatusTooManyRequests:
		if isRateLimited(res) {
			return nil, "", ErrRateLimited
		}
		return nil, "", fmt.Errorf("repo: HTTP %d", res.StatusCode)
	default:
		return nil, "", fmt.Errorf("repo: HTTP %d", res.StatusCode)
	}
}

func (c *Client) getLanguages(ctx context.Context, owner, repo string) (map[string]int, error) {
	u := c.BaseURL + "/repos/" + owner + "/" + repo + "/languages"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(ctx, req)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		// GitHub returns a JSON object with language → bytes (int).
		var out map[string]int
		if err := decodeCapped(res.Body, &out, MaxJSONResponseBytes); err != nil {
			return nil, fmt.Errorf("decode languages: %w", err)
		}
		return out, nil
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusForbidden, http.StatusTooManyRequests:
		if isRateLimited(res) {
			return nil, ErrRateLimited
		}
		return nil, fmt.Errorf("languages: HTTP %d", res.StatusCode)
	default:
		// Best-effort: drain a bit so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<14))
		return nil, fmt.Errorf("languages: HTTP %d", res.StatusCode)
	}
}

func (c *Client) setHeaders(ctx context.Context, req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != nil {
		if tok, err := c.Token.Token(ctx); err == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
}

func isRateLimited(res *http.Response) bool {
	// Two signals: X-RateLimit-Remaining=0 (canonical) or 429 with
	// no body. Either way, the client should back off.
	if res.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return res.Header.Get("X-RateLimit-Remaining") == "0"
}
