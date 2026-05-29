package config

import (
	"strings"
	"testing"
)

// TestProfileStringCanonical asserts every Profile value renders to a
// stable, lowercase, hyphenated string. UI banners, audit-log entries
// and config docs all read these names; if the strings move the world
// moves with them.
func TestProfileStringCanonical(t *testing.T) {
	cases := []struct {
		p    Profile
		want string
	}{
		{ProfileDefault, "default"},
		{ProfileMirrorOnly, "mirror-only"},
		{ProfileSyncRunner, "sync-runner"},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Profile(%d).String() = %q, want %q", c.p, got, c.want)
		}
	}
}

// TestParseProfileRoundTrip asserts ParseProfile is the inverse of
// String for every known profile.
func TestParseProfileRoundTrip(t *testing.T) {
	for _, p := range AllProfiles() {
		got, err := ParseProfile(p.String())
		if err != nil {
			t.Errorf("ParseProfile(%q) errored: %v", p.String(), err)
			continue
		}
		if got != p {
			t.Errorf("ParseProfile(%q) = %v, want %v", p.String(), got, p)
		}
	}
}

// TestParseProfileRejectsUnknown asserts the parser refuses anything
// outside the closed enum, with a diagnostic that names the offending
// input. The empty string is rejected too — there is no "no profile"
// state; absence in config means ProfileDefault, but the parser does
// not silently coerce.
func TestParseProfileRejectsUnknown(t *testing.T) {
	for _, in := range []string{"", "production", "airgap", "DEFAULT", "Default", "mirror_only"} {
		_, err := ParseProfile(in)
		if err == nil {
			t.Errorf("ParseProfile(%q) returned no error; expected rejection", in)
			continue
		}
		if !strings.Contains(err.Error(), in) && in != "" {
			t.Errorf("ParseProfile(%q) error %q did not name the offending input", in, err)
		}
	}
}

// TestAllProfilesClosure asserts AllProfiles() returns exactly the
// three known values, in canonical order. Closure guard: adding a new
// Profile value without updating AllProfiles will break this test —
// the failure is the reminder.
func TestAllProfilesClosure(t *testing.T) {
	got := AllProfiles()
	if len(got) != 3 {
		t.Fatalf("AllProfiles() returned %d profiles, want 3", len(got))
	}
	want := []Profile{ProfileDefault, ProfileMirrorOnly, ProfileSyncRunner}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("AllProfiles()[%d] = %v, want %v", i, got[i], p)
		}
	}
}
