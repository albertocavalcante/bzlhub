package preflight

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/policy"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Checker decides the verdict for one request. Implementations are
// pluggable so the runner can swap in fetch-based, license-detect,
// and hermeticity-classify checkers without runner changes.
//
// Check is called from a worker goroutine. Implementations must be
// goroutine-safe (Workers > 1 may invoke concurrently).
type Checker interface {
	Check(ctx context.Context, req store.Request) Verdict
}

// DefaultChecker performs URL-shape validation, host allowlist /
// denylist enforcement, and (when wired) a cascade short-circuit
// against an upstream BCR.
//
// Verdict order:
//  1. require_https + http:// → denied
//  2. host in denylisted_hosts → denied
//  3. allowed_hosts non-empty + host not in list → denied
//  4. cascade probe says upstream already has (m, v) AND policy
//     auto_pass_on_already_in_upstream → auto_pass
//  5. otherwise → needs_review (human looks)
//
// Cascade probe errors degrade safely to needs_review. The probe
// itself is optional — leave Cascade nil to skip step 4.
type DefaultChecker struct {
	policy policy.Snapshot

	// Cascade is the upstream-presence probe consulted in step 4.
	// Nil disables the short-circuit; every URL-validated request
	// then routes to needs_review.
	Cascade CascadeProbe
}

// NewDefaultChecker constructs a DefaultChecker wrapping a policy
// snapshot getter. The getter is consulted on every Check call so
// SIGHUP-driven policy reloads take effect on the next request
// without re-constructing the checker.
//
// snap may return nil — the checker then has no policy to enforce
// and emits needs_review for every request. Cascade is wired
// separately by the caller (serve.go) so tests can stub it.
func NewDefaultChecker(snap policy.Snapshot) *DefaultChecker {
	return &DefaultChecker{policy: snap}
}

// Check implements Checker.
func (c *DefaultChecker) Check(ctx context.Context, req store.Request) Verdict {
	pol := c.snapshot()
	if pol != nil {
		if v, deny := denyByURLShape(pol, req); deny {
			return v
		}
		if v, deny := denyByHostPolicy(pol, req); deny {
			return v
		}
		if v, hit := c.autoPassByCascade(ctx, pol, req); hit {
			return v
		}
	}
	return Verdict{
		NextState: store.RequestStateNeedsReview,
		Notes:     "submitted for human review",
	}
}

// snapshot returns the current policy, or nil when no source is
// wired.
func (c *DefaultChecker) snapshot() *policy.Policy {
	if c.policy == nil {
		return nil
	}
	return c.policy()
}

// denyByURLShape enforces admission.source.require_https.
func denyByURLShape(pol *policy.Policy, req store.Request) (Verdict, bool) {
	if !pol.Admission.Source.RequireHTTPS {
		return Verdict{}, false
	}
	if req.SourceURL == "" {
		return Verdict{}, false
	}
	if strings.HasPrefix(req.SourceURL, "https://") {
		return Verdict{}, false
	}
	return Verdict{
		NextState: store.RequestStateDenied,
		Reason:    "source_url must be https:// (policy admission.source.require_https=true)",
	}, true
}

// denyByHostPolicy enforces denylist + allowed_hosts. Denylist is
// checked first so an entry shared by both lists denies (operator
// intent: a denylist edit means "stop accepting this even if I had
// it allowlisted somewhere").
func denyByHostPolicy(pol *policy.Policy, req store.Request) (Verdict, bool) {
	host := hostOf(req.SourceURL)
	if host == "" {
		return Verdict{}, false
	}
	if slices.Contains(pol.Admission.Source.DenylistedHosts, host) {
		return Verdict{
			NextState: store.RequestStateDenied,
			Reason:    "source host " + host + " is denylisted (policy admission.source.denylisted_hosts)",
		}, true
	}
	allow := pol.Admission.Source.AllowedHosts
	if len(allow) > 0 && !slices.Contains(allow, host) {
		return Verdict{
			NextState: store.RequestStateDenied,
			Reason:    "source host " + host + " is not in the allowlist (policy admission.source.allowed_hosts)",
		}, true
	}
	return Verdict{}, false
}

// autoPassByCascade short-circuits to auto_pass when the upstream
// already publishes (m, v) AND the policy enables that shortcut.
// On hit, the upstream's source location is attached to the
// Verdict so admit can fetch from it when the request had no
// source_url of its own. Probe errors degrade to needs_review.
func (c *DefaultChecker) autoPassByCascade(ctx context.Context, pol *policy.Policy, req store.Request) (Verdict, bool) {
	if c.Cascade == nil || !pol.Admission.Review.AutoPassOnAlreadyInUpstream {
		return Verdict{}, false
	}
	hit, err := c.Cascade.Has(ctx, req.Module, req.Version)
	if err != nil || hit == nil {
		return Verdict{}, false
	}
	return Verdict{
		NextState:     store.RequestStateAutoPass,
		Notes:         "already published in upstream BCR — skipping human review",
		CascadeSource: hit,
	}, true
}

// hostOf returns the lowercased host of u, or "" when u doesn't
// parse or has no host component (rare for procurement URLs).
func hostOf(u string) string {
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
