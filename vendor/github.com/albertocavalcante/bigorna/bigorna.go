// Package bigorna defines a small, opinionated abstraction over
// code-hosting platforms for pull-request workflows. A Forge handles
// pull requests, comments, and commit listing — anything that
// requires the hosting platform's API rather than vanilla git.
//
// Concrete impls land in sibling packages:
//
//   - bigorna/github      — github.com (and GHES via BaseURL).
//   - bigorna/bitbucketdc — Bitbucket Data Center.
//
// The Forge interface is deliberately small (six methods, no event
// types, no webhook surface). Anything that requires the hosting
// platform's API and only the API lives here; everything else (file
// reads, branch creation, commits, pushes) is the caller's job via
// local git.
//
// Each impl has zero non-stdlib dependencies beyond this package.
package bigorna

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Forge is the abstraction over a code-hosting platform.
type Forge interface {
	// OpenPR creates a pull request. The HeadBranch must already exist
	// on the remote; this call only opens the PR.
	//
	// Idempotency: implementations may pre-check for an existing open
	// PR with the same HeadBranch and return that instead of creating
	// a duplicate. The contract is "best-effort idempotent" — the
	// network failure mode where a PR was created server-side but the
	// response was lost is recoverable on the next attempt.
	OpenPR(ctx context.Context, opts OpenPROpts) (PR, error)

	// GetPR fetches the current state of a pull request.
	GetPR(ctx context.Context, repo Repo, number int) (PR, error)

	// ListOpenPRs returns open PRs matching the given marker. The
	// marker is forge-specific:
	//   - GitHub: PR label name (e.g., "automation").
	//   - Bitbucket DC: branch-name prefix on fromRef (DC has no
	//     PR labels; impls translate to branch prefix).
	//
	// An empty marker returns all open PRs. Implementations paginate
	// to surface all open PRs, capped at a forge-specific maximum
	// (typically 1000) to bound memory and runtime.
	ListOpenPRs(ctx context.Context, repo Repo, marker string) ([]PR, error)

	// Comment posts a comment on a pull request.
	Comment(ctx context.Context, repo Repo, number int, body string) error

	// ListNewCommits returns commits on branch newer than sinceSHA.
	// etag is the value returned by a previous call; the forge may use
	// it for conditional GET (returning notModified=true with zero
	// commits). A first call passes "" for both sinceSHA and etag.
	//
	// On notModified=true, the returned etag is the one to pass to the
	// next call (forges without ETag support echo back the input).
	ListNewCommits(ctx context.Context, repo Repo, branch, sinceSHA, etag string) (
		commits []Commit, newETag string, notModified bool, err error,
	)

	// Health does a no-op API call to confirm credentials and base
	// URL. Typically called at host-process startup; failures should
	// fail the caller fast.
	Health(ctx context.Context) error
}

// Repo identifies a repository on a forge. The vocabulary is forge-
// specific (GitHub: owner+name; Bitbucket DC: projectKey+repoSlug) but
// the shape is the same.
type Repo struct {
	Owner string
	Name  string
}

// String renders the repo as "owner/name", useful for logs.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// OpenPROpts is the input to Forge.OpenPR.
type OpenPROpts struct {
	Repo       Repo
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
	Draft      bool
	// Labels are applied after open. Some forges (Bitbucket DC) ignore
	// this field — they have no PR-label concept and rely on the
	// branch-prefix marker convention instead.
	Labels []string
}

// PR is a forge pull request.
type PR struct {
	Number     int
	URL        string
	State      PRState
	HeadSHA    string
	HeadBranch string
	BaseBranch string
	Labels     []string
	OpenedAt   time.Time
	OpenedBy   string
}

// PRState is the high-level state of a pull request.
type PRState int

const (
	PRStateOpen PRState = iota
	PRStateClosed
	PRStateMerged
)

// String returns the lowercase canonical name (open / closed / merged).
func (s PRState) String() string {
	switch s {
	case PRStateOpen:
		return "open"
	case PRStateClosed:
		return "closed"
	case PRStateMerged:
		return "merged"
	default:
		return "unknown"
	}
}

// Commit is a forge commit summary as returned by ListNewCommits.
// Paths is populated when the forge cheaply provides changed files;
// nil otherwise. Callers that need full diffs should clone and use
// local git.
type Commit struct {
	SHA        string
	Message    string
	Author     string
	AuthoredAt time.Time
	Paths      []string
}

