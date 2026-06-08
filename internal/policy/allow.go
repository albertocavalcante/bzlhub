package policy

import (
	"slices"

	"github.com/albertocavalcante/bzlhub/internal/auth"
)

// Allow reports whether the identity is permitted to perform action
// per the effective policy.
//
// Action-name lookup: action is keyed verbatim against
// policy.Auth.Actions; missing entries default to deny — a typoed
// action name shouldn't quietly grant access.
//
// Gate semantics:
//   - GateAny           → always true (anonymous and authenticated)
//   - GateAuthenticated → true when identity.IsAuthenticated()
//   - "group:<name>"    → true when identity is authenticated AND
//                         carries <name> in its Groups slice
//   - GateMaintainer    → always false (per-target gate — use
//                         Evaluator.AllowFor with the target)
//   - GateDeny / unknown → false
//
// Pure read against the in-memory policy; concurrent calls are safe.
func (p *Policy) Allow(id auth.Identity, action string) bool {
	if p == nil {
		return false
	}
	gate, ok := p.Auth.Actions[action]
	if !ok {
		return false
	}
	switch gate {
	case GateAny:
		return true
	case GateAuthenticated:
		return id.IsAuthenticated()
	case GateMaintainer:
		// Per-target — Allow alone can't answer; safer to deny.
		return false
	case GateDeny:
		return false
	}
	if gate.IsGroup() {
		if !id.IsAuthenticated() {
			return false
		}
		return slices.Contains(id.Groups, gate.GroupName())
	}
	// Unrecognized gate string. Refuse rather than guess.
	return false
}
