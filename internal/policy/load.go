package policy

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"

	"github.com/goccy/go-yaml"
)

//go:embed profiles/*.yml
var profilesFS embed.FS

// maxPolicyFileBytes caps how much of policy.yml canopy will read at
// boot. A real deployment's policy is ~100 lines (~5 KiB); 10 MB is
// far past any plausible config. Symmetric with the identity-file
// cap — both defend against mis-mounted binds.
const maxPolicyFileBytes = 10 * 1024 * 1024

const supportedVersion = 1

// ErrPolicyFileMissing is returned by LoadFile when path doesn't
// exist, distinct from generic IO error so callers can branch on
// missing-vs-malformed.
var ErrPolicyFileMissing = errors.New("policy: file missing")

// ErrUnknownProfile is returned by LoadProfile when name isn't one
// of strict/open/closed.
var ErrUnknownProfile = errors.New("policy: unknown profile")

// LoadProfile returns the embedded baseline policy for one of
// strict, open, closed. Used by LoadFile to seed defaults before
// merging operator overrides on top.
func LoadProfile(name string) (*Policy, error) {
	switch name {
	case "strict", "open", "closed":
	default:
		return nil, fmt.Errorf("%w: %q (want one of: strict, open, closed)", ErrUnknownProfile, name)
	}
	body, err := fs.ReadFile(profilesFS, "profiles/"+name+".yml")
	if err != nil {
		return nil, fmt.Errorf("policy: embedded profile %q unreadable: %w", name, err)
	}
	var p Policy
	if err := yaml.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("policy: embedded profile %q: %w", name, err)
	}
	return &p, nil
}

// LoadFile parses policy.yml at path, applies the profile baseline
// underneath any operator overrides, and returns the merged Policy
// plus any non-fatal diagnostics.
//
// Returns ErrPolicyFileMissing when path doesn't exist; other errors
// mean the file is present but unusable.
//
// Merge semantics: operator overrides deep-merge onto the baseline.
// Nested maps recurse (keys present in both: user wins per key, keys
// only in baseline are preserved). Scalars, slices, and type
// mismatches: user value replaces. Bool false IS a value — setting
// a default-true field to false in the user file takes effect.
func LoadFile(path string) (*Policy, []Diagnostic, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrPolicyFileMissing
		}
		return nil, nil, fmt.Errorf("policy: open %s: %w", path, err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxPolicyFileBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	if len(body) > maxPolicyFileBytes {
		return nil, nil, fmt.Errorf("policy: %s too large (>%d bytes); refusing to parse", path, maxPolicyFileBytes)
	}

	// Two-pass: header first so we can pick the baseline profile
	// before parsing the full body. A separate tiny struct avoids
	// partially-populating the full Policy on a malformed file.
	var head struct {
		Version int    `yaml:"version"`
		Profile string `yaml:"profile"`
	}
	if err := yaml.Unmarshal(body, &head); err != nil {
		return nil, nil, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	if head.Version != supportedVersion {
		return nil, nil, fmt.Errorf("policy: %s: unsupported version %d (this build supports version %d)", path, head.Version, supportedVersion)
	}

	var diags []Diagnostic
	profileName := head.Profile
	if profileName == "" {
		profileName = "strict"
	}
	baseline, err := LoadProfile(profileName)
	if err != nil {
		if !errors.Is(err, ErrUnknownProfile) {
			return nil, nil, err
		}
		diags = append(diags, Diagnostic{
			Path:    "profile",
			Message: fmt.Sprintf("unknown profile %q; falling back to strict (known profiles: strict, open, closed)", profileName),
		})
		baseline, err = LoadProfile("strict")
		if err != nil {
			return nil, nil, err
		}
	}

	merged, err := mergeYAML(baseline, body)
	if err != nil {
		return nil, nil, fmt.Errorf("policy: merge %s: %w", path, err)
	}
	return merged, diags, nil
}

// mergeYAML produces an effective Policy by deep-merging the user
// file (operator overrides) onto the baseline. Round-trips through
// map[string]any so the merge semantics are uniform across every
// field — adding a new Policy field doesn't require an entry here.
//
// Nested maps recurse; scalars/slices replace. YAML distinguishes
// explicit `false` from "key absent," so a user setting a
// default-true bool to false takes effect.
func mergeYAML(baseline *Policy, userYAML []byte) (*Policy, error) {
	baselineYAML, err := yaml.Marshal(baseline)
	if err != nil {
		return nil, fmt.Errorf("marshal baseline: %w", err)
	}
	var baselineMap, userMap map[string]any
	if err := yaml.Unmarshal(baselineYAML, &baselineMap); err != nil {
		return nil, fmt.Errorf("decode baseline: %w", err)
	}
	if err := yaml.Unmarshal(userYAML, &userMap); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}
	mergedMap := deepMergeMaps(baselineMap, userMap)
	mergedYAML, err := yaml.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("marshal merged: %w", err)
	}
	var out Policy
	if err := yaml.Unmarshal(mergedYAML, &out); err != nil {
		return nil, fmt.Errorf("decode merged: %w", err)
	}
	return &out, nil
}

// deepMergeMaps recursively merges overlay onto base. Returns a
// fresh map; doesn't mutate either input.
//
// Semantics per key:
//   - both values are maps → recurse
//   - otherwise → overlay wins (including overlay's nil/zero values,
//     since YAML distinguishes those from "key absent")
//
// Slices replace wholesale — half-merged operator-facing lists
// confuse more than they help.
func deepMergeMaps(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	maps.Copy(out, base)
	for k, v := range overlay {
		if bv, ok := out[k]; ok {
			if bm, bok := bv.(map[string]any); bok {
				if om, ook := v.(map[string]any); ook {
					out[k] = deepMergeMaps(bm, om)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}
