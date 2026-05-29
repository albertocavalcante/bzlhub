// Package label provides types for Bazel module labels and versions.
//
// # Version Format
//
// Bazel module versions follow a format similar to SemVer but with Bazel-specific
// extensions. The format is: RELEASE[-PRERELEASE][+BUILD]
//
// Where RELEASE is a dot-separated sequence of identifiers (e.g., "1.2.3" or "1.2.3.bcr.1"),
// PRERELEASE is an optional hyphen-prefixed identifier sequence, and BUILD is optional
// build metadata (ignored for comparison purposes).
//
// # Version Comparison
//
// Version comparison follows Bazel's reference implementation:
//
//  1. Empty versions compare as HIGHEST (used for NonRegistryOverride)
//  2. Release segments are compared lexicographically using identifier comparison
//  3. Prerelease versions are lower than release versions (same release prefix)
//  4. Prerelease segments are compared lexicographically using identifier comparison
//
// Identifier comparison (per segment):
//
//  1. Numeric identifiers sort before non-numeric identifiers
//  2. Numeric identifiers are compared as unsigned integers
//  3. Non-numeric identifiers are compared lexicographically as strings
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java
package label

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version represents a validated Bazel module version.
//
// The version format is: RELEASE[-PRERELEASE][+BUILD]
// Where RELEASE is a sequence of dot-separated identifiers.
//
// Empty versions are valid and compare as the HIGHEST version,
// used by Bazel to signal NonRegistryOverride modules.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java
type Version struct {
	raw        string
	major      int
	minor      int
	patch      int
	suffix     string // Optional suffix like ".1" or ".bcr.7"
	prerelease string
	build      string
}

