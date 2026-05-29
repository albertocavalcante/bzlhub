# bigorna

A small, opinionated Go library for driving pull-request workflows
across code-hosting platforms behind one interface.

## Status

**Pre-release.** No tagged versions. The interface is still settling
as more providers come online. Consumers pin via Go pseudo-versions
or local replace directives. Breaking changes are landed on `main`
without ceremony; consumers bump their pinned SHA when ready.

## Why this exists

Existing Go libraries for code-hosting platforms (`go-github`,
`go-gitlab`, drone's `go-scm`) are either platform-specific or
SDK-shaped with a wide surface (issues, deployments, CI status,
webhooks…). `bigorna` provides only the six-method slice needed for
**PR-style publish workflows**:

- Open a pull request, idempotently.
- Read its current state.
- List open PRs matching a marker (label, branch prefix, etc.).
- Comment on a PR.
- List new commits on a branch since a known SHA.
- Health-check the API and credentials.

Nothing more. The surface is intentionally narrow.

## Providers

| Forge | Status | Auth |
|---|---|---|
| GitHub.com | ✅ | PAT via `BearerAuth` (GitHub App planned) |
| GitHub Enterprise Server | ✅ | same; configurable `BaseURL` |
| Bitbucket Data Center | ✅ | HTTP PAT via `BearerAuth` |
| GitLab (saas + self-managed) | planned | PAT |
| Forgejo / Gitea | planned | PAT |
| Bitbucket Cloud | not planned | — |
| Azure DevOps / Gerrit / Sourcehut | not planned | — |

## Quick start

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/albertocavalcante/bigorna"
    "github.com/albertocavalcante/bigorna/github"
)

func main() {
    ctx := context.Background()

    client, err := github.New(github.Config{
        Auth: bigorna.BearerAuth(os.Getenv("GITHUB_TOKEN")),
        Repo: bigorna.Repo{Owner: "example", Name: "registry"},
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := client.Health(ctx); err != nil {
        log.Fatal(err)
    }

    pr, err := client.OpenPR(ctx, bigorna.OpenPROpts{
        Repo:       bigorna.Repo{Owner: "example", Name: "registry"},
        Title:      "Add foo@1.0.0",
        Body:       "Module published by bigorna.",
        HeadBranch: "release/add-foo-1.0.0",
        BaseBranch: "main",
        Labels:     []string{"automated"},
    })
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("opened PR #%d: %s", pr.Number, pr.URL)
}
```

For Bitbucket Data Center, swap `github` for `bitbucketdc` and add
`BaseURL`:

```go
import "github.com/albertocavalcante/bigorna/bitbucketdc"

client, err := bitbucketdc.New(bitbucketdc.Config{
    Auth:    bigorna.BearerAuth(os.Getenv("BITBUCKET_TOKEN")),
    Repo:    bigorna.Repo{Owner: "BAZ", Name: "registry"},
    BaseURL: "https://bitbucket.example.com",
})
```

For GitLab (gitlab.com or self-hosted via `BaseURL`):

```go
import "github.com/albertocavalcante/bigorna/gitlab"

client, err := gitlab.New(gitlab.Config{
    Auth: bigorna.BearerAuth(os.Getenv("GITLAB_TOKEN")),
    Repo: bigorna.Repo{Owner: "group/subgroup", Name: "registry"},
})
```

For Forgejo (codeberg.org or self-hosted, `BaseURL` required):

```go
import "github.com/albertocavalcante/bigorna/forgejo"

client, err := forgejo.New(forgejo.Config{
    Auth:    bigorna.BearerAuth(os.Getenv("FORGEJO_TOKEN")),
    Repo:    bigorna.Repo{Owner: "owner", Name: "registry"},
    BaseURL: "https://codeberg.org",
})
```

## Design highlights

- **Zero non-stdlib dependencies.** Hand-rolled HTTP, no `go-github`,
  no `go-git`.
- **Injectable retry policy + clock.** Tests run instantly via
  `bigornatest.ManualClock`; production uses `bigorna.RealClock{}`.
- **OpenPR is idempotent** by default. A second call with the same
  `HeadBranch` returns the existing PR instead of creating a
  duplicate. Disable per-config when the extra GET is unaffordable.
- **HTTPError wrapping.** Non-2xx responses surface as
  `*bigorna.HTTPError` carrying method, path, status, body. Both
  `errors.Is(err, bigorna.ErrNotFound)` and `errors.As(err,
  &httpErr)` work.
- **Rate-limit awareness** (GitHub). Below floor of remaining
  requests, the client preemptively sleeps until reset.
- **Pluggable Authorizer.** `BearerAuth(token)` for PATs;
  `BasicAuth(user, pass)` for legacy schemes; implement the
  interface for rotating credentials (Vault, KMS, GitHub App
  installation tokens).
- **Forge-agnostic marker convention.** `ListOpenPRs(repo, marker)`
  uses native PR labels on GitHub, branch-prefix matching on
  Bitbucket DC (which has no labels). Same interface for callers.

## License

MIT. See `LICENSE`.
