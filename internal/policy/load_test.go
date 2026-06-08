package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePolicyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProfile_StrictDefaults(t *testing.T) {
	p, err := LoadProfile("strict")
	if err != nil {
		t.Fatalf("LoadProfile(strict): %v", err)
	}
	if p.Profile != "strict" {
		t.Errorf("Profile = %q", p.Profile)
	}
	if got := p.Auth.Actions["submit_request"]; got != GateAuthenticated {
		t.Errorf("strict.submit_request = %q, want authenticated", got)
	}
	if got := p.Auth.Actions["view_modules"]; got != GateAny {
		t.Errorf("strict.view_modules = %q, want any", got)
	}
	if got := p.Auth.Actions["approve_request"]; got != "group:approver" {
		t.Errorf("strict.approve_request = %q, want group:approver", got)
	}
	if p.MCP.WriteToolsEnabled {
		t.Error("strict.mcp.write_tools_enabled must default false")
	}
}

func TestLoadProfile_OpenDefaults(t *testing.T) {
	p, err := LoadProfile("open")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Auth.Actions["submit_request"]; got != GateAny {
		t.Errorf("open.submit_request = %q, want any", got)
	}
}

func TestLoadProfile_ClosedDefaults(t *testing.T) {
	p, err := LoadProfile("closed")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Auth.Actions["submit_request"]; got != "group:engineers" {
		t.Errorf("closed.submit_request = %q", got)
	}
	if got := p.Auth.Actions["view_modules"]; got != GateAuthenticated {
		t.Errorf("closed.view_modules = %q, want authenticated", got)
	}
	if got := p.Admission.Hermeticity["repository-rule-arbitrary-code"]; got != "deny" {
		t.Errorf("closed.hermeticity rep-rule = %q, want deny", got)
	}
}

func TestLoadProfile_Unknown(t *testing.T) {
	_, err := LoadProfile("bogus")
	if !errors.Is(err, ErrUnknownProfile) {
		t.Errorf("err = %v, want ErrUnknownProfile", err)
	}
}

func TestLoadFile_UserOverridesProfile(t *testing.T) {
	body := `version: 1
profile: strict
auth:
  actions:
    submit_request: group:reviewer
    view_audit: deny
`
	p, diags, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("unexpected diagnostics: %v", diags)
	}
	// Overrides took effect.
	if got := p.Auth.Actions["submit_request"]; got != "group:reviewer" {
		t.Errorf("submit_request = %q, want group:reviewer", got)
	}
	if got := p.Auth.Actions["view_audit"]; got != GateDeny {
		t.Errorf("view_audit = %q, want deny", got)
	}
	// Unchanged keys still carry strict defaults.
	if got := p.Auth.Actions["view_modules"]; got != GateAny {
		t.Errorf("view_modules = %q, want any (from strict baseline)", got)
	}
	if got := p.Auth.Actions["approve_request"]; got != "group:approver" {
		t.Errorf("approve_request = %q, want group:approver", got)
	}
}

func TestLoadFile_DefaultsToStrictWhenNoProfile(t *testing.T) {
	body := `version: 1
auth:
  actions:
    submit_request: deny
`
	p, _, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if p.Profile != "strict" {
		t.Errorf("Profile = %q, want strict by default", p.Profile)
	}
	if got := p.Auth.Actions["submit_request"]; got != GateDeny {
		t.Errorf("submit_request = %q, want deny", got)
	}
}

func TestLoadFile_VersionMismatch(t *testing.T) {
	body := `version: 99
profile: strict
`
	_, _, err := LoadFile(writePolicyFile(t, body))
	if err == nil {
		t.Fatal("want version-mismatch error")
	}
	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("err = %q, want 'unsupported version'", err)
	}
}

func TestLoadFile_UnknownProfileFallsBack(t *testing.T) {
	body := `version: 1
profile: bogus
auth:
  actions:
    submit_request: deny
`
	p, diags, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	// Must surface a diagnostic so the operator sees it.
	if len(diags) == 0 {
		t.Fatal("want a diagnostic about the unknown profile")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "bogus") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no diagnostic mentioned 'bogus': %v", diags)
	}
	// Should fall back to strict.
	if got := p.Auth.Actions["view_modules"]; got != GateAny {
		t.Errorf("view_modules = %q, want any (strict fallback)", got)
	}
	// Override still applied on top of fallback.
	if got := p.Auth.Actions["submit_request"]; got != GateDeny {
		t.Errorf("submit_request = %q, want deny", got)
	}
}