// versionRegex matches Bazel module versions.
// Bazel allows more flexible versions than strict semver:
// - Single-part versions: 1
// - Two-part versions: 29.0
// - Three-part versions: 1.2.3
// - Four-part versions: 8.2.1.1 (common for buildifier, etc.)
// - BCR suffix versions: 1.3.1.bcr.7, 8.2.bcr.3 (BCR-specific patches)
// - Optional v-prefix: v1.2.3 (non-standard but found in BCR)
// - Prerelease with any format: 0.0.0-20241220-5e258e33
// - Build metadata: 1.2.3+build
//
// The regex captures: major, minor (optional), patch (optional), extra suffix (optional), prerelease, build
var versionRegex = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?((?:\.[a-zA-Z0-9]+)*)?(?:-([a-zA-Z0-9._-]+))?(?:\+([a-zA-Z0-9._-]+))?$`)

// commitSHARegex matches git commit SHAs used as versions (40 hex chars)
var commitSHARegex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// NewVersion creates a validated Version from a string.
func NewVersion(s string) (Version, error) {
	if s == "" {
		return Version{}, nil // Empty version is valid for some contexts
	}

	// Handle commit SHA versions (used by some BCR modules)
	if commitSHARegex.MatchString(s) {
		return Version{raw: s}, nil
	}

	matches := versionRegex.FindStringSubmatch(s)
	if matches == nil {
		return Version{}, fmt.Errorf("invalid version %q: must follow version format", s)
	}

	major, _ := strconv.Atoi(matches[1])
	var minor, patch int
	if matches[2] != "" {
		minor, _ = strconv.Atoi(matches[2])
	}
	if matches[3] != "" {
		patch, _ = strconv.Atoi(matches[3])
	}
	// matches[4] is the suffix like ".bcr.7" or ".1"

	return Version{
		raw:        s,
		major:      major,
		minor:      minor,
		patch:      patch,
		suffix:     matches[4],
		prerelease: matches[5],
		build:      matches[6],
	}, nil
}

// MustVersion creates a Version or panics. Use only for constants/tests.
func MustVersion(s string) Version {
	v, err := NewVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// String returns the version string.
func (v Version) String() string {
	return v.raw
}

// IsEmpty returns true if this is a zero-value Version.
func (v Version) IsEmpty() bool {
	return v.raw == ""
}

// Major returns the major version number.
func (v Version) Major() int {
	return v.major
}

// Minor returns the minor version number.
func (v Version) Minor() int {
	return v.minor
}

// Patch returns the patch version number.
func (v Version) Patch() int {
	return v.patch
}

// Suffix returns the optional version suffix (e.g., ".1" or ".bcr.7").
func (v Version) Suffix() string {
	return v.suffix
}

// HasSuffix returns true if this version has a suffix.
func (v Version) HasSuffix() bool {
	return v.suffix != ""
}

// Prerelease returns the pre-release identifier (e.g., "rc1", "alpha.1").
func (v Version) Prerelease() string {
	return v.prerelease
}

// Build returns the build metadata (e.g., "build.123").
func (v Version) Build() string {
	return v.build
}

// IsPrerelease returns true if this is a pre-release version.
func (v Version) IsPrerelease() bool {
	return v.prerelease != ""
}

// Compare compares two versions following Bazel's version comparison algorithm.
//
// Returns -1 if v < other, 0 if v == other, 1 if v > other.
//
// The comparison algorithm (from Version.java lines 182-186):
//
//	comparing(Version::isEmpty, falseFirst())           // Empty is HIGHEST
//	    .thenComparing(Version::release, lexicographical(Identifier.COMPARATOR))
//	    .thenComparing(Version::isPrerelease, trueFirst())  // Prerelease is lower
//	    .thenComparing(Version::prerelease, lexicographical(Identifier.COMPARATOR))
//
// Key behaviors:
//   - Empty version compares as HIGHEST (used for NonRegistryOverride)
//   - Prerelease versions are lower than release versions with same release prefix
//   - Build metadata is ignored for comparison
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L182-L186
func (v Version) Compare(other Version) int {
	// Step 1: Empty versions compare as HIGHEST (falseFirst means non-empty < empty)
	// From Version.java: "An empty version signifies that there is a NonRegistryOverride"
	// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L183
	if v.IsEmpty() && !other.IsEmpty() {
		return 1 // v (empty) > other (non-empty)
	}
	if !v.IsEmpty() && other.IsEmpty() {
		return -1 // v (non-empty) < other (empty)
	}
	if v.IsEmpty() && other.IsEmpty() {
		return 0 // Both empty
	}

	// Step 2: Compare release segments lexicographically
	// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L184
	if v.major != other.major {
		return intCompare(v.major, other.major)
	}
	if v.minor != other.minor {
		return intCompare(v.minor, other.minor)
	}
	if v.patch != other.patch {
		return intCompare(v.patch, other.patch)
	}
	// Compare suffix (e.g., ".1" vs ".2" or ".bcr.1" vs ".bcr.2")
	if v.suffix != other.suffix {
		return compareSuffix(v.suffix, other.suffix)
	}

	// Step 3: Prerelease versions have lower precedence (trueFirst means prerelease < release)
	// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L185
	if v.prerelease == "" && other.prerelease != "" {
		return 1 // v (release) > other (prerelease)
	}
	if v.prerelease != "" && other.prerelease == "" {
		return -1 // v (prerelease) < other (release)
	}

	// Step 4: Compare prerelease segments lexicographically
	// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L186
	if v.prerelease != other.prerelease {
		return comparePrerelease(v.prerelease, other.prerelease)
	}

	// Build metadata does not affect precedence (same as SemVer spec)
	return 0
}

// compareSuffix compares version suffixes like ".1", ".bcr.7" using
// lexicographical comparison of identifiers.
//
// This follows the same Identifier comparison logic as Bazel:
//   - Numeric identifiers sort before non-numeric (digitsOnly trueFirst)
//   - Numeric identifiers compared as unsigned integers
//   - Non-numeric identifiers compared lexicographically
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L72-L80
func compareSuffix(a, b string) int {
	// Empty suffix is less than non-empty suffix (fewer segments = lower)
	if a == "" && b != "" {
		return -1
	}
	if a != "" && b == "" {
		return 1
	}
	// Strip leading dot and split by dots
	aParts := strings.Split(strings.TrimPrefix(a, "."), ".")
	bParts := strings.Split(strings.TrimPrefix(b, "."), ".")

	return compareIdentifierLists(aParts, bParts)
}

// Less returns true if v < other.
func (v Version) Less(other Version) bool {
	return v.Compare(other) < 0
}

func intCompare(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// comparePrerelease compares prerelease identifiers using lexicographical
// comparison following Bazel's Identifier.COMPARATOR.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L72-L80
func comparePrerelease(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	return compareIdentifierLists(aParts, bParts)
}

// compareIdentifierLists performs lexicographical comparison of identifier lists
// following Bazel's Identifier.COMPARATOR (Version.java lines 72-80):
//
//	comparing(Identifier::isDigitsOnly, trueFirst())
//	    .thenComparing((a, b) -> Long.compareUnsigned(a.asNumber, b.asNumber))
//	    .thenComparing(Identifier::asString)
//
// This means:
//  1. Numeric-only identifiers sort before non-numeric identifiers
//  2. Numeric identifiers are compared as unsigned integers
//  3. Non-numeric identifiers are compared lexicographically as strings
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L72-L80
func compareIdentifierLists(aParts, bParts []string) int {
	for i := range min(len(aParts), len(bParts)) {
		if c := compareIdentifier(aParts[i], bParts[i]); c != 0 {
			return c
		}
	}
	// Shorter list comes first if all compared elements are equal
	return intCompare(len(aParts), len(bParts))
}

// compareIdentifier compares two version identifiers following Bazel's rules.
//
// From Identifier.COMPARATOR in Version.java:
//  1. isDigitsOnly with trueFirst: numeric identifiers sort before non-numeric
//  2. Compare as unsigned numbers if both numeric
//  3. Compare as strings otherwise
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java#L72-L80
func compareIdentifier(a, b string) int {
	aNum, aIsNum := tryParseInt(a)
	bNum, bIsNum := tryParseInt(b)

	// Step 1: Numeric identifiers sort before non-numeric (trueFirst)
	if aIsNum && !bIsNum {
		return -1 // Numeric < non-numeric
	}
	if !aIsNum && bIsNum {
		return 1 // Non-numeric > numeric
	}

	// Step 2: Both numeric - compare as unsigned integers
	if aIsNum && bIsNum {
		return intCompare(aNum, bNum)
	}

	// Step 3: Both non-numeric - compare as strings
	return strings.Compare(a, b)
}

func tryParseInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// Versions is a sortable slice of Version.
type Versions []Version

func (v Versions) Len() int           { return len(v) }
func (v Versions) Swap(i, j int)      { v[i], v[j] = v[j], v[i] }
func (v Versions) Less(i, j int) bool { return v[i].Less(v[j]) }
