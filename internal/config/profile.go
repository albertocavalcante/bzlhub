// Package config holds canopy's typed configuration model.
//
// Profile is the load-bearing knob. It selects which keys are valid,
// which backends are allowed, and which egress posture canopy adopts.
// See docs/plans/21-emu-and-explicit-egress.md for the full design and
// docs/plans/22-next-steps-sequencing.md for why this scaffolds first.
package config

import "fmt"

// Profile is a closed enum of deployment postures. Adding a value
// requires updating AllProfiles, String, and ParseProfile in lockstep;
// the test suite enforces this.
type Profile int

const (
	// ProfileDefault is the laptop / single-binary / OSS self-host
	// posture. Egress permitted; no mirror required. The behaviour
	// canopy has shipped since Phase 0.
	ProfileDefault Profile = iota

	// ProfileMirrorOnly is the corp-net posture. Egress is hard-denied;
	// the only data source is the configured internal mirror. Any
	// attempted outbound HTTP call returns ErrEgressForbidden and is
	// captured in the audit log.
	ProfileMirrorOnly

	// ProfileSyncRunner is the inverted posture: egress permitted (to
	// an allowlist), serve disabled by default, audit-every-call on.
	// Used for the one host that bridges public upstream registries
	// into the internal mirror.
	ProfileSyncRunner
)

// String renders the canonical name. Stable across releases; UI
// banners, audit-log entries and config docs all read it.
func (p Profile) String() string {
	switch p {
	case ProfileDefault:
		return "default"
	case ProfileMirrorOnly:
		return "mirror-only"
	case ProfileSyncRunner:
		return "sync-runner"
	default:
		return fmt.Sprintf("profile(%d)", int(p))
	}
}

// AllProfiles returns every known Profile in canonical order. The
// closure guard test asserts the length and order; adding a value
// without updating this slice surfaces immediately.
func AllProfiles() []Profile {
	return []Profile{ProfileDefault, ProfileMirrorOnly, ProfileSyncRunner}
}

// ParseProfile is the inverse of String for known values. Unknown
// input — including the empty string, casing variants, and
// underscore-vs-hyphen typos — is rejected with a diagnostic that
// names the offending input. Absence in config maps to ProfileDefault
// by the caller, not silently by this parser.
func ParseProfile(s string) (Profile, error) {
	for _, p := range AllProfiles() {
		if p.String() == s {
			return p, nil
		}
	}
	return 0, fmt.Errorf("unknown profile %q (valid: default, mirror-only, sync-runner)", s)
}
