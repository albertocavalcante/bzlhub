package bitbucketdc

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// createPRRequest mirrors Bitbucket DC's PR-create body shape. The
// repository must be repeated under both fromRef.repository and
// toRef.repository — DC requires it even for same-repo PRs.
//
// Open/Closed/Locked are server-managed; we omit them. Sending them
// is at best redundant and at worst rejected by hardened DC
// validators.
type createPRRequest struct {
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"`
	FromRef     refDescriptor `json:"fromRef"`
	ToRef       refDescriptor `json:"toRef"`
	Draft       bool          `json:"draft,omitempty"`
}

type refDescriptor struct {
	ID         string            `json:"id"`
	Repository refDescriptorRepo `json:"repository"`
}

type refDescriptorRepo struct {
	Slug    string               `json:"slug"`
	Project refDescriptorProject `json:"project"`
}

type refDescriptorProject struct {
	Key string `json:"key"`
}

// prResponse covers the fields this package reads from a PR document.
type prResponse struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	State       string `json:"state"` // OPEN | MERGED | DECLINED | SUPERSEDED
	Open        bool   `json:"open"`
	Closed      bool   `json:"closed"`
	CreatedDate int64  `json:"createdDate"` // Unix ms
	UpdatedDate int64  `json:"updatedDate"`
	FromRef     struct {
		ID           string `json:"id"`
		DisplayID    string `json:"displayId"`
		LatestCommit string `json:"latestCommit"`
	} `json:"fromRef"`
	ToRef struct {
		ID        string `json:"id"`
		DisplayID string `json:"displayId"`
	} `json:"toRef"`
	Links struct {
		Self []struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
	Author struct {
		User struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
	} `json:"author"`
}

// OpenPR creates a pull request via the Bitbucket DC REST API.
//
// opts.Labels is ignored — DC has no PR-label concept. A warning is
// logged so an operator surprised to find labels missing knows where
// to look.
//
// Idempotency: unless DisableOpenPRIdempotency is set, this scans
// open PRs paginated and returns any existing PR whose fromRef matches
// opts.HeadBranch. Cost: up to listOpenPRsScanMax (1000) PRs scanned.
// Disable on repos where this is too expensive AND duplicates are
// tolerable.
func (c *Client) OpenPR(ctx context.Context, opts bigorna.OpenPROpts) (bigorna.PR, error) {
	if opts.Repo != c.repo {
		return bigorna.PR{}, fmt.Errorf(
			"bitbucketdc: OpenPR repo %s does not match Client repo %s",
			opts.Repo, c.repo)
	}
	if opts.HeadBranch == "" || opts.BaseBranch == "" {
		return bigorna.PR{}, fmt.Errorf("bitbucketdc: OpenPR requires HeadBranch and BaseBranch")
	}
	if len(opts.Labels) > 0 {
		c.logger.Warn("bitbucketdc: PR labels are not supported by Bitbucket DC; ignoring",
			"labels", opts.Labels,
			"hint", "use a branch-prefix marker convention instead (e.g. release/* or automation/*)")
	}

	if !c.disableIdempotency {
		if existing, err := c.findOpenPRByHead(ctx, opts.HeadBranch); err != nil {
			return bigorna.PR{}, fmt.Errorf("idempotency check: %w", err)
		} else if existing != nil {
			c.logger.Info("bitbucketdc: OpenPR found existing open PR with same head; returning it",
				"id", existing.Number, "head", opts.HeadBranch)
			return *existing, nil
		}
	}

	body := createPRRequest{
		Title:       opts.Title,
		Description: opts.Body,
		FromRef:     c.refOf(opts.HeadBranch),
		ToRef:       c.refOf(opts.BaseBranch),
		Draft:       opts.Draft,
	}
	var resp prResponse
	path := fmt.Sprintf("%s/pull-requests", c.repoPath(c.repo))
	if err := c.postJSON(ctx, path, body, &resp); err != nil {
		return bigorna.PR{}, fmt.Errorf("create pr: %w", err)
	}
	return prFromResponse(resp), nil
}

// findOpenPRByHead walks open PRs (paginated, capped) and returns one
// whose fromRef.displayId equals headBranch. DC has no head-ref query
// param, so client-side scanning is the only option.
func (c *Client) findOpenPRByHead(ctx context.Context, headBranch string) (*bigorna.PR, error) {
	hits, err := c.listOpenPRsRaw(ctx, func(r prResponse) bool {
		return r.FromRef.DisplayID == headBranch
	}, 1)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	pr := prFromResponse(hits[0])
	return &pr, nil
}

func (c *Client) refOf(branch string) refDescriptor {
	return refDescriptor{
		ID: "refs/heads/" + branch,
		Repository: refDescriptorRepo{
			Slug:    c.repo.Name,
			Project: refDescriptorProject{Key: c.repo.Owner},
		},
	}
}

