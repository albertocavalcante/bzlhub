// Package compat provides Bazel version compatibility checking.
// This is an internal package used by the resolver to validate
// that bzlmod fields are supported in the target Bazel version.
package compat

import (
	"fmt"

	"github.com/albertocavalcante/go-bzlmod/selection/version"
)

// FieldLocation indicates where a bzlmod field is used.
type FieldLocation string

const (
	// LocationModule indicates a field used in MODULE.bazel.
	LocationModule FieldLocation = "MODULE.bazel"
	// LocationSource indicates a field used in source.json.
	LocationSource FieldLocation = "source.json"
	// LocationRegistry indicates a field used in bazel_registry.json.
	LocationRegistry FieldLocation = "bazel_registry.json"
)

// FieldRequirement defines version requirements for a bzlmod field.
type FieldRequirement struct {
	// Name is the field name (e.g., "mirror_urls", "max_compatibility_level").
	Name string
	// MinVersion is the minimum Bazel version required (e.g., "7.7.0").
	MinVersion string
	// Location indicates where this field is used.
	Location FieldLocation
	// Description explains what the field does.
	Description string
}

// fieldRegistry contains all known field requirements.
// Each entry documents when a field was added to Bazel.
//
// Reference sources:
// - mirror_urls: https://github.com/bazelbuild/bazel/issues/17829 (added in 7.7.0)
// - max_compatibility_level: https://bazel.build/versions/7.0.0/external/module#bazel_dep (added in 7.0.0)
// - include: https://bazel.build/versions/7.2.0/external/module#include (added in 7.2.0)
// - use_repo_rule: https://bazel.build/versions/7.0.0/external/module#use_repo_rule (added in 7.0.0)
// - override_repo/inject_repo: https://bazel.build/versions/8.0.0/external/module (added in 8.0.0)
var fieldRegistry = []FieldRequirement{
	// source.json fields
	{
		Name:        "mirror_urls",
		MinVersion:  "7.7.0",
		Location:    LocationSource,
		Description: "Backup URLs for source archive",
	},

	// MODULE.bazel fields
	{
		Name:        "max_compatibility_level",
		MinVersion:  "7.0.0",
		Location:    LocationModule,
		Description: "Maximum allowed compatibility level for dependency",
	},
	{
		Name:        "include",
		MinVersion:  "7.2.0",
		Location:    LocationModule,
		Description: "Include another MODULE.bazel segment",
	},
	{
		Name:        "use_repo_rule",
		MinVersion:  "7.0.0",
		Location:    LocationModule,
		Description: "Direct repository rule invocation",
	},

	// Extension fields (Bazel 8+)
	{
		Name:        "override_repo",
		MinVersion:  "8.0.0",
		Location:    LocationModule,
		Description: "Override repository from extension",
	},
	{
		Name:        "inject_repo",
		MinVersion:  "8.0.0",
		Location:    LocationModule,
		Description: "Inject repository into extension",
	},
}

// FieldWarning represents a field used with an incompatible Bazel version.
type FieldWarning struct {
	// Field is the name of the unsupported field.
	Field string
	// MinVersion is the minimum Bazel version required for this field.
	MinVersion string
	// UsedVersion is the Bazel version being targeted.
	UsedVersion string
	// Location indicates where the field was used.
	Location FieldLocation
	// Description explains what the field does.
	Description string
}

// String returns a human-readable warning message.
func (w *FieldWarning) String() string {
	return fmt.Sprintf("%s requires Bazel %s+, but target is %s",
		w.Field, w.MinVersion, w.UsedVersion)
}

// IsSupported checks if a field is supported in the given Bazel version.
// Returns true if:
// - bazelVersion is empty (no constraint)
// - fieldName is unknown (assume supported for forward compatibility)
// - bazelVersion >= field's minimum version
func IsSupported(bazelVersion, fieldName string) bool {
	if bazelVersion == "" {
		return true // No version constraint, assume supported
	}
	for _, req := range fieldRegistry {
		if req.Name == fieldName {
			return version.Compare(bazelVersion, req.MinVersion) >= 0
		}
	}
	return true // Unknown field, assume supported for forward compatibility
}

// GetRequirement returns the requirement for a field, or nil if not found.
func GetRequirement(fieldName string) *FieldRequirement {
	for i := range fieldRegistry {
		if fieldRegistry[i].Name == fieldName {
			return &fieldRegistry[i]
		}
	}
	return nil
}

// CheckField validates field usage and returns a warning if unsupported.
// Returns nil if:
// - bazelVersion is empty
// - fieldName is unknown
// - field is supported in bazelVersion
func CheckField(bazelVersion, fieldName string) *FieldWarning {
	if bazelVersion == "" {
		return nil
	}
	req := GetRequirement(fieldName)
	if req == nil {
		return nil
	}
	if version.Compare(bazelVersion, req.MinVersion) < 0 {
		return &FieldWarning{
			Field:       fieldName,
			MinVersion:  req.MinVersion,
			UsedVersion: bazelVersion,
			Location:    req.Location,
			Description: req.Description,
		}
	}
	return nil
}

// GetAllRequirements returns all known field requirements.
// This can be used for documentation or validation purposes.
func GetAllRequirements() []FieldRequirement {
	result := make([]FieldRequirement, len(fieldRegistry))
	copy(result, fieldRegistry)
	return result
}

// GetRequirementsForLocation returns all field requirements for a given location.
func GetRequirementsForLocation(location FieldLocation) []FieldRequirement {
	var result []FieldRequirement
	for _, req := range fieldRegistry {
		if req.Location == location {
			result = append(result, req)
		}
	}
	return result
}
