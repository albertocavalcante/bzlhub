package forgejo

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// prJSON is the wire shape of a Forgejo PR (verified against
// codeberg.org). Forgejo's list, get, and create endpoints all return
// this shape.
type prJSON struct {
	Number    int       `json:"number"`
	HTMLURL   string    `json:"html_url"`
	State     string    `json:"state"`
	Merged    bool      `json:"merged"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (p prJSON) toBigorna() bigorna.PR {
	labels := make([]string, len(p.Labels))
	for i, l := range p.Labels {
		labels[i] = l.Name
	}
	return bigorna.PR{
		Number:     p.Number,
		URL:        p.HTMLURL,
		State:      stateFromForgejo(p.State, p.Merged),
		HeadSHA:    p.Head.SHA,
		HeadBranch: p.Head.Ref,
		BaseBranch: p.Base.Ref,
		Labels:     labels,
		OpenedAt:   p.CreatedAt,
		OpenedBy:   p.User.Login,
	}
}

// OpenPR opens a pull request. With idempotency enabled (default),
// first scans open PRs and returns any existing one whose head branch
// matches opts.HeadBranch. Forgejo's pulls list endpoint silently
// ignores the documented `head` filter (verified against codeberg.org),
// so the scan is client-side.
func (c *Client) OpenPR(ctx context.Context, opts bigorna.OpenPROpts) (bigorna.PR, error) {
	if opts.Repo != c.repo {
		return bigorna.PR{}, fmt.Errorf("forgejo: OpenPR repo %s does not match Client repo %s",
			opts.Repo, c.repo)
	}
	if opts.HeadBranch == "" || opts.BaseBranch == "" {
		return bigorna.PR{}, fmt.Errorf("forgejo: OpenPR requires HeadBranch and BaseBranch")
	}

	if !c.disableIdempotency {
		if existing, err := c.findOpenPRByHead(ctx, opts.HeadBranch); err != nil {
			return bigorna.PR{}, fmt.Errorf("idempotency check: %w", err)
		} else if existing != nil {
			c.logger.Info("forgejo: OpenPR found existing open PR with same head; returning it",
				"number", existing.Number, "head", opts.HeadBranch)
			return *existing, nil
		}
	}

	body := map[string]any{
		"head":  opts.HeadBranch,
		"base":  opts.BaseBranch,
		"title": opts.Title,
		"body":  opts.Body,
	}
	if opts.Draft {
		// Forgejo recognizes a "WIP:" or "Draft:" title prefix as the
		// draft signal — the `draft` field on the create payload is
		// not honored by all versions, so use the title prefix path.
		body["title"] = "Draft: " + opts.Title
	}
	if len(opts.Labels) > 0 {
		// Forgejo's create payload accepts label IDs (integers), not
		// names. Mapping names → IDs adds a round trip on every call.
		// Skip with a warning, matching bitbucketdc's no-label policy.
		c.logger.Warn(
			"forgejo: OpenPR ignoring opts.Labels — Forgejo requires label IDs, not names",
			"labels", opts.Labels)
	}

	var raw prJSON
	if err := c.postJSON(ctx, c.repoBasePath()+"/pulls", body, &raw); err != nil {
		return bigorna.PR{}, fmt.Errorf("create pr: %w", err)
	}
	return raw.toBigorna(), nil
}

// idempotencyScanCap bounds the client-side scan in findOpenPRByHead.
// One page of 50 is enough for the canopy use case: a registry repo
// has at most a handful of open canopy/* PRs at any time. Operators
// with thousands of unrelated open PRs should set
// DisableOpenPRIdempotency and live with at-most-once semantics.
const idempotencyScanCap = 50

// findOpenPRByHead scans open PRs and returns the first whose head.ref
// matches headBranch. Forgejo doesn't honor a `head` query filter, so
// the filter is applied client-side.
func (c *Client) findOpenPRByHead(ctx context.Context, headBranch string) (*bigorna.PR, error) {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("limit", strconv.Itoa(idempotencyScanCap))
	path := c.repoBasePath() + "/pulls?" + q.Encode()

	var raw []prJSON
	if _, err := c.getJSON(ctx, path, &raw); err != nil {
		return nil, err
	}
	for _, p := range raw {
		if p.Head.Ref == headBranch {
			pr := p.toBigorna()
			return &pr, nil
		}
	}
	return nil, nil
}

// GetPR fetches a PR by number.
func (c *Client) GetPR(ctx context.Context, repo bigorna.Repo, number int) (bigorna.PR, error) {
	if repo != c.repo {
		return bigorna.PR{}, fmt.Errorf("forgejo: GetPR repo %s does not match Client repo %s",
			repo, c.repo)
	}
	path := fmt.Sprintf("%s/pulls/%d", c.repoBasePath(), number)
	var raw prJSON
	if _, err := c.getJSON(ctx, path, &raw); err != nil {
		return bigorna.PR{}, err
	}
	return raw.toBigorna(), nil
}

// listOpenPRsScanMax bounds the pagination walk.
const listOpenPRsScanMax = 1000

// ListOpenPRs returns open PRs, optionally filtered by label name.
// Since Forgejo's pulls endpoint takes label IDs (not names), the
// `marker` is applied client-side after fetching each page.
func (c *Client) ListOpenPRs(ctx context.Context, repo bigorna.Repo, marker string) ([]bigorna.PR, error) {
	if repo != c.repo {
		return nil, fmt.Errorf("forgejo: ListOpenPRs repo %s does not match Client repo %s",
			repo, c.repo)
	}
	const perPage = 50
	out := make([]bigorna.PR, 0, perPage)
	for page := 1; ; page++ {
		if len(out) >= listOpenPRsScanMax {
			c.logger.Warn("forgejo: ListOpenPRs hit scan cap; results truncated",
				"cap", listOpenPRsScanMax)
			break
		}
		q := url.Values{}
		q.Set("state", "open")
		q.Set("limit", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		path := c.repoBasePath() + "/pulls?" + q.Encode()

		var raw []prJSON
		if _, err := c.getJSON(ctx, path, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		for _, p := range raw {
			if marker != "" && !hasLabelName(p, marker) {
				continue
			}
			out = append(out, p.toBigorna())
		}
		if len(raw) < perPage {
			break
		}
	}
	return out, nil
}

func hasLabelName(p prJSON, name string) bool {
	for _, l := range p.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// Comment posts a comment on a PR. In Forgejo (as in Gitea and GitHub),
// PRs are issues for comment purposes — the endpoint is /issues/:index.
func (c *Client) Comment(ctx context.Context, repo bigorna.Repo, number int, body string) error {
	if repo != c.repo {
		return fmt.Errorf("forgejo: Comment repo %s does not match Client repo %s",
			repo, c.repo)
	}
	path := fmt.Sprintf("%s/issues/%d/comments", c.repoBasePath(), number)
	return c.postJSON(ctx, path, map[string]string{"body": body}, nil)
}

// stateFromForgejo maps Forgejo's PR state to the bigorna enum.
// Forgejo's PR state is just "open" | "closed"; the merged flag
// distinguishes merged-closed from declined-closed.
func stateFromForgejo(state string, merged bool) bigorna.PRState {
	if merged {
		return bigorna.PRStateMerged
	}
	switch state {
	case "open":
		return bigorna.PRStateOpen
	case "closed":
		return bigorna.PRStateClosed
	default:
		return bigorna.PRStateOpen
	}
}
