// Header-based authentication middleware.
//
// Reads the X-Forwarded-User / X-Forwarded-Email / X-Forwarded-Groups
// triple injected by a trusted reverse proxy (nginx + oauth2-proxy,
// Caddy + oauth2-proxy, Pomerium, Pinniped, k8s ingress with an
// ext-auth annotation, etc.) and resolves them into an auth.Identity
// attached to the request context.
//
// The headers are trusted ONLY when the request's source IP is in
// the configured trusted-proxy CIDR. Outside the CIDR, any client-
// sent X-Forwarded-* headers are ignored — defangs the trivial
// "anyone can spoof headers" attack against an exposed canopy.
//
// Documented in docs/plans/08-corporate-security.md
// § "Authentication model / v0.3 — header-based auth scaffold".

package server

import (
	"net"
	"net/http"
	"strings"

	"github.com/albertocavalcante/canopy/internal/auth"
)

const (
	// HeaderForwardedUser is the canonical X-Forwarded-User header.
	// oauth2-proxy emits this with `--set-xauthrequest`.
	HeaderForwardedUser = "X-Forwarded-User"
	// HeaderForwardedEmail is the canonical X-Forwarded-Email header.
	HeaderForwardedEmail = "X-Forwarded-Email"
	// HeaderForwardedGroups is the canonical X-Forwarded-Groups
	// header (comma-separated list of group names).
	HeaderForwardedGroups = "X-Forwarded-Groups"
)

// headerAuth returns a chi-compatible middleware that resolves the
// request's identity from trusted-proxy headers.
//
// trustedCIDRs is the operator-provided list of source-IP CIDRs
// from which X-Forwarded-* headers will be honored. An empty list
// disables header trust entirely (requests stay anonymous), which
// is the safe default for personal-canopy installs not running
// behind a reverse proxy.
//
// Anonymous requests pass through with no identity attached;
// downstream handlers see a bare ctx and auth.FromContext returns
// (Anonymous(), false).
func headerAuth(trustedCIDRs []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(trustedCIDRs) > 0 && sourceIPIsTrusted(r, trustedCIDRs) {
				if id, ok := identityFromHeaders(r); ok {
					ctx := auth.WithContext(r.Context(), id)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// sourceIPIsTrusted reports whether the request's remote address
// falls within any of the operator-trusted CIDR blocks. Uses
// RemoteAddr verbatim — does NOT honor X-Forwarded-For (that would
// re-introduce the spoofing attack we're guarding against). The
// trusted proxy must connect directly to canopy.
func sourceIPIsTrusted(r *http.Request, cidrs []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}

// identityFromHeaders builds an auth.Identity from the request's
// X-Forwarded-* triple. Returns (Identity{}, false) when neither
// user nor email is present — the headers must carry something
// recognizable for the identity to be considered authenticated.
func identityFromHeaders(r *http.Request) (auth.Identity, bool) {
	user := strings.TrimSpace(r.Header.Get(HeaderForwardedUser))
	email := strings.TrimSpace(r.Header.Get(HeaderForwardedEmail))
	if user == "" && email == "" {
		return auth.Identity{}, false
	}
	groupsRaw := r.Header.Get(HeaderForwardedGroups)
	var groups []string
	if groupsRaw != "" {
		for _, g := range strings.Split(groupsRaw, ",") {
			if g = strings.TrimSpace(g); g != "" {
				groups = append(groups, g)
			}
		}
	}
	return auth.Identity{
		User:   user,
		Email:  email,
		Groups: groups,
		Source: auth.SourceHeader,
	}, true
}

// ParseTrustedProxyCIDRs parses a comma-separated CIDR list (the
// operator-facing config form, e.g., from
// CANOPY_TRUSTED_PROXY_CIDR). Skips empty entries. Returns an
// empty slice when the input is empty — that's the "disable header
// trust" mode. Exported so cmd/canopy/main.go can call it during
// startup.
func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	out := []*net.IPNet{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		out = append(out, cidr)
	}
	return out, nil
}
