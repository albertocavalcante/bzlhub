package lockfile

import "fmt"

// Version mapping between Bazel releases and lockfile format versions.
//
// IMPORTANT: Bazel uses exact version matching for lockfile compatibility.
// A lockfile is only valid if its version exactly matches the expected version
// for that Bazel release. There is no forward or backward compatibility.
//
// Version mapping (researched from Bazel source code):
//
//	| Bazel Version | Lockfile Version | Notes                              |
//	|---------------|------------------|------------------------------------|
//	| 6.2.x         | 1                | Experimental lockfile support      |
//	| 6.3.x         | 1                | Initial stable lockfile support    |
//	| 6.4.x-6.6.x   | 3                |                                    |
//	| 7.0.x         | 3                | LTS release                        |
//	| 7.1.x         | 6                |                                    |
//	| 7.2.x-7.4.x   | 11               | Incremental lockfile format        |
//	| 7.5.x-7.7.x   | 13               | Uses odd numbers on 7.x branch     |
//	| 8.0.x         | 16               | LTS release                        |
//	| 8.1.x-8.4.x   | 18               |                                    |
//	| 8.5.x         | 24               |                                    |
//	| 9.0.x         | 26               | Uses even numbers on master        |
//	| master (10.x) | 26               | Development branch                 |
//
// Gap versions (2, 4-5, 7-10, 12, 14-15, 17, 19-23, 25) were used during
// development but never appeared in any released Bazel version.
//
// Note on version numbering:
// - On the 7.x branch, version increments should be done 2 at a time (keeping odd)
// - On master, version increments should be done 2 at a time (keeping even)
// - This allows cherry-picking between branches without version collisions
//
// Source: github.com/bazelbuild/bazel BazelLockFileValue.java

// BazelVersion represents a Bazel release version.
type BazelVersion struct {
	Major int
	Minor int
	Patch int
}

// String returns the version as "major.minor.patch".
func (v BazelVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// VersionMapping maps lockfile versions to their corresponding Bazel releases.
// This is an exhaustive list of all released Bazel versions with lockfile support.
var VersionMapping = map[int][]BazelVersion{
	// Bazel 6.2.x-6.3.x: Initial lockfile support (experimental in 6.2)
	1: {{6, 2, 0}, {6, 2, 1}, {6, 3, 0}, {6, 3, 1}, {6, 3, 2}},
	// Bazel 6.4.x-7.0.x
	3: {{6, 4, 0}, {6, 5, 0}, {6, 6, 0}, {7, 0, 0}, {7, 0, 1}, {7, 0, 2}},
	// Bazel 7.1.x
	6: {{7, 1, 0}, {7, 1, 1}, {7, 1, 2}},
	// Bazel 7.2.x-7.4.x: Incremental lockfile format
	11: {{7, 2, 0}, {7, 2, 1}, {7, 3, 0}, {7, 3, 1}, {7, 3, 2}, {7, 4, 0}, {7, 4, 1}},
	// Bazel 7.5.x-7.7.x
	13: {{7, 5, 0}, {7, 6, 0}, {7, 6, 1}, {7, 6, 2}, {7, 7, 0}, {7, 7, 1}},
	// Bazel 8.0.x (LTS)
	16: {{8, 0, 0}, {8, 0, 1}},
	// Bazel 8.1.x-8.4.x
	18: {{8, 1, 0}, {8, 1, 1}, {8, 2, 0}, {8, 2, 1}, {8, 3, 0}, {8, 3, 1}, {8, 4, 0}, {8, 4, 1}, {8, 4, 2}},
	// Bazel 8.5.x
	24: {{8, 5, 0}, {8, 5, 1}},
	// Bazel 9.0.x
	26: {{9, 0, 0}},
}

// LockfileVersionForBazel returns the expected lockfile version for a Bazel release.
// Returns -1 if the Bazel version is not in the known mapping.
func LockfileVersionForBazel(major, minor, patch int) int {
	// Walk through known mappings
	for lfVersion, bazelVersions := range VersionMapping {
		for _, bv := range bazelVersions {
			if bv.Major == major && bv.Minor == minor && bv.Patch == patch {
				return lfVersion
			}
		}
	}
	return -1
}

// BazelVersionsForLockfile returns the Bazel versions that use a given lockfile version.
// Returns nil if the lockfile version is not in the known mapping.
func BazelVersionsForLockfile(version int) []BazelVersion {
	return VersionMapping[version]
}

// KnownLockfileVersions returns all known lockfile versions in ascending order.
func KnownLockfileVersions() []int {
	return []int{1, 3, 6, 11, 13, 16, 18, 24, 26}
}

// VersionInfo provides detailed information about a lockfile version.
type VersionInfo struct {
	// LockfileVersion is the lockfile format version number.
	LockfileVersion int

	// BazelVersions are the Bazel releases using this lockfile version.
	BazelVersions []BazelVersion

	// IsLTS indicates if any of the Bazel versions is an LTS release.
	IsLTS bool

	// Features describes notable features introduced in this version.
	Features []string
}

// GetVersionInfo returns detailed information about a lockfile version.
//
//nolint:mnd // Version numbers are the actual data, not magic numbers
func GetVersionInfo(version int) *VersionInfo {
	bazelVersions := VersionMapping[version]
	if bazelVersions == nil {
		return nil
	}

	info := &VersionInfo{
		LockfileVersion: version,
		BazelVersions:   bazelVersions,
	}

	// Check for LTS releases (x.0.0 where x is even starting from 6)
	for _, bv := range bazelVersions {
		if bv.Minor == 0 && bv.Patch == 0 && bv.Major >= 6 {
			info.IsLTS = true
			break
		}
	}

	// Add known features for each version
	switch version {
	case 1:
		info.Features = []string{"Initial lockfile support", "Basic module graph locking"}
	case 3:
		info.Features = []string{"Improved format stability"}
	case 6:
		info.Features = []string{"Registry file hashes"}
	case 11:
		info.Features = []string{"Incremental lockfile updates", "Git merge driver support"}
	case 13:
		info.Features = []string{"Enhanced extension tracking"}
	case 16:
		info.Features = []string{"Repo mapping entries", "Improved extension isolation"}
	case 18:
		info.Features = []string{"UTF-8 encoding fixes", "Facts persistence"}
	case 24:
		info.Features = []string{"Enhanced module extension caching"}
	case 26:
		info.Features = []string{"Hidden lockfile support", "Recorded inputs preservation"}
	}

	return info
}

// Lockfile version constants for well-known Bazel releases.
const (
	// VersionBazel8LTS is the lockfile version for Bazel 8.0.0 (LTS).
	VersionBazel8LTS = 16

	// VersionBazel9 is the lockfile version for Bazel 9.0.0.
	VersionBazel9 = 26
)

// LatestLTSVersion returns the lockfile version for the latest LTS Bazel release.
func LatestLTSVersion() int {
	return VersionBazel8LTS
}

// LatestVersion returns the most recent known lockfile version.
func LatestVersion() int {
	return VersionBazel9
}

// IsExactMatchRequired returns true because Bazel requires exact version matching.
// Lockfiles are not forward or backward compatible between versions.
func IsExactMatchRequired() bool {
	return true
}
