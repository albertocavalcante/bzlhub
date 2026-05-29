package bitbucketdc

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// ListNewCommits walks the commits endpoint for branch, using
// `since` / `until` query params instead of ETag.
//
// Semantics:
//   - sinceSHA non-empty → DC returns commits between sinceSHA
//     (exclusive) and branch tip (inclusive), newest first. An empty
//     response means nothing new — surfaced as notModified=true so
//     callers can treat it identically to a GitHub 304.
//   - sinceSHA empty → cold start. We take the first page (100
//     commits) and stop. Re-indexing from scratch when the remote
//     has more than 100 commits ahead is the caller's concern,
//     typically resolved by a `git pull` and full re-ingest.
//
// The etag parameter is opaque on this forge: passed through unchanged.
func (c *Client) ListNewCommits(
	ctx context.Context, repo bigorna.Repo, branch, sinceSHA, etag string,
) (commits []bigorna.Commit, newETag string, notModified bool, err error) {
	if repo != c.repo {
		return nil, "", false, fmt.Errorf(
			"bitbucketdc: ListNewCommits repo %s/%s does not match Client repo %s/%s",
			repo.Owner, repo.Name, c.repo.Owner, c.repo.Name)
	}
	q := url.Values{}
	q.Set("until", branch)
	q.Set("limit", "100")
	if sinceSHA != "" {
		q.Set("since", sinceSHA)
	}
	path := fmt.Sprintf("%s/commits?%s", c.repoPath(repo), q.Encode())

	var page struct {
		Values []struct {
			ID              string `json:"id"`
			Message         string `json:"message"`
			AuthorTimestamp int64  `json:"authorTimestamp"`
			Author          struct {
				Name         string `json:"name"`
				EmailAddress string `json:"emailAddress"`
			} `json:"author"`
		} `json:"values"`
	}
	if err := c.getJSON(ctx, path, &page); err != nil {
		return nil, "", false, err
	}
	if len(page.Values) == 0 {
		return nil, etag, true, nil
	}
	out := make([]bigorna.Commit, 0, len(page.Values))
	for _, v := range page.Values {
		out = append(out, bigorna.Commit{
			SHA:        v.ID,
			Message:    v.Message,
			Author:     v.Author.Name,
			AuthoredAt: time.UnixMilli(v.AuthorTimestamp).UTC(),
		})
	}
	return out, etag, false, nil
}

// Compile-time check that Client satisfies bigorna.Forge.
var _ bigorna.Forge = (*Client)(nil)