// GetPR fetches a PR by ID.
func (c *Client) GetPR(ctx context.Context, repo bigorna.Repo, number int) (bigorna.PR, error) {
	if repo != c.repo {
		return bigorna.PR{}, fmt.Errorf(
			"bitbucketdc: GetPR repo %s does not match Client repo %s",
			repo, c.repo)
	}
	var resp prResponse
	path := fmt.Sprintf("%s/pull-requests/%d", c.repoPath(repo), number)
	if err := c.getJSON(ctx, path, &resp); err != nil {
		return bigorna.PR{}, err
	}
	return prFromResponse(resp), nil
}

// listOpenPRsScanMax bounds the pagination walk for any client-side
// scan so a catastrophic config never blocks the caller on tens of
// thousands of PRs.
const listOpenPRsScanMax = 1000

// ListOpenPRs returns open PRs whose source branch starts with the
// given marker. Marker semantics:
//
//   - "" → all open PRs (no filter), up to listOpenPRsScanMax.
//   - "automation" → matches branches like automation/bump-foo-1.0.0.
//
// Paginates via DC's start/nextPageStart/isLastPage protocol.
func (c *Client) ListOpenPRs(ctx context.Context, repo bigorna.Repo, marker string) ([]bigorna.PR, error) {
	if repo != c.repo {
		return nil, fmt.Errorf(
			"bitbucketdc: ListOpenPRs repo %s does not match Client repo %s",
			repo, c.repo)
	}
	pred := func(r prResponse) bool {
		if marker == "" {
			return true
		}
		return strings.HasPrefix(r.FromRef.DisplayID, marker)
	}
	raws, err := c.listOpenPRsRaw(ctx, pred, listOpenPRsScanMax)
	if err != nil {
		return nil, err
	}
	out := make([]bigorna.PR, 0, len(raws))
	for _, r := range raws {
		out = append(out, prFromResponse(r))
	}
	return out, nil
}

// listOpenPRsRaw is the shared paginated walker used by ListOpenPRs
// and the idempotency check. Stops after `pred` returns true `want`
// times, OR after listOpenPRsScanMax PRs are scanned (whichever first).
func (c *Client) listOpenPRsRaw(
	ctx context.Context, pred func(prResponse) bool, want int,
) ([]prResponse, error) {
	const pageLimit = 100
	hits := make([]prResponse, 0, pageLimit)
	scanned := 0
	start := 0
	for {
		if scanned >= listOpenPRsScanMax {
			c.logger.Warn("bitbucketdc: ListOpenPRs hit scan cap; results may be incomplete",
				"cap", listOpenPRsScanMax, "marker_hits", len(hits))
			break
		}
		q := url.Values{}
		q.Set("state", "OPEN")
		q.Set("limit", strconv.Itoa(pageLimit))
		q.Set("start", strconv.Itoa(start))
		path := fmt.Sprintf("%s/pull-requests?%s", c.repoPath(c.repo), q.Encode())

		var page struct {
			Values        []prResponse `json:"values"`
			IsLastPage    bool         `json:"isLastPage"`
			NextPageStart int          `json:"nextPageStart"`
		}
		if err := c.getJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Values {
			scanned++
			if pred(r) {
				hits = append(hits, r)
				if want > 0 && len(hits) >= want {
					return hits, nil
				}
			}
		}
		if page.IsLastPage || len(page.Values) == 0 {
			break
		}
		// Defensive: if the server didn't advance, bail to avoid an
		// infinite loop on a buggy response.
		if page.NextPageStart <= start {
			break
		}
		start = page.NextPageStart
	}
	return hits, nil
}

// Comment posts a comment on a pull request.
func (c *Client) Comment(ctx context.Context, repo bigorna.Repo, number int, body string) error {
	if repo != c.repo {
		return fmt.Errorf(
			"bitbucketdc: Comment repo %s does not match Client repo %s",
			repo, c.repo)
	}
	path := fmt.Sprintf("%s/pull-requests/%d/comments", c.repoPath(repo), number)
	return c.postJSON(ctx, path, map[string]string{"text": body}, nil)
}

func prFromResponse(r prResponse) bigorna.PR {
	pr := bigorna.PR{
		Number:     r.ID,
		State:      stateFromDC(r.State),
		HeadSHA:    r.FromRef.LatestCommit,
		HeadBranch: r.FromRef.DisplayID,
		BaseBranch: r.ToRef.DisplayID,
		OpenedAt:   time.UnixMilli(r.CreatedDate).UTC(),
		OpenedBy:   r.Author.User.Name,
	}
	if len(r.Links.Self) > 0 {
		pr.URL = r.Links.Self[0].Href
	}
	return pr
}

func stateFromDC(s string) bigorna.PRState {
	switch s {
	case "OPEN":
		return bigorna.PRStateOpen
	case "MERGED":
		return bigorna.PRStateMerged
	case "DECLINED", "SUPERSEDED":
		return bigorna.PRStateClosed
	default:
		return bigorna.PRStateOpen
	}
}
