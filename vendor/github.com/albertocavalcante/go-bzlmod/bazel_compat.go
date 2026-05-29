package gobzlmod

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/albertocavalcante/go-bzlmod/selection/version"
)

// bazelCompatConstraint represents a parsed bazel_compatibility constraint.
//
// Reference: ModuleFileGlobals.java lines 213-225
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
type bazelCompatConstraint struct {
	operator string // ">=", "<=", ">", "<", "-"
	version  string // The version part (e.g., "7.0.0")
}

// bazelCompatConstraintPattern matches bazel_compatibility entries.
// Format: (>=|<=|>|<|-)X.Y.Z
var bazelCompatConstraintPattern = regexp.MustCompile(`^(>=|<=|>|<|-)(\d+\.\d+\.\d+)$`)

// parseBazelCompatConstraint parses a bazel_compatibility constraint string.
func parseBazelCompatConstraint(s string) (*bazelCompatConstraint, error) {
	match := bazelCompatConstraintPattern.FindStringSubmatch(s)
	if match == nil {
		return nil, fmt.Errorf("invalid bazel_compatibility constraint: %q", s)
	}
	return &bazelCompatConstraint{
		operator: match[1],
		version:  match[2],
	}, nil
}

// checkBazelCompatibility checks if the given Bazel version satisfies a constraint.
//
// Reference: BazelModuleResolutionFunction.java lines 298-333
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/BazelModuleResolutionFunction.java
func (c *bazelCompatConstraint) check(bazelVersion string) bool {
	cmp := version.Compare(bazelVersion, c.version)

	switch c.operator {
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case "-":
		// Exclusion: the Bazel version must NOT equal the constraint version
		return cmp != 0
	default:
		return false
	}
}

// checkBazelCompatibility checks if a Bazel version satisfies all bazel_compatibility constraints.
// Returns (compatible, reason, invalidConstraints) where:
//   - compatible: true if all valid constraints are satisfied
//   - reason: explanation of why it's incompatible (empty if compatible)
//   - invalidConstraints: list of constraints that couldn't be parsed (for warning purposes)
//
// Reference: BazelModuleResolutionFunction.java lines 298-333
func checkBazelCompatibility(bazelVersion string, constraints []string) (bool, string, []string) {
	if len(constraints) == 0 {
		return true, "", nil
	}

	if bazelVersion == "" {
		return true, "", nil // No Bazel version specified, skip validation
	}

	// Normalize the Bazel version (strip any prerelease/build metadata for comparison)
	normalizedBazel := normalizeBazelVersion(bazelVersion)

	var failedConstraints []string
	var invalidConstraints []string
	for _, constraintStr := range constraints {
		constraint, err := parseBazelCompatConstraint(constraintStr)
		if err != nil {
			// Track invalid constraints for warning purposes
			invalidConstraints = append(invalidConstraints, constraintStr)
			continue
		}

		if !constraint.check(normalizedBazel) {
			failedConstraints = append(failedConstraints, constraintStr)
		}
	}

	if len(failedConstraints) == 0 {
		return true, "", invalidConstraints
	}

	// Build explanation
	if len(failedConstraints) == 1 {
		return false, fmt.Sprintf("requires %s", failedConstraints[0]), invalidConstraints
	}
	return false, fmt.Sprintf("requires %s", strings.Join(failedConstraints, " and ")), invalidConstraints
}

// normalizeBazelVersion strips prerelease and build metadata from a Bazel version.
// For example, "7.0.0-pre.20231115.1" becomes "7.0.0".
func normalizeBazelVersion(v string) string {
	// Find the first hyphen or plus sign
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}

	// Validate it looks like a version (X.Y.Z)
	parts := strings.Split(v, ".")
	if len(parts) >= 3 {
		// Ensure first 3 parts are numeric
		for i := 0; i < 3; i++ {
			if _, err := strconv.Atoi(parts[i]); err != nil {
				return v // Return original if not valid
			}
		}
		return strings.Join(parts[:3], ".")
	}

	return v
}

// checkModuleBazelCompatibility checks all resolved modules for Bazel compatibility
// and populates the IsBazelIncompatible and BazelIncompatibilityReason fields.
func checkModuleBazelCompatibility(modules []ModuleToResolve, moduleInfoCache map[string]*ModuleInfo, bazelVersion string) {
	for i := range modules {
		m := &modules[i]

		// Get the module's bazel_compatibility constraints from the cache
		key := m.Name + "@" + m.Version
		if info, ok := moduleInfoCache[key]; ok && len(info.BazelCompatibility) > 0 {
			m.BazelCompatibility = info.BazelCompatibility
			compatible, reason, _ := checkBazelCompatibility(bazelVersion, info.BazelCompatibility)
			// Note: invalidConstraints are ignored here as they were already validated during parsing
			if !compatible {
				m.IsBazelIncompatible = true
				m.BazelIncompatibilityReason = reason
			}
		}
	}
}
