// Package version implements Bazel's module version parsing and comparison.
//
// This is a Go port of Bazel's Version.java.
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Version.java
//
// Version format: RELEASE[-PRERELEASE][+BUILD]
// - RELEASE: dot-separated identifiers (alphanumeric, no hyphens)
// - PRERELEASE: dot-separated identifiers (alphanumeric and hyphens allowed)
// - BUILD: ignored for comparison purposes
//
// Reference: Version.java lines 33-63
package version

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Version.java line 70-72: Pattern for parsing versions
// We don't capture BUILD part as it's ignored.
var versionPattern = regexp.MustCompile(
	`^(?P<release>[a-zA-Z0-9.]+)(?:-(?P<prerelease>[a-zA-Z0-9.-]+))?(?:\+[a-zA-Z0-9.-]+)?$`,
)

// Identifier represents a dot-separated segment in the version string.
//
// Reference: Version.java lines 92-120
// "An identifier is compared differently based on whether it's digits-only or not."
type Identifier struct {
	IsDigitsOnly bool
	AsNumber     uint64 // Only valid if IsDigitsOnly
	AsString     string
}

// ParseIdentifier creates an Identifier from a string segment.
//
// Reference: Version.java lines 96-109
func ParseIdentifier(s string) Identifier {
	if s == "" {
		return Identifier{AsString: s}
	}

	// Check if all digits
	allDigits := true
	for _, r := range s {
		if !unicode.IsDigit(r) {
			allDigits = false
			break
		}
	}

	if allDigits {
		num, err := strconv.ParseUint(s, 10, 64)
		if err == nil {
			return Identifier{IsDigitsOnly: true, AsNumber: num, AsString: s}
		}
	}

	return Identifier{IsDigitsOnly: false, AsString: s}
}

// CompareIdentifiers compares two identifiers per Bazel's rules.
//
// Reference: Version.java lines 111-119
// - Digits-only identifiers sort BEFORE alphanumeric (trueFirst for isDigitsOnly)
// - Digits-only identifiers compare numerically
// - Alphanumeric identifiers compare lexicographically
func CompareIdentifiers(a, b Identifier) int {
	// Digits-only sorts first (trueFirst in Java)
	if a.IsDigitsOnly != b.IsDigitsOnly {
		if a.IsDigitsOnly {
			return -1
		}
		return 1
	}

	// Both digits-only: compare numerically
	if a.IsDigitsOnly {
		if a.AsNumber < b.AsNumber {
			return -1
		}
		if a.AsNumber > b.AsNumber {
			return 1
		}
		return 0
	}

	// Both alphanumeric: compare lexicographically
	return strings.Compare(a.AsString, b.AsString)
}

// ParsedVersion represents a parsed version.
type ParsedVersion struct {
	Release    []Identifier
	Prerelease []Identifier
	Normalized string
	IsEmpty    bool
}

// Parse parses a version string into its components.
//
// Reference: Version.java lines 144-180
func Parse(s string) (ParsedVersion, error) {
	// Empty string is special: compares higher than everything.
	// Reference: Version.java lines 77-81
	if s == "" {
		return ParsedVersion{IsEmpty: true, Normalized: ""}, nil
	}

	match := versionPattern.FindStringSubmatch(s)
	if match == nil {
		return ParsedVersion{}, &ParseError{Version: s, Message: "does not match version pattern"}
	}

	releaseStr := match[1]
	prereleaseStr := match[2]

	var release []Identifier
	for _, part := range strings.Split(releaseStr, ".") {
		release = append(release, ParseIdentifier(part))
	}

	var prerelease []Identifier
	if prereleaseStr != "" {
		for _, part := range strings.Split(prereleaseStr, ".") {
			prerelease = append(prerelease, ParseIdentifier(part))
		}
	}

	normalized := releaseStr
	if prereleaseStr != "" {
		normalized = releaseStr + "-" + prereleaseStr
	}

	return ParsedVersion{
		Release:    release,
		Prerelease: prerelease,
		Normalized: normalized,
	}, nil
}

// ParseError represents a version parsing error.
type ParseError struct {
	Version string
	Message string
}

func (e *ParseError) Error() string {
	return "bad version " + e.Version + ": " + e.Message
}

// Compare compares two version strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
//
// Reference: Version.java lines 182-191, COMPARATOR
// Order:
// 1. Empty versions sort LAST (falseFirst for isEmpty)
// 2. Compare release segments lexicographically
// 3. Prerelease versions sort BEFORE release versions (trueFirst for isPrerelease)
// 4. Compare prerelease segments lexicographically
func Compare(a, b string) int {
	va, errA := Parse(a)
	vb, errB := Parse(b)

	// Handle parse errors by treating as lexicographic comparison
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}

	// Empty versions sort LAST (higher than everything)
	// Reference: Version.java line 183
	if va.IsEmpty != vb.IsEmpty {
		if va.IsEmpty {
			return 1
		}
		return -1
	}
	if va.IsEmpty && vb.IsEmpty {
		return 0
	}

	// Compare release segments
	// Reference: Version.java line 184
	result := compareIdentifierLists(va.Release, vb.Release)
	if result != 0 {
		return result
	}

	// Prerelease versions sort BEFORE release versions
	// Reference: Version.java lines 185-186
	aIsPre := len(va.Prerelease) > 0
	bIsPre := len(vb.Prerelease) > 0
	if aIsPre != bIsPre {
		if aIsPre {
			return -1
		}
		return 1
	}

	// Compare prerelease segments
	return compareIdentifierLists(va.Prerelease, vb.Prerelease)
}

// compareIdentifierLists compares two lists of identifiers lexicographically.
//
// Reference: Version.java line 184 uses lexicographical(Identifier.COMPARATOR)
func compareIdentifierLists(a, b []Identifier) int {
	minLen := min(len(a), len(b))

	for i := range minLen {
		c := CompareIdentifiers(a[i], b[i])
		if c != 0 {
			return c
		}
	}

	// Shorter list is less (lexicographic)
	return cmp.Compare(len(a), len(b))
}

// Sort sorts a slice of version strings in ascending order.
func Sort(versions []string) {
	slices.SortFunc(versions, Compare)
}

// Max returns the higher of two versions.
func Max(a, b string) string {
	if Compare(a, b) >= 0 {
		return a
	}
	return b
}
