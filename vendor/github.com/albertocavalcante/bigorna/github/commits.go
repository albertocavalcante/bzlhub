package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// ListNewCommits walks the commits endpoint for branch, stopping at
// sinceSHA. ETag conditional GETs are honored — a stable branch returns
// 304 and zero new commits. New commits are returned newest-first.
//
// Designed for callers driving a polling loop to keep local state
// in sync with the remote branch without webhooks.
func (c *Client) ListNewCommits(
	ctx context.Context, repo bigorna.Repo, branch, sinceSHA, etag string,
) (commits []bigorna.Commit, newETag string, notModified bool, err error) {
	if repo != c.repo {
		return nil, "", false, fmt.Errorf(
			"github: ListNewCommits repo %s/%s does not match Client repo %s/%s",
			repo.Owner, repo.Name, c.repo.Owner, c.repo.Name)
	}
	q := url.Values{}
	q.Set("sha", branch)
	q.Set("per_page", "100")
	path := fmt.Sprintf("/repos/%s/%s/commits?%s", repo.Owner, repo.Name, q.Encode())

	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	gotETag, getErr := c.getJSON(ctx, path, etag, &raw)
	if errors.Is(getErr, errNotModified) {
		return nil, gotETag, true, nil
	}
	if getErr != nil {
		return nil, "", false, getErr
	}

	out := make([]bigorna.Commit, 0, len(raw))
	for _, r := range raw {
		if r.SHA == sinceSHA {
			break
		}
		out = append(out, bigorna.Commit{
			SHA:        r.SHA,
			Message:    r.Commit.Message,
			Author:     r.Commit.Author.Name,
			AuthoredAt: r.Commit.Author.Date,
		})
	}
	return out, gotETag, false, nil
}

// Compile-time check that Client satisfies bigorna.Forge.
var _ bigorna.Forge = (*Client)(nil)
