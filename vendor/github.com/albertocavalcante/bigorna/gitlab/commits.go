package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// ListNewCommits walks GET /api/v4/projects/:id/repository/commits.
//
// ETag handling: GitLab honors If-None-Match but on a match returns
// HTTP 200 with an empty body rather than 304. Both signals are mapped
// to notModified=true so callers see the bigorna contract regardless.
func (c *Client) ListNewCommits(
	ctx context.Context, repo bigorna.Repo, branch, sinceSHA, etag string,
) (commits []bigorna.Commit, newETag string, notModified bool, err error) {
	if repo != c.repo {
		return nil, "", false, fmt.Errorf(
			"gitlab: ListNewCommits repo %s/%s does not match Client repo %s/%s",
			repo.Owner, repo.Name, c.repo.Owner, c.repo.Name)
	}
	q := url.Values{}
	q.Set("ref_name", branch)
	q.Set("per_page", "100")
	path := c.projectPath() + "/repository/commits?" + q.Encode()

	// Suppress automatic decode — we need the raw body to detect
	// GitLab's "200 + empty array" not-modified shape.
	gotETag, body, getErr := c.getJSON(ctx, path, etag, nil)
	if errors.Is(getErr, errNotModified) {
		return nil, gotETag, true, nil
	}
	if getErr != nil {
		return nil, "", false, getErr
	}

	// "200 + empty body when If-None-Match was sent" → GitLab's
	// conditional-GET match. Treat it as not-modified and echo the
	// input etag so the next call still hits the conditional path.
	trimmed := trimBody(body)
	if etag != "" && (len(trimmed) == 0 || string(trimmed) == "[]") {
		return nil, etag, true, nil
	}

	var raw []struct {
		ID            string    `json:"id"`
		Message       string    `json:"message"`
		AuthorName    string    `json:"author_name"`
		AuthorEmail   string    `json:"author_email"`
		AuthoredDate  time.Time `json:"authored_date"`
	}
	if len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, "", false, fmt.Errorf("decode commits: %w", err)
		}
	}

	out := make([]bigorna.Commit, 0, len(raw))
	for _, r := range raw {
		if r.ID == sinceSHA {
			break
		}
		out = append(out, bigorna.Commit{
			SHA:        r.ID,
			Message:    r.Message,
			Author:     r.AuthorName,
			AuthoredAt: r.AuthoredDate,
		})
	}
	return out, gotETag, false, nil
}

// trimBody strips leading/trailing ASCII whitespace from the raw HTTP
// body so the "empty array" comparison is robust against pretty-printed
// servers and trailing newlines.
func trimBody(b []byte) []byte {
	start := 0
	for start < len(b) {
		switch b[start] {
		case ' ', '\t', '\n', '\r':
			start++
			continue
		}
		break
	}
	end := len(b)
	for end > start {
		switch b[end-1] {
		case ' ', '\t', '\n', '\r':
			end--
			continue
		}
		break
	}
	return b[start:end]
}

// Compile-time check that Client satisfies bigorna.Forge.
var _ bigorna.Forge = (*Client)(nil)
