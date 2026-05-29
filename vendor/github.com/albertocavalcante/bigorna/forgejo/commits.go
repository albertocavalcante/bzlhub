package forgejo

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// ListNewCommits walks GET /api/v1/repos/:owner/:repo/commits.
//
// Forgejo exposes no ETag header on this endpoint (verified against
// codeberg.org); the bigorna `etag` parameter is round-tripped on the
// notModified path but otherwise unused. The "unchanged" signal is
// derived from the response itself: if the newest commit returned by
// the server equals sinceSHA, there are no new commits.
//
// The implementation walks the page newest-first and stops at sinceSHA
// (exclusive). If sinceSHA was non-empty AND the filtered result is
// empty, the call returns notModified=true with the input etag echoed
// back so the bigorna contract holds.
func (c *Client) ListNewCommits(
	ctx context.Context, repo bigorna.Repo, branch, sinceSHA, etag string,
) (commits []bigorna.Commit, newETag string, notModified bool, err error) {
	if repo != c.repo {
		return nil, "", false, fmt.Errorf(
			"forgejo: ListNewCommits repo %s/%s does not match Client repo %s/%s",
			repo.Owner, repo.Name, c.repo.Owner, c.repo.Name)
	}
	q := url.Values{}
	q.Set("sha", branch)
	q.Set("limit", "100")
	path := c.repoBasePath() + "/commits?" + q.Encode()

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
	if _, err := c.getJSON(ctx, path, &raw); err != nil {
		return nil, "", false, err
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

	// Caller passed a sinceSHA and the walk produced nothing → either
	// the newest remote commit IS sinceSHA, or the branch went empty.
	// In both cases the right answer for a polling caller is "nothing
	// new" — echo back the input etag so the next call doesn't reset
	// to a cold-start cycle.
	if sinceSHA != "" && len(out) == 0 {
		return nil, etag, true, nil
	}

	// On a changed response Forgejo has no native ETag to round-trip,
	// so synthesize one from the newest SHA. Callers treat the etag
	// as an opaque token; using the latest SHA gives them a stable
	// value that changes iff the branch head moves.
	var synth string
	if len(out) > 0 {
		synth = out[0].SHA
	}
	return out, synth, false, nil
}

// Compile-time check that Client satisfies bigorna.Forge.
var _ bigorna.Forge = (*Client)(nil)
