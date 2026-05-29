package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// repoNodeIDCache memoizes the GraphQL node ID for the configured
// repo. The ID is a stable opaque string; caching avoids a GraphQL
// round-trip on every OpenPR call.
type repoNodeIDCache struct {
	mu sync.Mutex
	id string
}

func (c *Client) repoNodeID(ctx context.Context) (string, error) {
	c.nodeID.mu.Lock()
	defer c.nodeID.mu.Unlock()
	if c.nodeID.id != "" {
		return c.nodeID.id, nil
	}
	var out struct {
		Repository struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	const q = `query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) { id }
	}`
	if err := c.graphql(ctx, q, map[string]any{
		"owner": c.repo.Owner,
		"name":  c.repo.Name,
	}, &out); err != nil {
		return "", fmt.Errorf("resolve repo node id: %w", err)
	}
	if out.Repository.ID == "" {
		return "", bigorna.ErrNotFound
	}
	c.nodeID.id = out.Repository.ID
	return c.nodeID.id, nil
}

const createPRMutation = `mutation OpenPR($input: CreatePullRequestInput!) {
	createPullRequest(input: $input) {
		pullRequest {
			number
			url
			state
			headRefName
			baseRefName
			headRefOid
			isDraft
			createdAt
			author { login }
		}
	}
}`

// OpenPR creates a pull request via GraphQL, then applies labels via
// REST. Label failures soft-fail (logged warning, PR still returned)
// because the PR exists and the caller's state has moved forward.
//
// Idempotency: unless DisableOpenPRIdempotency is set on Config, this
// first checks for an existing open PR whose head ref matches
// opts.HeadBranch. If one exists, the existing PR is returned without
// creating a duplicate. This handles the partial-failure case where
// a previous OpenPR succeeded server-side but the response was lost.
func (c *Client) OpenPR(ctx context.Context, opts bigorna.OpenPROpts) (bigorna.PR, error) {
	if opts.Repo != c.repo {
		return bigorna.PR{}, fmt.Errorf("github: OpenPR repo %s does not match Client repo %s",
			opts.Repo, c.repo)
	}
	if opts.HeadBranch == "" || opts.BaseBranch == "" {
		return bigorna.PR{}, fmt.Errorf("github: OpenPR requires HeadBranch and BaseBranch")
	}

	if !c.disableIdempotency {
		if existing, err := c.findOpenPRByHead(ctx, opts.HeadBranch); err != nil {
			return bigorna.PR{}, fmt.Errorf("idempotency check: %w", err)
		} else if existing != nil {
			c.logger.Info("github: OpenPR found existing open PR with same head; returning it",
				"number", existing.Number, "head", opts.HeadBranch)
			return *existing, nil
		}
	}

	repoID, err := c.repoNodeID(ctx)
	if err != nil {
		return bigorna.PR{}, err
	}

	var resp struct {
		CreatePullRequest struct {
			PullRequest struct {
				Number      int       `json:"number"`
				URL         string    `json:"url"`
				State       string    `json:"state"`
				HeadRefName string    `json:"headRefName"`
				BaseRefName string    `json:"baseRefName"`
				HeadRefOid  string    `json:"headRefOid"`
				IsDraft     bool      `json:"isDraft"`
				CreatedAt   time.Time `json:"createdAt"`
				Author      struct {
					Login string `json:"login"`
				} `json:"author"`
			} `json:"pullRequest"`
		} `json:"createPullRequest"`
	}
	if err := c.graphql(ctx, createPRMutation, map[string]any{
		"input": map[string]any{
			"repositoryId": repoID,
			"headRefName":  opts.HeadBranch,
			"baseRefName":  opts.BaseBranch,
			"title":        opts.Title,
			"body":         opts.Body,
			"draft":        opts.Draft,
		},
	}, &resp); err != nil {
		return bigorna.PR{}, fmt.Errorf("create pr: %w", err)
	}
	pr := bigorna.PR{
		Number:     resp.CreatePullRequest.PullRequest.Number,
		URL:        resp.CreatePullRequest.PullRequest.URL,
		State:      stateFromGraphQL(resp.CreatePullRequest.PullRequest.State),
		HeadSHA:    resp.CreatePullRequest.PullRequest.HeadRefOid,
		HeadBranch: resp.CreatePullRequest.PullRequest.HeadRefName,
		BaseBranch: resp.CreatePullRequest.PullRequest.BaseRefName,
		OpenedAt:   resp.CreatePullRequest.PullRequest.CreatedAt,
		OpenedBy:   resp.CreatePullRequest.PullRequest.Author.Login,
	}

	if len(opts.Labels) > 0 {
		if err := c.addLabels(ctx, pr.Number, opts.Labels); err != nil {
			// Soft-fail: PR exists; missing labels is a warning.
			c.logger.Warn("github: label-add failed",
				"pr", pr.Number, "labels", opts.Labels, "err", err)
		} else {
			pr.Labels = append([]string(nil), opts.Labels...)
		}
	}
	return pr, nil
}

// findOpenPRByHead uses GitHub's efficient head-ref filter on the
// pulls endpoint. Returns (pr, nil) if found, (nil, nil) if no open PR
// matches, (nil, err) on transport failure.
func (c *Client) findOpenPRByHead(ctx context.Context, headBranch string) (*bigorna.PR, error) {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", c.repo.Owner+":"+headBranch)
	q.Set("per_page", "1")
	path := c.repoBasePath() + "/pulls?" + q.Encode()

	// The "state=open" query above guarantees no merged PRs in the
	// response set, so we don't need to read the `merged` field. The
	// previous code declared a Merged bool tagged `json:"merged_at"`,
	// which would have triggered an UnmarshalTypeError on any non-
	// empty response had the filter ever been broadened.
	var raw []struct {
		Number    int       `json:"number"`
		HTMLURL   string    `json:"html_url"`
		State     string    `json:"state"`
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
	if _, err := c.getJSON(ctx, path, "", &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	r := raw[0]
	labels := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		labels[i] = l.Name
	}
	pr := bigorna.PR{
		Number:     r.Number,
		URL:        r.HTMLURL,
		State:      stateFromREST(r.State, false),
		HeadSHA:    r.Head.SHA,
		HeadBranch: r.Head.Ref,
		BaseBranch: r.Base.Ref,
		Labels:     labels,
		OpenedAt:   r.CreatedAt,
		OpenedBy:   r.User.Login,
	}
	return &pr, nil
}

// addLabels POSTs labels to the issue endpoint. PRs are issues in
// GitHub's model.
func (c *Client) addLabels(ctx context.Context, number int, labels []string) error {
	path := fmt.Sprintf("%s/issues/%d/labels", c.repoBasePath(), number)
	return c.postJSON(ctx, path, map[string][]string{"labels": labels}, nil)
}

// GetPR fetches the PR's current state via REST.
func (c *Client) GetPR(ctx context.Context, repo bigorna.Repo, number int) (bigorna.PR, error) {
	if repo != c.repo {
		return bigorna.PR{}, fmt.Errorf("github: GetPR repo %s does not match Client repo %s",
			repo, c.repo)
	}
	var raw struct {
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
	path := fmt.Sprintf("%s/pulls/%d", c.repoBasePath(), number)
	if _, err := c.getJSON(ctx, path, "", &raw); err != nil {
		return bigorna.PR{}, err
	}
	labels := make([]string, len(raw.Labels))
	for i, l := range raw.Labels {
		labels[i] = l.Name
	}
	return bigorna.PR{
		Number:     raw.Number,
		URL:        raw.HTMLURL,
		State:      stateFromREST(raw.State, raw.Merged),
		HeadSHA:    raw.Head.SHA,
		HeadBranch: raw.Head.Ref,
		BaseBranch: raw.Base.Ref,
		Labels:     labels,
		OpenedAt:   raw.CreatedAt,
		OpenedBy:   raw.User.Login,
	}, nil
}

// listOpenPRsScanMax bounds the pagination walk for ListOpenPRs so a
// catastrophic config never blocks the caller on tens of thousands
// of PRs.
const listOpenPRsScanMax = 1000

// ListOpenPRs returns open PRs carrying the given label. Implemented
// via the issues endpoint (PRs are issues in GitHub's model) so we can
// filter server-side by label. Paginates up to listOpenPRsScanMax PRs.
func (c *Client) ListOpenPRs(ctx context.Context, repo bigorna.Repo, marker string) ([]bigorna.PR, error) {
	if repo != c.repo {
		return nil, fmt.Errorf("github: ListOpenPRs repo %s does not match Client repo %s",
			repo, c.repo)
	}
	const perPage = 100
	out := make([]bigorna.PR, 0, perPage)
	for page := 1; ; page++ {
		if len(out) >= listOpenPRsScanMax {
			c.logger.Warn("github: ListOpenPRs hit scan cap; results truncated",
				"cap", listOpenPRsScanMax)
			break
		}
		q := url.Values{}
		q.Set("state", "open")
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		if marker != "" {
			q.Set("labels", marker)
		}
		path := c.repoBasePath() + "/issues?" + q.Encode()

		var raw []struct {
			Number      int    `json:"number"`
			HTMLURL     string `json:"html_url"`
			State       string `json:"state"`
			CreatedAt   time.Time `json:"created_at"`
			User        struct {
				Login string `json:"login"`
			} `json:"user"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
			PullRequest *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
		}
		if _, err := c.getJSON(ctx, path, "", &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		for _, r := range raw {
			if r.PullRequest == nil {
				continue // issue, not PR
			}
			labels := make([]string, len(r.Labels))
			for i, l := range r.Labels {
				labels[i] = l.Name
			}
			out = append(out, bigorna.PR{
				Number:   r.Number,
				URL:      r.HTMLURL,
				State:    stateFromREST(r.State, false),
				Labels:   labels,
				OpenedAt: r.CreatedAt,
				OpenedBy: r.User.Login,
			})
		}
		if len(raw) < perPage {
			break // last page
		}
	}
	return out, nil
}

// Comment posts a comment on a pull request via the issues endpoint.
func (c *Client) Comment(ctx context.Context, repo bigorna.Repo, number int, body string) error {
	if repo != c.repo {
		return fmt.Errorf("github: Comment repo %s does not match Client repo %s",
			repo, c.repo)
	}
	path := fmt.Sprintf("%s/issues/%d/comments", c.repoBasePath(), number)
	return c.postJSON(ctx, path, map[string]string{"body": body}, nil)
}

func stateFromGraphQL(s string) bigorna.PRState {
	switch s {
	case "OPEN":
		return bigorna.PRStateOpen
	case "MERGED":
		return bigorna.PRStateMerged
	case "CLOSED":
		return bigorna.PRStateClosed
	default:
		return bigorna.PRStateOpen
	}
}

func stateFromREST(state string, merged bool) bigorna.PRState {
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