func TestLoadFile_Missing(t *testing.T) {
	_, _, err := LoadFile(filepath.Join(t.TempDir(), "no-such.yml"))
	if !errors.Is(err, ErrPolicyFileMissing) {
		t.Errorf("err = %v, want ErrPolicyFileMissing", err)
	}
}

func TestLoadFile_RejectsMalformedYAML(t *testing.T) {
	body := "this: : is not yaml ::"
	_, _, err := LoadFile(writePolicyFile(t, body))
	if err == nil {
		t.Fatal("want parse error")
	}
}

func TestLoadFile_OversizeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.yml")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 11*1024*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v, want 'too large'", err)
	}
}

// TestLoadFile_BoolFalseOverridesBaselineTrue captures the
// bool-zero-vs-explicit-false bug from the original
// mergeUserOnBaseline. A user file that explicitly sets a
// default-true bool to false must take effect.
func TestLoadFile_BoolFalseOverridesBaselineTrue(t *testing.T) {
	// strict baseline:
	//   admission.review.auto_pass_on_already_in_upstream: true
	// User explicitly turns it off.
	body := `version: 1
profile: strict
admission:
  review:
    auto_pass_on_already_in_upstream: false
`
	p, _, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if p.Admission.Review.AutoPassOnAlreadyInUpstream {
		t.Error("explicit false should override baseline true")
	}
}

// TestLoadFile_PartialNestedOverridePreservesSiblings captures
// the Maintainers-clobber bug from the original merge — setting one
// nested field used to wipe sibling fields in the same struct.
func TestLoadFile_PartialNestedOverridePreservesSiblings(t *testing.T) {
	// strict baseline review block:
	//   timeout_days: 14
	//   auto_pass_on_already_in_upstream: true
	// User overrides only timeout_days; the other must survive.
	body := `version: 1
profile: strict
admission:
  review:
    timeout_days: 7
`
	p, _, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if p.Admission.Review.TimeoutDays != 7 {
		t.Errorf("timeout_days = %d, want 7", p.Admission.Review.TimeoutDays)
	}
	if !p.Admission.Review.AutoPassOnAlreadyInUpstream {
		t.Error("unspecified sibling auto_pass_on_already_in_upstream lost (clobber bug)")
	}
}

// TestLoadFile_MaintainersPartialOverride is the regression test
// for the original Maintainers-clobber bug: setting one nested
// field used to wipe sibling fields in the same struct because
// the merge took `out.Maintainers = user.Maintainers` wholesale.
func TestLoadFile_MaintainersPartialOverride(t *testing.T) {
	// strict baseline: auto_grant_on_admission=true, min_per_module=1.
	// User only sets min_per_module=3.
	body := `version: 1
profile: strict
maintainers:
  min_per_module: 3
`
	p, _, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if p.Maintainers.MinPerModule != 3 {
		t.Errorf("min_per_module = %d, want 3", p.Maintainers.MinPerModule)
	}
	if !p.Maintainers.AutoGrantOnAdmission {
		t.Error("auto_grant_on_admission lost — sibling clobber bug")
	}
}

func TestLoadFile_PreservesParsedSectionsForwardCompat(t *testing.T) {
	// Forward-compat per Plan 71 Q71.3: sections we don't yet act
	// on still parse into the Policy struct so chunk 4/5/etc.
	// consumers find the values.
	body := `version: 1
profile: strict
admission:
  review:
    timeout_days: 7
naming:
  mode: suffixed
  suffix: .acme
maintainers:
  auto_grant_on_admission: false
git:
  remote: forgejo.example.com/team/registry
  adapter: forgejo
`
	p, _, err := LoadFile(writePolicyFile(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if p.Admission.Review.TimeoutDays != 7 {
		t.Errorf("review.timeout_days = %d", p.Admission.Review.TimeoutDays)
	}
	if p.Naming.Mode != "suffixed" || p.Naming.Suffix != ".acme" {
		t.Errorf("naming = %+v", p.Naming)
	}
	if p.Maintainers.AutoGrantOnAdmission {
		t.Errorf("maintainers.auto_grant_on_admission must be false")
	}
	if p.Git.Adapter != "forgejo" || p.Git.Remote == "" {
		t.Errorf("git = %+v", p.Git)
	}
}
