package config

import (
	"fmt"
	"os"
	"strings"
)

const (
	EnvProfile      = "BZLHUB_PROFILE"
	EnvAllowedHosts = "BZLHUB_ALLOWED_HOSTS"
)

// LoadEnvironment resolves the process deployment profile and egress
// allowlist. The profile owns the mode so operators cannot accidentally
// combine a mirror-only name with an allow-mode transport.
func LoadEnvironment() (*Config, error) {
	profile := ProfileDefault
	if raw := strings.TrimSpace(os.Getenv(EnvProfile)); raw != "" {
		parsed, err := ParseProfile(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvProfile, err)
		}
		profile = parsed
	}

	cfg := &Config{
		Profile: profile,
		Egress: EgressConfig{
			Allow: parseCSV(os.Getenv(EnvAllowedHosts)),
			Mode:  profileEgressMode(profile),
		},
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func profileEgressMode(p Profile) string {
	switch p {
	case ProfileMirrorOnly:
		return "deny"
	case ProfileSyncRunner:
		return "audit"
	default:
		return "allow"
	}
}

func parseCSV(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
