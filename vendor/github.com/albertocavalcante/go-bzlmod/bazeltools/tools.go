// Package bazeltools provides Bazel version-specific MODULE.tools dependencies.
// These are the implicit dependencies that Bazel adds to every resolution.
//
// Source of truth: https://github.com/bazelbuild/bazel/blob/{tag}/src/MODULE.tools
// Each version's deps are extracted from the bazel_dep() declarations in that file.
//
// To verify a specific version:
//
//	curl -sL "https://raw.githubusercontent.com/bazelbuild/bazel/{version}/src/MODULE.tools"
//
// Reference: MODULE.tools is loaded by BazelModuleResolutionFunction.java as an
// implicit dependency of every Bazel workspace.
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/BazelModuleResolutionFunction.java
package bazeltools

import (
	"slices"
	"strings"

	"github.com/albertocavalcante/go-bzlmod/selection/version"
)

// ToolDep represents a dependency from Bazel's MODULE.tools file.
type ToolDep struct {
	Name    string
	Version string
}

// VersionConfig contains the MODULE.tools dependencies for a specific Bazel version.
type VersionConfig struct {
	// BazelVersion is the Bazel version (e.g., "7.0.0").
	BazelVersion string
	// Deps are the dependencies declared in MODULE.tools.
	Deps []ToolDep
}

// Source: https://github.com/bazelbuild/bazel/blob/6.6.0/src/MODULE.tools
var bazel660Deps = []ToolDep{
	{"rules_cc", "0.0.9"},
	{"rules_java", "5.5.1"},
	{"rules_license", "0.0.3"},
	{"rules_proto", "4.0.0"},
	{"rules_python", "0.4.0"},
	{"platforms", "0.0.7"},
	{"protobuf", "3.19.6"},
	{"zlib", "1.2.13"},
}

