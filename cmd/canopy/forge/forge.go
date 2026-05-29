// Package forge wraps bigorna's forge constructors so the cmd/canopy
// publish and watch subcommands share a single switch for "kind" and
// a single display-name table. Keeps publish/ and watch.go from
// drifting on which forges are supported.
package forge

import (
	"fmt"
	"time"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bigorna/bitbucketdc"
	"github.com/albertocavalcante/bigorna/forgejo"
	"github.com/albertocavalcante/bigorna/github"
	"github.com/albertocavalcante/bigorna/gitlab"

	"github.com/albertocavalcante/canopy/internal/egress"
)

// New constructs a bigorna.Forge from a forge kind + repo + baseURL +
// token + userAgent. Callers pre-validate `kind`; the default-branch
// error is only reached if validation drifts out of sync.
func New(kind string, repo bigorna.Repo, baseURL, token, userAgent string) (bigorna.Forge, error) {
	httpClient := egress.NewHTTPClient(egress.Policy{})
	httpClient.Timeout = 30 * time.Second
	switch kind {
	case "github":
		return github.New(github.Config{
			Auth:       bigorna.BearerAuth(token),
			Repo:       repo,
			BaseURL:    baseURL, // empty → github.com default
			UserAgent:  userAgent,
			HTTPClient: httpClient,
		})
	case "bitbucketdc":
		return bitbucketdc.New(bitbucketdc.Config{
			Auth:       bigorna.BearerAuth(token),
			Repo:       repo,
			BaseURL:    baseURL, // required (validated upstream)
			UserAgent:  userAgent,
			HTTPClient: httpClient,
		})
	case "gitlab":
		return gitlab.New(gitlab.Config{
			Auth:       bigorna.BearerAuth(token),
			Repo:       repo,
			BaseURL:    baseURL, // empty → gitlab.com default
			UserAgent:  userAgent,
			HTTPClient: httpClient,
		})
	case "forgejo":
		return forgejo.New(forgejo.Config{
			Auth:       bigorna.BearerAuth(token),
			Repo:       repo,
			BaseURL:    baseURL, // required (validated upstream)
			UserAgent:  userAgent,
			HTTPClient: httpClient,
		})
	}
	return nil, fmt.Errorf("forge.New: unhandled forge %q", kind)
}

// DisplayName returns a human-readable name for use in error
// messages (e.g., "GitHub PAT" reads better than "github PAT").
func DisplayName(kind string) string {
	switch kind {
	case "github":
		return "GitHub"
	case "gitlab":
		return "GitLab"
	case "bitbucketdc":
		return "Bitbucket DC"
	case "forgejo":
		return "Forgejo"
	}
	return kind
}
