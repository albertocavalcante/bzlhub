// Egress allowlist enforcement for fetch.Client.
//
// Goal: every outbound HTTP request canopy makes goes through the
// Client; the Client gates by destination host against an
// operator-configured allowlist. Default = empty = no enforcement
// (preserves existing behavior). Operator opts in via Client
// configuration at startup.
//
// Documented in docs/plans/08-corporate-security.md § "Network
// posture / Egress".

package fetch

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// defaultAllowedHosts is the process-wide allowlist applied to every
// fetch.Client created via NewClient. Set once at startup by
// cmd/bzlhub/main.go from BZLHUB_ALLOWED_HOSTS; left empty in tests
// and in personal-canopy where no enforcement is desired.
var (
	defaultAllowedHostsMu sync.RWMutex
	defaultAllowedHosts   []string
)

// SetDefaultAllowedHosts records the process-wide egress allowlist.
// Subsequent NewClient calls copy this slice into Client.AllowedHosts.
// Safe to call concurrently but intended to be called once at startup.
func SetDefaultAllowedHosts(hosts []string) {
	defaultAllowedHostsMu.Lock()
	defer defaultAllowedHostsMu.Unlock()
	defaultAllowedHosts = append([]string(nil), hosts...)
}

func snapshotDefaultAllowedHosts() []string {
	defaultAllowedHostsMu.RLock()
	defer defaultAllowedHostsMu.RUnlock()
	if len(defaultAllowedHosts) == 0 {
		return nil
	}
	out := make([]string, len(defaultAllowedHosts))
	copy(out, defaultAllowedHosts)
	return out
}

// ParseAllowedHosts splits a comma-separated host list into the
// canonical AllowedHosts slice. Whitespace around each entry is
// trimmed; empty entries are dropped. Returns nil for an empty input
// so callers can treat "no enforcement" as the zero value.
func ParseAllowedHosts(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ErrEgressDenied is returned by Client when an outbound request's
// destination host is not in the configured allowlist. Callers
// can errors.Is against this sentinel to surface the failure
// distinctly from other transport errors.
var ErrEgressDenied = errors.New("egress: destination host not in allowlist")

// hostAllowed reports whether `rawURL`'s host is permitted by the
// Client's AllowedHosts list. Returns true when the allowlist is
// empty (no enforcement). Returns false when the URL is unparseable
// (fail closed — better to break a malformed request than allow it
// through).
//
// Matching rules:
//
//   - Exact host match: "bcr.bazel.build" matches "bcr.bazel.build".
//   - Subdomain wildcard: "*.githubusercontent.com" matches any host
//     ending in ".githubusercontent.com" (e.g.,
//     "raw.githubusercontent.com"). Does NOT match the apex itself
//     — use both "*.githubusercontent.com" and
//     "githubusercontent.com" if both are wanted.
//
// Host comparison is case-insensitive (DNS-normalized). Port is
// ignored — allowlists are per-host, not per-port.
func (c *Client) hostAllowed(rawURL string) bool {
	return hostAllowed(rawURL, c.AllowedHosts)
}

func hostAllowed(rawURL string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	for _, pattern := range allowedHosts {
		pat := strings.ToLower(strings.TrimSpace(pattern))
		if pat == "" {
			continue
		}
		if pat == host {
			return true
		}
		if strings.HasPrefix(pat, "*.") {
			suffix := pat[1:] // ".foo.com"
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
	}
	return false
}

type allowlistTransport struct {
	base         http.RoundTripper
	allowedHosts []string
}

func (t allowlistTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !hostAllowed(req.URL.String(), t.allowedHosts) {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.String(), ErrEgressDenied)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
