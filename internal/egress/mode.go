// Package egress is the single point of contact between canopy
// and any outbound HTTP host. All callers route through Client(ctx)
// or NewHTTPClient(policy); the golangci-lint forbidigo rule
// banning raw http.Client construction outside this package
// enforces it (added in commit C4 of Plan 28's day-one chain).
//
// The package implements four things:
//
//   - Mode: the policy posture (allow / deny / audit).
//   - Policy: the policy data (mode + allowlist).
//   - HTTP client wiring: a stdlib *http.Client whose transport
//     consults the policy on every RoundTrip.
//   - Audit events: a JSONL-shaped record per outbound call
//     (success, denial, network failure).
//
// Mode-specific behaviour aligns with the three canopy profiles:
//
//   - default → ModeAllow with empty allowlist (legacy posture).
//   - mirror-only → ModeDeny.
//   - sync-runner → ModeAllow with non-empty allowlist + ModeAudit.
//
// See docs/plans/20-airgap-and-bcr-fork.md (the original egress
// design) and docs/plans/21-emu-and-explicit-egress.md (the EMU
// posture that made the lint check load-bearing).
package egress

import "fmt"

// Mode controls how Policy.Check evaluates outbound requests.
// Closed enum — adding a value requires updating String, the
// policy logic, and the test suite together.
type Mode int

const (
	// ModeAllow is the permissive default. Empty allowlist passes
	// everything (legacy behaviour); non-empty allowlist gates by
	// hostname.
	ModeAllow Mode = iota

	// ModeDeny refuses every request unconditionally. The allowlist
	// is ignored. This is the mirror-only profile's posture.
	ModeDeny

	// ModeAudit is policy-permissive but emission-mandatory: every
	// request is allowed (subject to the allowlist) AND every
	// request, success or failure, is recorded in the audit log.
	// The sync-runner profile uses this to produce the auditable
	// byproduct of its egress.
	ModeAudit
)

// String returns the canonical token embedded in audit-log entries.
// Stable across releases — operators key dashboards off it.
func (m Mode) String() string {
	switch m {
	case ModeAllow:
		return "allow"
	case ModeDeny:
		return "deny"
	case ModeAudit:
		return "audit"
	default:
		return fmt.Sprintf("mode(%d)", int(m))
	}
}
