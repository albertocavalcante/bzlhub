package policy

import (
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/auth"
)

func anonymous() auth.Identity {
	return auth.Anonymous()
}

func user(email string, groups ...string) auth.Identity {
	return auth.Identity{
		Email:  email,
		Groups: groups,
		Source: auth.SourceBearer,
	}
}

func policyWithGate(action string, gate Gate) *Policy {
	return &Policy{
		Auth: Auth{Actions: map[string]Gate{action: gate}},
	}
}

func TestAllow_AnyGate_AllowsAnonymous(t *testing.T) {
	p := policyWithGate("view_modules", GateAny)
	if !p.Allow(anonymous(), "view_modules") {
		t.Error("any gate must allow anonymous")
	}
}

func TestAllow_AnyGate_AllowsUser(t *testing.T) {
	p := policyWithGate("view_modules", GateAny)
	if !p.Allow(user("alice@example.com"), "view_modules") {
		t.Error("any gate must allow authenticated user")
	}
}

func TestAllow_AuthenticatedGate_DeniesAnonymous(t *testing.T) {
	p := policyWithGate("submit_request", GateAuthenticated)
	if p.Allow(anonymous(), "submit_request") {
		t.Error("authenticated gate must deny anonymous")
	}
}

func TestAllow_AuthenticatedGate_AllowsUser(t *testing.T) {
	p := policyWithGate("submit_request", GateAuthenticated)
	if !p.Allow(user("alice@example.com"), "submit_request") {
		t.Error("authenticated gate must allow identified user")
	}
}

func TestAllow_GroupGate_AllowsMember(t *testing.T) {
	p := policyWithGate("approve_request", "group:approver")
	if !p.Allow(user("alice@example.com", "approver"), "approve_request") {
		t.Error("group gate must allow group member")
	}
}

func TestAllow_GroupGate_DeniesNonMember(t *testing.T) {
	p := policyWithGate("approve_request", "group:approver")
	if p.Allow(user("alice@example.com", "reader"), "approve_request") {
		t.Error("group gate must deny non-member")
	}
}

func TestAllow_GroupGate_DeniesAnonymous(t *testing.T) {
	p := policyWithGate("approve_request", "group:approver")
	if p.Allow(anonymous(), "approve_request") {
		t.Error("group gate must deny anonymous")
	}
}

func TestAllow_DenyGate_AlwaysFalse(t *testing.T) {
	p := policyWithGate("destructive_op", GateDeny)
	cases := []auth.Identity{
		anonymous(),
		user("alice@example.com"),
		user("admin@example.com", "approver", "engineers"),
	}
	for _, id := range cases {
		if p.Allow(id, "destructive_op") {
			t.Errorf("deny gate must always reject; allowed %+v", id)
		}
	}
}

// Plan 72 §C6: maintainer gate is per-target. Allow() (global) MUST
// return false for it — chunk 4 wires AllowFor(action, target) with
// the DB lookup. A v0 handler accidentally calling Allow() instead
// of AllowFor() for a per-target action gets the safer default.
func TestAllow_MaintainerGate_ReturnsFalseInV0(t *testing.T) {
	p := policyWithGate("maintain_module", GateMaintainer)
	if p.Allow(user("alice@example.com"), "maintain_module") {
		t.Error("maintainer gate via Allow() must return false (use AllowFor)")
	}
}

// Per Plan 71 resolution rule: missing actions in the merged policy
// fall back to deny (closed default), and Allow emits no panic.
func TestAllow_UnknownAction_DeniesByDefault(t *testing.T) {
	p := &Policy{Auth: Auth{Actions: map[string]Gate{}}}
	if p.Allow(user("alice@example.com"), "never_declared_action") {
		t.Error("unknown action must default to deny")
	}
}

// Integration with LoadFile: gate behavior end-to-end after profile
// merge. Pins that the merge produces a Policy whose Allow() reads
// like the YAML actually said.
func TestAllow_EndToEnd_AfterLoadFile(t *testing.T) {
	body := `version: 1
profile: strict
auth:
  actions:
    submit_request: group:reviewer
`
	p, _, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatal(err)
	}

	// strict baseline says view_modules: any.
	if !p.Allow(anonymous(), "view_modules") {
		t.Error("view_modules should allow anonymous (strict baseline)")
	}
	// Override applied — only group:reviewer can submit.
	if p.Allow(user("alice@example.com"), "submit_request") {
		t.Error("submit_request should deny user without reviewer group")
	}
	if !p.Allow(user("alice@example.com", "reviewer"), "submit_request") {
		t.Error("submit_request should allow reviewer-group user")
	}
}
