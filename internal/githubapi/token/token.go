// Package token provides pluggable credential sources for canopy's
// optional GitHub API access.
//
// canopy NEVER requires GitHub access. Features that benefit from
// it (I5 stars/forks/watchers, I9 languages, I4 PR provenance) MUST
// degrade gracefully when no token is available. Anonymous (60/h
// rate limit) is the default; everything else is an operator opt-in
// at startup.
//
// Provider implementations land in order of operator-friendliness:
//
//   - Anonymous: zero setup, ships today, 60/h.
//   - PAT:       file-pointed env var, ships today, personal use.
//   - GitHubApp: short-lived installation tokens, Sprint 4 — the
//                recommended corporate path.
//   - OIDC:      federation à la OctoSTS, Sprint 5 — zero stored
//                secret, ideal for k8s / cloud workload identity.
//
// Documented in docs/plans/08-corporate-security.md §
// "TokenProvider abstraction".
package token

import (
	"context"

	"github.com/albertocavalcante/canopy/internal/secrets"
)

// Provider returns a bearer token for GitHub API requests, or ""
// to use anonymous access. May return an error when the provider
// is misconfigured (e.g., a future GitHubApp can't mint an
// installation token).
//
// Consumers call Token() per-request (or per-batch when caching
// is appropriate). Implementations cache internally where useful.
type Provider interface {
	Token(ctx context.Context) (string, error)
}

// Anonymous is the default provider. Returns the empty token,
// which makes GitHub API calls anonymous (60 requests/hour). Works
// without configuration; suitable for personal-canopy installs at
// small corpus scale.
type Anonymous struct{}

// Token returns "", nil.
func (Anonymous) Token(_ context.Context) (string, error) { return "", nil }

// PAT (personal access token) reads a long-lived GitHub token from
// the file pointed at by $<Env>_FILE, with literal $<Env> as a
// fallback for quick-start.
//
// PAT is documented as the "escape hatch" path for personal
// canopy installs that don't have a GitHub App configured. Not
// recommended for corporate deployments — its blast radius on
// leak equals the issuing user's account permissions.
type PAT struct {
	// Env is the env var name to read. Convention is the GitHub-
	// standard `GITHUB_TOKEN`, but operators can choose another
	// (e.g., `CANOPY_GITHUB_TOKEN`) when sharing a host with
	// other tools.
	Env string
}

// Token returns the trimmed contents of the file at $<p.Env>_FILE,
// or the literal $<p.Env>, or "" if neither is set. Always nil-err
// at this layer; an unreadable file just yields "" so callers
// fall back to anonymous mode.
func (p PAT) Token(_ context.Context) (string, error) {
	if p.Env == "" {
		return "", nil
	}
	return secrets.Read(p.Env), nil
}