// Authorizer, BearerAuth, BasicAuth, AuthorizerFunc live in auth.go.

// RetryPolicy controls the HTTP retry loop. The zero value is invalid;
// use DefaultRetryPolicy() and override fields as needed.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	// MaxAttempts=1 disables retries entirely.
	MaxAttempts int

	// BaseBackoff is the duration of the first retry sleep.
	BaseBackoff time.Duration

	// MaxBackoff caps the exponential growth.
	MaxBackoff time.Duration

	// Jitter is the proportion (0..1) of random jitter applied to each
	// backoff. 0.0 = deterministic exponential; 0.2 = ±20%; 1.0 = full
	// jitter. The classic AWS "Equal Jitter" recommendation is 0.5;
	// this package defaults to 0.2 (mild, predictable, still spreads
	// herd) — see DefaultRetryPolicy.
	Jitter float64
}

// DefaultRetryPolicy returns sensible defaults: 3 attempts total,
// 500ms base, 5min cap, 20% jitter.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseBackoff: 500 * time.Millisecond,
		MaxBackoff:  5 * time.Minute,
		Jitter:      0.2,
	}
}

// BackoffFor returns the duration to sleep before retry attempt N
// (1-based). attempt=1 is the first retry (after the first failure).
// Capped by MaxBackoff. Jitter is applied via the provided rand source
// so tests can pin it deterministically.
func (p RetryPolicy) BackoffFor(attempt int, rng *rand.Rand) time.Duration {
	if attempt < 1 {
		return p.BaseBackoff
	}
	// Shifts past ~33 on a multi-bit BaseBackoff can truncate enough
	// significant bits that the result is zero or wraps positive
	// without ever being detected by the `d < 0` overflow guard
	// below. Any shift of 30+ on a 1-second base already exceeds any
	// reasonable MaxBackoff (~years), so capping here is harmless and
	// just future-proofs against absurd attempt values.
	shift := attempt - 1
	if shift > 30 {
		return p.MaxBackoff
	}
	d := p.BaseBackoff << shift
	if d > p.MaxBackoff || d < 0 { // overflow guard
		d = p.MaxBackoff
	}
	if p.Jitter > 0 {
		// Symmetric jitter: d * (1 + jitter*(2*rand-1)).
		j := p.Jitter
		if j > 1 {
			j = 1
		}
		// rand.Float64 in [0,1); shift to [-1,1).
		factor := 1 + j*(2*rng.Float64()-1)
		d = time.Duration(float64(d) * factor)
	}
	if d < 0 {
		d = p.BaseBackoff
	}
	return d
}

// Clock abstracts time for testability. Real production code uses
// RealClock; tests use a manual clock.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// Sleep waits for d or until ctx is canceled, whichever first.
	// Returns ctx.Err() on cancel.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock — wraps time.Now and time.NewTimer.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// HTTPError carries the HTTP-level details from a non-2xx forge
// response. Wraps a sentinel error (ErrNotFound / ErrUnauthorized) so
// callers can branch on `errors.Is(err, forge.ErrNotFound)` AND inspect
// via `errors.As(err, &forge.HTTPError{})` when they need the status,
// path, or response body.
type HTTPError struct {
	Method string
	Path   string
	Status int
	// Body is the raw response body, truncated. May be empty for
	// non-text responses or after upstream parsing.
	Body string
	// Inner is the underlying sentinel (ErrNotFound / ErrUnauthorized /
	// generic "forge: HTTP <status>"). errors.Is unwraps to find it.
	Inner error
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("forge: %s %s: status %d", e.Method, e.Path, e.Status)
	if e.Body != "" {
		msg += " (" + e.Body + ")"
	}
	return msg
}

func (e *HTTPError) Unwrap() error { return e.Inner }

// ErrNotFound is returned (or wrapped) when a forge resource doesn't
// exist. Distinguishes 404 from network/auth errors.
var ErrNotFound = errors.New("forge: not found")

// ErrUnauthorized is returned (or wrapped) when credentials are missing
// or invalid (HTTP 401), or when the token lacks permission (HTTP 403).
// Both surface as the same sentinel because the operator response is
// the same: check the token and its scopes.
var ErrUnauthorized = errors.New("forge: unauthorized")
