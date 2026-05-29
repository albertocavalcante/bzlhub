package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// mrJSON is the wire shape of a GitLab merge request as returned by the
// list, get, and create endpoints. GitLab is uniform across them.
type mrJSON struct {
	IID          int       `json:"iid"`
	WebURL       string    `json:"web_url"`
	State        string    `json:"state"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	SHA          string    `json:"sha"`
	Draft        bool      `json:"draft"`
	Labels       []string  `json:"labels"`
	CreatedAt    time.Time `json:"created_at"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (m mrJSON) toBigorna() bigorna.PR {
	return bigorna.PR{
		Number:     m.IID,
		URL:        m.WebURL,
		State:      stateFromGitLab(m.State),
		HeadSHA:    m.SHA,
		HeadBranch: m.SourceBranch,
		BaseBranch: m.TargetBranch,
		Labels:     append([]string(nil), m.Labels...),
		OpenedAt:   m.CreatedAt,
		OpenedBy:   m.Author.Username,
	}
}

// OpenPR opens a merge request. With idempotency enabled (default),
// first looks for an existing open MR with the same source branch and
// returns that instead of creating a duplicate.
func (c *Client) OpenPR(ctx context.Context, opts bigorna.OpenPROpts) (bigorna.PR, error) {
	if opts.Repo != c.repo {
		return bigorna.PR{}, fmt.Errorf("gitlab: OpenPR repo %s does not match Client repo %s",
			opts.Repo, c.repo)
	}
	if opts.HeadBranch == "" || opts.BaseBranch == "" {
		return bigorna.PR{}, fmt.Errorf("gitlab: OpenPR requires HeadBranch and BaseBranch")
	}

	if !c.disableIdempotency {
		if existing, err := c.findOpenMRBySource(ctx, opts.HeadBranch); err != nil {
			return bigorna.PR{}, fmt.Errorf("idempotency check: %w", err)
		} else if existing != nil {
			c.logger.Info("gitlab: OpenPR found existing open MR with same source; returning it",
				"iid", existing.Number, "source", opts.HeadBranch)
			return *existing, nil
		}
	}

	body := map[string]any{
		"source_branch": opts.HeadBranch,
		"target_branch": opts.BaseBranch,
		"title":         opts.Title,
		"description":   opts.Body,
	}
	if len(opts.Labels) > 0 {
		body["labels"] = opts.Labels
	}
	if opts.Draft {
		// GitLab convention: a draft MR has "Draft: " prefix in title.
		// The dedicated boolean was deprecated in v15. Setting title
		// here is the durable approach.
		body["title"] = "Draft: " + opts.Title
	}

	var raw mrJSON
	if err := c.postJSON(ctx, c.projectPath()+"/merge_requests", body, &raw); err != nil {
		return bigorna.PR{}, fmt.Errorf("create mr: %w", err)
	}
	return raw.toBigorna(), nil
}

// findOpenMRBySource returns the open MR matching source_branch, or nil
// if none exists.
func (c *Client) findOpenMRBySource(ctx context.Context, source string) (*bigorna.PR, error) {
	q := url.Values{}
	q.Set("state", "opened")
	q.Set("source_branch", source)
	q.Set("per_page", "1")
	path := c.projectPath() + "/merge_requests?" + q.Encode()

	var raw []mrJSON
	if _, _, err := c.getJSON(ctx, path, "", &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	pr := raw[0].toBigorna()
	return &pr, nil
}

// GetPR fetches a merge request by IID.
func (c *Client) GetPR(ctx context.Context, repo bigorna.Repo, number int) (bigorna.PR, error) {
	if repo != c.repo {
		return bigorna.PR{}, fmt.Errorf("gitlab: GetPR repo %s does not match Client repo %s",
			repo, c.repo)
	}
	path := fmt.Sprintf("%s/merge_requests/%d", c.projectPath(), number)
	var raw mrJSON
	if _, _, err := c.getJSON(ctx, path, "", &raw); err != nil {
		return bigorna.PR{}, err
	}
	return raw.toBigorna(), nil
}

const listOpenPRsScanMax = 1000

// ListOpenPRs returns open MRs filtered by the given label marker.
// Empty marker returns all open MRs. Paginates up to listOpenPRsScanMax.
func (c *Client) ListOpenPRs(ctx context.Context, repo bigorna.Repo, marker string) ([]bigorna.PR, error) {
	if repo != c.repo {
		return nil, fmt.Errorf("gitlab: ListOpenPRs repo %s does not match Client repo %s",
			repo, c.repo)
	}
	const perPage = 100
	out := make([]bigorna.PR, 0, perPage)
	for page := 1; ; page++ {
		if len(out) >= listOpenPRsScanMax {
			c.logger.Warn("gitlab: ListOpenPRs hit scan cap; results truncated",
				"cap", listOpenPRsScanMax)
			break
		}
		q := url.Values{}
		q.Set("state", "opened")
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		if marker != "" {
			q.Set("labels", marker)
		}
		path := c.projectPath() + "/merge_requests?" + q.Encode()

		var raw []mrJSON
		if _, _, err := c.getJSON(ctx, path, "", &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}
		for _, m := range raw {
			out = append(out, m.toBigorna())
		}
		if len(raw) < perPage {
			break
		}
	}
	return out, nil
}

// Comment posts a note (GitLab's term for a comment) on an MR.
func (c *Client) Comment(ctx context.Context, repo bigorna.Repo, number int, body string) error {
	if repo != c.repo {
		return fmt.Errorf("gitlab: Comment repo %s does not match Client repo %s",
			repo, c.repo)
	}
	path := fmt.Sprintf("%s/merge_requests/%d/notes", c.projectPath(), number)
	return c.postJSON(ctx, path, map[string]string{"body": body}, nil)
}

func stateFromGitLab(s string) bigorna.PRState {
	switch s {
	case "opened":
		return bigorna.PRStateOpen
	case "merged":
		return bigorna.PRStateMerged
	case "closed":
		return bigorna.PRStateClosed
	default:
		return bigorna.PRStateOpen
	}
}