// Source: https://github.com/bazelbuild/bazel/blob/7.0.0/src/MODULE.tools
var bazel700Deps = []ToolDep{
	{"rules_cc", "0.0.9"},
	{"rules_java", "7.1.0"},
	{"rules_license", "0.0.3"},
	{"rules_proto", "4.0.0"},
	{"rules_python", "0.4.0"},
	{"platforms", "0.0.7"},
	{"protobuf", "3.19.6"},
	{"zlib", "1.3"},
	{"apple_support", "1.5.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/7.1.0/src/MODULE.tools
var bazel710Deps = []ToolDep{
	{"rules_cc", "0.0.9"},
	{"rules_java", "7.4.0"},
	{"rules_license", "0.0.3"},
	{"rules_proto", "4.0.0"},
	{"rules_python", "0.22.1"},
	{"buildozer", "6.4.0.2"},
	{"platforms", "0.0.7"},
	{"protobuf", "3.19.6"},
	{"zlib", "1.3"},
	{"apple_support", "1.5.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/7.2.0/src/MODULE.tools
// (also used for 7.2.1)
var bazel720Deps = []ToolDep{
	{"rules_cc", "0.0.9"},
	{"rules_java", "7.6.1"},
	{"rules_license", "0.0.3"},
	{"rules_proto", "4.0.0"},
	{"rules_python", "0.22.1"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.9"},
	{"protobuf", "3.19.6"},
	{"zlib", "1.3"},
	{"apple_support", "1.5.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/7.3.0/src/MODULE.tools
// (also used for 7.3.1, 7.3.2, 7.4.0, 7.4.1, 7.5.0, 7.6.0, 7.6.1)
var bazel730Deps = []ToolDep{
	{"rules_cc", "0.0.9"},
	{"rules_java", "7.6.5"},
	{"rules_license", "0.0.3"},
	{"rules_proto", "4.0.0"},
	{"rules_python", "0.22.1"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.9"},
	{"protobuf", "3.19.6"},
	{"zlib", "1.3.1.bcr.3"},
	{"apple_support", "1.5.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/7.6.2/src/MODULE.tools
// (also used for 7.7.0, 7.7.1)
var bazel762Deps = []ToolDep{
	{"rules_cc", "0.0.11"},
	{"rules_java", "7.6.5"},
	{"rules_license", "0.0.3"},
	{"rules_proto", "4.0.0"},
	{"rules_python", "0.22.1"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.9"},
	{"protobuf", "3.19.6"},
	{"zlib", "1.3.1.bcr.3"},
	{"apple_support", "1.23.1"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.0.0/src/MODULE.tools
// (also used for 8.0.1)
// Note: apple_support removed in 8.0 (was in 7.x); rules_shell added.
var bazel800Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.10"},
	{"zlib", "1.3.1.bcr.3"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.21.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.6.1"},
	{"rules_cc", "0.0.16"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.2.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.1.0/src/MODULE.tools
// (also used for 8.1.1)
var bazel810Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.10"},
	{"zlib", "1.3.1.bcr.3"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.21.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.6.1"},
	{"rules_cc", "0.0.17"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.2.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.2.0/src/MODULE.tools
// (also used for 8.2.1)
var bazel820Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.10"},
	{"zlib", "1.3.1.bcr.3"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.21.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.11.0"},
	{"rules_cc", "0.0.17"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.2.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.3.0/src/MODULE.tools
var bazel830Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.11"},
	{"zlib", "1.3.1.bcr.5"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.30.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.12.0"},
	{"rules_cc", "0.1.1"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.3.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.3.1/src/MODULE.tools
var bazel831Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.11"},
	{"zlib", "1.3.1.bcr.5"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.30.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.12.0"},
	{"rules_cc", "0.1.1"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.2.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.4.0/src/MODULE.tools
// (also used for 8.4.1)
var bazel840Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.11"},
	{"zlib", "1.3.1.bcr.5"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.30.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.14.0"},
	{"rules_cc", "0.1.1"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.2.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/8.4.2/src/MODULE.tools
// (also used for 8.5.0, 8.5.1, 8.6.0)
var bazel842Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "7.1.2"},
	{"platforms", "0.0.11"},
	{"zlib", "1.3.1.bcr.5"},
	{"rules_proto", "7.0.2"},
	{"bazel_features", "1.30.0"},
	{"protobuf", "29.0"},
	{"rules_java", "8.14.0"},
	{"rules_cc", "0.1.1"},
	{"rules_python", "0.40.0"},
	{"rules_shell", "0.2.0"},
}

// Source: https://github.com/bazelbuild/bazel/blob/9.0.0/src/MODULE.tools
// Note: rules_apple, rules_swift, abseil-cpp added in 9.0; apple_support re-added.
var bazel900Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "8.2.1"},
	{"platforms", "1.0.0"},
	{"zlib", "1.3.1.bcr.5"},
	{"bazel_features", "1.30.0"},
	{"protobuf", "33.4"},
	{"rules_java", "9.0.3"},
	{"rules_cc", "0.2.14"},
	{"rules_python", "1.7.0"},
	{"rules_shell", "0.6.1"},
	{"apple_support", "1.24.2"},
	{"rules_apple", "4.1.0"},
	{"rules_swift", "3.1.2"},
	{"abseil-cpp", "20250814.1"},
}

// Source: https://github.com/bazelbuild/bazel/blob/9.0.1/src/MODULE.tools
// (also used for 9.0.2)
var bazel901Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "8.5.1"},
	{"platforms", "1.0.0"},
	{"zlib", "1.3.1.bcr.5"},
	{"bazel_features", "1.42.1"},
	{"protobuf", "33.4"},
	{"rules_java", "9.0.3"},
	{"rules_cc", "0.2.17"},
	{"rules_python", "1.7.0"},
	{"rules_shell", "0.6.1"},
	{"apple_support", "1.24.2"},
	{"rules_apple", "4.1.0"},
	{"rules_swift", "3.1.2"},
	{"abseil-cpp", "20250814.1"},
}

// Source: https://github.com/bazelbuild/bazel/blob/9.1.0/src/MODULE.tools
var bazel910Deps = []ToolDep{
	{"rules_license", "1.0.0"},
	{"buildozer", "8.5.1"},
	{"platforms", "1.0.0"},
	{"zlib", "1.3.1.bcr.5"},
	{"bazel_features", "1.42.1"},
	{"protobuf", "33.4"},
	{"rules_java", "9.1.0"},
	{"rules_cc", "0.2.17"},
	{"rules_python", "1.7.0"},
	{"rules_shell", "0.6.1"},
	{"apple_support", "1.24.2"},
	{"rules_apple", "4.1.0"},
	{"rules_swift", "3.1.2"},
	{"abseil-cpp", "20250814.1"},
}

var stableBazelVersions = []string{
	"6.6.0",
	"7.0.0",
	"7.0.1",
	"7.1.0",
	"7.1.1",
	"7.1.2",
	"7.2.0",
	"7.2.1",
	"7.3.0",
	"7.3.1",
	"7.3.2",
	"7.4.0",
	"7.4.1",
	"7.5.0",
	"7.6.0",
	"7.6.1",
	"7.6.2",
	"7.7.0",
	"7.7.1",
	"8.0.0",
	"8.0.1",
	"8.1.0",
	"8.1.1",
	"8.2.0",
	"8.2.1",
	"8.3.0",
	"8.3.1",
	"8.4.0",
	"8.4.1",
	"8.4.2",
	"8.5.0",
	"8.5.1",
	"8.6.0",
	"9.0.0",
	"9.0.1",
	"9.0.2",
	"9.1.0",
}

// bazelConfigs maps Bazel versions to their MODULE.tools dependencies.
//
// Note on Bazel 6: Version 6.6.0 is the final release in the Bazel 6.x series.
// Bazel 6 reached end-of-life with the release of Bazel 9.
// See: https://blog.bazel.build/2026/01/20/bazel-9.html#bazel-6-deprecation
var bazelConfigs = map[string]VersionConfig{
	"6.6.0": {BazelVersion: "6.6.0", Deps: bazel660Deps},
	"7.0.0": {BazelVersion: "7.0.0", Deps: bazel700Deps},
	"7.0.1": {BazelVersion: "7.0.1", Deps: bazel700Deps},
	"7.1.0": {BazelVersion: "7.1.0", Deps: bazel710Deps},
	"7.1.1": {BazelVersion: "7.1.1", Deps: bazel710Deps},
	"7.1.2": {BazelVersion: "7.1.2", Deps: bazel710Deps},
	"7.2.0": {BazelVersion: "7.2.0", Deps: bazel720Deps},
	"7.2.1": {BazelVersion: "7.2.1", Deps: bazel720Deps},
	"7.3.0": {BazelVersion: "7.3.0", Deps: bazel730Deps},
	"7.3.1": {BazelVersion: "7.3.1", Deps: bazel730Deps},
	"7.3.2": {BazelVersion: "7.3.2", Deps: bazel730Deps},
	"7.4.0": {BazelVersion: "7.4.0", Deps: bazel730Deps},
	"7.4.1": {BazelVersion: "7.4.1", Deps: bazel730Deps},
	"7.5.0": {BazelVersion: "7.5.0", Deps: bazel730Deps},
	"7.6.0": {BazelVersion: "7.6.0", Deps: bazel730Deps},
	"7.6.1": {BazelVersion: "7.6.1", Deps: bazel730Deps},
	"7.6.2": {BazelVersion: "7.6.2", Deps: bazel762Deps},
	"7.7.0": {BazelVersion: "7.7.0", Deps: bazel762Deps},
	"7.7.1": {BazelVersion: "7.7.1", Deps: bazel762Deps},
	"8.0.0": {BazelVersion: "8.0.0", Deps: bazel800Deps},
	"8.0.1": {BazelVersion: "8.0.1", Deps: bazel800Deps},
	"8.1.0": {BazelVersion: "8.1.0", Deps: bazel810Deps},
	"8.1.1": {BazelVersion: "8.1.1", Deps: bazel810Deps},
	"8.2.0": {BazelVersion: "8.2.0", Deps: bazel820Deps},
	"8.2.1": {BazelVersion: "8.2.1", Deps: bazel820Deps},
	"8.3.0": {BazelVersion: "8.3.0", Deps: bazel830Deps},
	"8.3.1": {BazelVersion: "8.3.1", Deps: bazel831Deps},
	"8.4.0": {BazelVersion: "8.4.0", Deps: bazel840Deps},
	"8.4.1": {BazelVersion: "8.4.1", Deps: bazel840Deps},
	"8.4.2": {BazelVersion: "8.4.2", Deps: bazel842Deps},
	"8.5.0": {BazelVersion: "8.5.0", Deps: bazel842Deps},
	"8.5.1": {BazelVersion: "8.5.1", Deps: bazel842Deps},
	"8.6.0": {BazelVersion: "8.6.0", Deps: bazel842Deps},
	"9.0.0": {BazelVersion: "9.0.0", Deps: bazel900Deps},
	"9.0.1": {BazelVersion: "9.0.1", Deps: bazel901Deps},
	"9.0.2": {BazelVersion: "9.0.2", Deps: bazel901Deps},
	"9.1.0": {BazelVersion: "9.1.0", Deps: bazel910Deps},
}

// GetConfig returns the MODULE.tools configuration for a Bazel version.
// Returns nil if the version is not supported.
// Use ClosestVersion to find the closest matching version.
func GetConfig(version string) *VersionConfig {
	if cfg, ok := bazelConfigs[version]; ok {
		return &cfg
	}
	return nil
}

// GetDeps returns the MODULE.tools dependencies for a Bazel version.
// Returns nil if the version is not supported.
func GetDeps(version string) []ToolDep {
	if cfg := GetConfig(version); cfg != nil {
		return cfg.Deps
	}
	return nil
}

// LookupDeps returns the MODULE.tools dependencies for a Bazel version using the
// same closest-version fallback logic used by the resolver.
//
// For example, "7.0.1" resolves to the "7.0.0" data when that is the closest
// built-in match. Returns nil if no built-in configuration matches.
func LookupDeps(version string) []ToolDep {
	closestVersion := ClosestVersion(version)
	if closestVersion == "" {
		return nil
	}
	return GetDeps(closestVersion)
}

// SetToolDep replaces a dependency with the same name while preserving its
// position, or appends the dependency if the name is not present.
func SetToolDep(deps []ToolDep, dep ToolDep) []ToolDep {
	for i := range deps {
		if deps[i].Name == dep.Name {
			deps[i] = dep
			return deps
		}
	}
	return append(deps, dep)
}

// RemoveToolDep removes the dependency with the given name while preserving the
// relative order of the remaining dependencies.
func RemoveToolDep(deps []ToolDep, name string) []ToolDep {
	for i := range deps {
		if deps[i].Name == name {
			return append(deps[:i], deps[i+1:]...)
		}
	}
	return deps
}

// SupportedVersions returns all supported Bazel versions.
func SupportedVersions() []string {
	return slices.Clone(stableBazelVersions)
}

const (
	// versionMinLenMajorMinor is the minimum length for major.minor pattern (e.g., "7.0.x").
	versionMinLenMajorMinor = 5
)

// ClosestVersion finds the closest supported version for a given Bazel version.
// For example, "7.0.1" would return "7.0.0", "7.1.2" would return "7.1.0".
// Returns empty string if no suitable version is found.
func ClosestVersion(version string) string {
	// Exact match
	if _, ok := bazelConfigs[version]; ok {
		return version
	}

	if prefix, ok := releasePrefix(version, 2); ok {
		if closest := highestSupportedAtOrBelow(version, prefix+"."); closest != "" {
			return closest
		}
	}

	// Fall back to the highest known release in the same major line.
	// For example, "9.2.0" prefers the highest known "9.x" snapshot.
	if prefix, ok := releasePrefix(version, 1); ok {
		if closest := highestSupportedAtOrBelow(version, prefix+"."); closest != "" {
			return closest
		}
	}

	return ""
}

func releasePrefix(v string, segments int) (string, bool) {
	if segments <= 0 {
		return "", false
	}

	release := v
	if idx := strings.IndexAny(release, "-+"); idx >= 0 {
		release = release[:idx]
	}

	parts := strings.Split(release, ".")
	if len(parts) < segments {
		return "", false
	}
	return strings.Join(parts[:segments], "."), true
}

func highestSupportedAtOrBelow(requested, prefix string) string {
	best := ""
	for supported := range bazelConfigs {
		if !strings.HasPrefix(supported, prefix) {
			continue
		}
		if version.Compare(supported, requested) > 0 {
			continue
		}
		if best == "" || version.Compare(supported, best) > 0 {
			best = supported
		}
	}
	return best
}
