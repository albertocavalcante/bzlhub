package egress

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// reasonEgressPolicyDeny is the canonical reason code embedded in
// audit-log entries on policy denial. Stable across releases.
const reasonEgressPolicyDeny = "egress-policy-deny"

// ErrEgressForbidden is the sentinel returned when Policy.Check
// refuses a request. Callers can errors.Is against this value to
// distinguish policy denial (the host is not allowed to leave the
// network) from transport failure (the host could not be reached).
// Stays sentinel-shaped, not Errorf-wrapped, so the comparison is
// cheap and the diagnostic shape doesn't change with refactors.
var ErrEgressForbidden = errors.New("egress forbidden by policy")

// Policy is the data half of the egress decision: the active mode
// and the allowlist of permitted hostnames.
//
// The zero value (ModeAllow + empty allowlist) is the permissive
// default; canopy callers that don't need policy enforcement get
// it for free.
type Policy struct {
	// Mode controls the decision shape. See mode.go.
	Mode Mode

	// Allow is the hostname allowlist. Consulted only when Mode is
	// ModeAllow or ModeAudit; ignored under ModeDeny. Hostname
	// matching is exact (no subdomain glob); add an entry per host
	// you intend to permit.
	Allow []string
}

// Check inspects the outbound request and returns nil if it is
// permitted, or a denial error wrapping ErrEgressForbidden if not.
// The wrapping error message names the offending host so the
// audit log entry and the operator diagnostic are both readable.
func (p Policy) Check(req *http.Request) error {
	if req == nil || req.URL == nil {
		// Malformed input is a denial under any mode — the
		// alternative is a silent leak.
		return fmt.Errorf("%w: nil request or URL", ErrEgressForbidden)
	}
	host := req.URL.Hostname()

	if p.Mode == ModeDeny {
		return fmt.Errorf("%w: profile denies all egress (host=%s)", ErrEgressForbidden, host)
	}

	// ModeAllow and ModeAudit share the allowlist semantics:
	// empty allowlist → permit everything (legacy default);
	// non-empty allowlist → permit only listed hosts.
	if len(p.Allow) == 0 {
		return nil
	}
	if slices.Contains(p.Allow, host) {
		return nil
	}
	return fmt.Errorf("%w: host %s not in allowlist", ErrEgressForbidden, host)
}
