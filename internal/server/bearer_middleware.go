// Bearer-token authentication middleware.
//
// Reads `Authorization: Bearer <token>` from incoming requests,
// resolves the token against an in-memory IdentityRegistry loaded
// from a JSON file at boot, and attaches the resolved auth.Identity
// to the request context.
//
// Bearer auth runs BEFORE the header-based auth middleware in the
// chi chain. When BOTH a valid bearer token AND X-Forwarded-* headers
// are present on the same request, the bearer identity wins and the
// middleware emits a WARN log — the operator should investigate
// (likely a misconfigured reverse proxy that's both terminating OIDC
// AND passing through Authorization).
//
// Per Plan 72 §C3 + §CC3 — bearer is the stronger signal
// (server-side validated against canopy's local registry) versus
// X-Forwarded-* (only proven by network-CIDR trust).
//
// Authorization headers from outside the reverse-proxy CIDR are
// validated UNCHANGED — bearer auth doesn't depend on the
// trusted-proxy gate (the token's hash in identity.json IS the
// proof). This is intentional: bearer is canopy's "agent/CI/MCP
// client" path, not a "reverse proxy did the work" path.

package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/auth"
)

// bearerAuth returns a chi-compatible middleware that resolves
// identity from `Authorization: Bearer <token>` against reg.
//
// reg may be nil — middleware then passes through unconditionally
// (the operator hasn't wired bearer auth). Identical behavior to
// reg.Size() == 0 with one fewer indirection.
//
// The returned middleware writes the resolved identity onto the
// request context via auth.WithContext; downstream code reads via
// auth.FromContext. No HTTP response side-effects on success or
// miss — the request continues to the next handler regardless.
// Authorization failures (a bearer token that doesn't match
// anything) are anonymous from canopy's perspective; policy gates
// downstream decide whether anonymous is allowed for the requested
// action.
func bearerAuth(reg *auth.IdentityRegistry, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if reg == nil || reg.Size() == 0 {
				next.ServeHTTP(w, r)
				return
			}
			token := extractBearerToken(r.Header.Get("Authorization"))
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			ident, ok := reg.Lookup(token)
			if !ok {
				// Token presented but didn't match anything in the
				// registry. Could be: typo, revoked token, attacker
				// guessing. Log at INFO (not WARN — false positives
				// from clients with stale tokens are common); never
				// log the token itself.
				if log != nil {
					log.Info("bearer auth: token did not match registry",
						"remote_addr", r.RemoteAddr,
						"path", r.URL.Path)
				}
				next.ServeHTTP(w, r)
				return
			}

			// Bearer wins over any X-Forwarded-* headers per
			// Plan 72 §CC3 — warn the operator when this collides.
			if log != nil && (r.Header.Get(HeaderForwardedUser) != "" ||
				r.Header.Get(HeaderForwardedEmail) != "") {
				log.Warn("bearer auth and X-Forwarded-* both present; bearer wins (operator: check reverse proxy isn't double-authing)",
					"bearer_identity", ident.DisplayName(),
					"forwarded_user", r.Header.Get(HeaderForwardedUser),
					"forwarded_email", r.Header.Get(HeaderForwardedEmail),
					"path", r.URL.Path)
			}

			ctx := auth.WithContext(r.Context(), ident)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearerToken parses the value of an Authorization header
// and returns the bearer token, or "" if the header doesn't carry
// a bearer scheme.
//
// Tolerates extra whitespace ("Bearer  abc"), mixed case
// ("bearer abc"), and the spec-strict "Bearer abc" form. Empty
// input or any non-bearer scheme returns "".
//
// Does NOT trim the token itself — bearer tokens may legitimately
// contain whitespace-significant base64 padding in some schemes.
// canopy's tokens are operator-generated hex, so this doesn't
// matter in practice, but the contract is "give me whatever
// follows the scheme byte-for-byte."
func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	// Common shape: "Bearer <token>". Skip the scheme word + any
	// run of whitespace.
	const scheme = "bearer"
	if len(header) <= len(scheme) {
		return ""
	}
	if !strings.EqualFold(header[:len(scheme)], scheme) {
		return ""
	}
	rest := header[len(scheme):]
	// Require at least one whitespace separator after the scheme.
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return ""
	}
	return strings.TrimLeft(rest, " \t")
}
