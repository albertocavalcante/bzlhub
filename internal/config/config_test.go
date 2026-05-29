package config

import (
	"strings"
	"testing"
)

// TestConfigValidate_MirrorOnlyRejectsNonEmptyEgressAllow is the
// load-bearing contract from Plan 21 §"mirror-only" profile: a corp-net
// canopy MUST NOT carry any egress allowlist entries. The configured
// allowlist is the policy-shaped expression of "this host may talk to
// the public internet"; mirror-only is the policy-shaped expression of
// "this host may not." The two are mutually exclusive by design, and
// the lint catches accidental misconfiguration before serve starts.
//
// The error message MUST name both the offending profile and the
// offending key. A diagnostic that says "config invalid" is a foot-gun
// at 3 AM; "profile mirror-only forbids egress.allow entries" is not.
func TestConfigValidate_MirrorOnlyRejectsNonEmptyEgressAllow(t *testing.T) {
	c := &Config{
		Profile: ProfileMirrorOnly,
		Egress:  EgressConfig{Allow: []string{"github.com"}},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil; expected rejection of mirror-only + egress.allow")
	}
	msg := err.Error()
	if !strings.Contains(msg, "mirror-only") {
		t.Errorf("error %q does not name the offending profile", msg)
	}
	if !strings.Contains(msg, "egress.allow") {
		t.Errorf("error %q does not name the offending config key", msg)
	}
}

// TestConfigValidate_MirrorOnlyAcceptsEmptyEgressAllow guards the
// happy path: mirror-only with an empty allow list (the intended
// shape) validates cleanly.
func TestConfigValidate_MirrorOnlyAcceptsEmptyEgressAllow(t *testing.T) {
	c := &Config{Profile: ProfileMirrorOnly}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v; expected nil for mirror-only + empty egress.allow", err)
	}
}

// TestConfigValidate_DefaultAllowsNonEmptyEgressAllow guards against a
// well-meaning over-restriction: only mirror-only forbids the
// allowlist. The default profile is permissive by definition and must
// accept any allowlist value.
func TestConfigValidate_DefaultAllowsNonEmptyEgressAllow(t *testing.T) {
	c := &Config{
		Profile: ProfileDefault,
		Egress:  EgressConfig{Allow: []string{"github.com"}},
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v; expected nil for default + non-empty egress.allow", err)
	}
}

// TestConfigValidate_SyncRunnerAllowsNonEmptyEgressAllow guards the
// inverted posture: sync-runner is the host that's *supposed* to
// egress, so an allowlist is the expected shape (a non-empty allow
// list is exactly how a sync-runner is configured securely).
func TestConfigValidate_SyncRunnerAllowsNonEmptyEgressAllow(t *testing.T) {
	c := &Config{
		Profile: ProfileSyncRunner,
		Egress:  EgressConfig{Allow: []string{"github.com", "bcr.bazel.build"}},
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v; expected nil for sync-runner + non-empty egress.allow", err)
	}
}

// TestConfigValidate_ZeroValueIsValid asserts the documented default:
// a Config{} (zero value) is the same as profile=default with no
// egress allowlist. This means a missing config file or an unset
// profile key both map to permissive-default behaviour, NOT to an
// error. The parser refusal in profile_test.go's
// TestParseProfileRejectsUnknown applies only when a profile key is
// *present and wrong*, not when it's absent.
func TestConfigValidate_ZeroValueIsValid(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() on zero-value Config = %v; expected nil", err)
	}
}
