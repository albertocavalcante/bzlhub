// Package auth carries the authenticated-identity types canopy
// uses across HTTP, MCP, and CLI surfaces.
//
// Pure types + context plumbing. The actual identity-extraction
// machinery (header parsing, bearer-token validation, OIDC) lives
// in transport-specific middleware. Service-layer code only sees
// the resolved Identity via FromContext().
//
// Identity is propagated through context.Context — never as an
// argument — so service methods don't need to thread an extra
// parameter through every call.
//
// Documented in docs/plans/08-corporate-security.md
// § "Authentication model" + § "Authorization model".
package auth

import "context"

// Source enumerates how canopy determined the request's identity.
// Distinct from the actual claims so downstream policy can vary
// per-source (e.g., bearer tokens may have a narrower role default
// than SSO-authenticated users).
type Source string

const (
	// SourceAnonymous indicates the request had no recognizable
	// identity. Most reads on personal-canopy installs operate
	// under this source.
	SourceAnonymous Source = "anonymous"
	// SourceHeader indicates identity came from X-Forwarded-*
	// headers injected by a trusted reverse proxy.
	SourceHeader Source = "header"
	// SourceBearer indicates identity came from a server-side
	// bearer token (CI runners, MCP clients with a configured
	// token). Shipped in Phase 2B Step 2 — Sprint 4.
	SourceBearer Source = "bearer"
	// SourceOIDC indicates canopy validated an OIDC token
	// directly (no reverse-proxy proxying). Shipped Sprint 5.
	SourceOIDC Source = "oidc"
)

// Identity is the resolved who-is-this for one request. All fields
// optional except Source — anonymous requests still carry an
// Identity with Source=SourceAnonymous and empty User/Email/Groups.
type Identity struct {
	// User is the username / login / sub claim. Convention: prefer
	// Email when both are available, but expose User for surfaces
	// that want a short handle (audit log, header chip).
	User string
	// Email is the request's email address. Often more stable
	// across SSO providers than User (GitHub OIDC reports
	// `login` as User; Google OIDC reports the email as `sub`).
	Email string
	// Groups is the optional list of group memberships, e.g.,
	// from X-Forwarded-Groups. Drives role lookups in the future
	// authorization layer; empty for sources that don't surface
	// groups.
	Groups []string
	// Source tells the policy layer how this identity was
	// established. See Source* constants.
	Source Source
}

// Anonymous returns the canonical "no identity" Identity value.
// Helper for tests + handlers that need to construct a baseline.
func Anonymous() Identity {
	return Identity{Source: SourceAnonymous}
}

// IsAuthenticated reports whether the identity carries any
// recognized authentication (anything other than SourceAnonymous).
// Helpers building authorization decisions should branch on this
// rather than .User != "".
func (i Identity) IsAuthenticated() bool {
	return i.Source != "" && i.Source != SourceAnonymous
}

// DisplayName returns the most useful single-string handle for UI
// + audit log: Email when set, else User, else "".
func (i Identity) DisplayName() string {
	if i.Email != "" {
		return i.Email
	}
	return i.User
}

// ctxKey is unexported so external packages can't accidentally
// overwrite the identity (context.WithValue's key-collision foot-gun).
type ctxKey struct{}

// WithContext returns a new ctx carrying id. Use in middleware after
// resolving the request's identity. Downstream handlers and the
// Service layer pull it back out via FromContext.
func WithContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the Identity attached to ctx (or
// Anonymous() + false when none is). Callers may ignore the bool
// for "give me whatever identity is here (anonymous if none)"
// patterns:
//
//	id, _ := auth.FromContext(ctx)
//	if id.IsAuthenticated() { ... }
func FromContext(ctx context.Context) (Identity, bool) {
	if v := ctx.Value(ctxKey{}); v != nil {
		if id, ok := v.(Identity); ok {
			return id, true
		}
	}
	return Anonymous(), false
}
