package config

import "fmt"

// Config is canopy's typed deployment configuration. It is the
// single source of truth for runtime policy decisions: which profile
// is active, which hosts may be reached for egress, which backends
// are configured, and so on. Wire format (YAML) lives elsewhere; this
// type is what every consumer reads after parsing + validation.
//
// The zero value is intentionally a working configuration:
// profile=default, no egress allowlist. A canopy invoked with no
// config file behaves exactly like a canopy invoked with an empty
// one.
type Config struct {
	// Profile selects the deployment posture. See profile.go and
	// docs/plans/21-emu-and-explicit-egress.md.
	Profile Profile

	// Egress holds the outbound-HTTP policy. Only sync-runner and
	// default profiles use it meaningfully; mirror-only must leave
	// it empty (Validate enforces this).
	Egress EgressConfig
}

// EgressConfig is the outbound-HTTP policy. Mode controls the
// enforcement posture (allow / deny / audit); Allow is the hostname
// allowlist consulted when Mode is allow or audit. Both fields are
// future-shaped for the full Plan 20/21 egress wiring — this commit
// adds only the validation gate so misconfigured profiles fail fast
// at startup; the runtime client lands in a subsequent commit.
type EgressConfig struct {
	// Allow is the list of hostnames permitted for outbound HTTP.
	// Empty allow with profile=default means "no enforcement" (legacy
	// behaviour); empty allow with profile=mirror-only is the
	// required shape. A non-empty allow with profile=mirror-only is
	// a configuration error.
	Allow []string

	// Mode is reserved for the egress-package wiring. Empty string
	// today; populated in the C3 commit.
	Mode string
}

// Validate walks the config and returns the first inconsistency that
// would make canopy unsafe to run. Diagnostics name both the offending
// profile and the offending key — the caller renders them; we don't
// truncate.
//
// Adding a new validation rule belongs here, alongside a failing test
// that exercises the rule. The TDD anchor is config_test.go.
func (c *Config) Validate() error {
	// Plan 21 §"mirror-only" profile: hard-deny egress posture is
	// incompatible with any allowlist. The allowlist is the corp-net
	// expression of "this host may talk out"; mirror-only is the
	// expression of "this host may not." Both being set is a
	// misconfiguration that almost always means the operator copied
	// a sync-runner config into a corp-net deployment.
	if c.Profile == ProfileMirrorOnly && len(c.Egress.Allow) > 0 {
		return fmt.Errorf(
			"profile %q forbids egress.allow entries (%d configured); "+
				"mirror-only canopies must have an empty egress.allow list",
			c.Profile, len(c.Egress.Allow),
		)
	}

	return nil
}
