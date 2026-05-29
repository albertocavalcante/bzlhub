// Package label provides strongly-typed, validated label components for Bazel modules.
//
// All types in this package are immutable and validate their values at construction time.
// Zero values are generally invalid - use the constructor functions (NewModule, NewVersion, etc.)
// to create valid instances.
//
// # Types
//
// The main types are:
//   - [Module]: A validated module name (e.g., "rules_go")
//   - [Version]: A semantic version with Bazel extensions (e.g., "0.50.1", "1.0.0-rc1")
//   - [ApparentRepo]: A repository name as it appears in labels
//   - [CanonicalRepo]: A fully-qualified repo name (module+version)
//   - [ApparentLabel]: A Bazel label (e.g., "@rules_go//go:def.bzl")
//   - [StarlarkIdentifier]: A valid Starlark identifier
//
// # Validation Patterns
//
// Module names must match: [a-z]([a-z0-9._-]*[a-z0-9])?
// Repository names must match: [a-zA-Z][a-zA-Z0-9._-]*
// Starlark identifiers must match: [a-zA-Z_][a-zA-Z0-9_]*
//
// # Reference
//
// Module name validation follows Bazel's ModuleFileGlobals.java (lines 68-77):
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
package label

import (
	"fmt"
	"regexp"
	"strings"
)

// Module represents a validated Bazel module name.
// Module names must match: [a-z]([a-z0-9._-]*[a-z0-9])?
type Module struct {
	name string
}

// moduleNameRegex matches Bazel's VALID_MODULE_NAME pattern from ModuleFileGlobals.java.
// Valid names must:
//  1. only contain lowercase letters (a-z), digits (0-9), dots (.), hyphens (-), and underscores (_)
//  2. begin with a lowercase letter
//  3. end with a lowercase letter or digit
var moduleNameRegex = regexp.MustCompile(`^[a-z]([a-z0-9._-]*[a-z0-9])?$`)

// NewModule creates a validated Module from a string.
func NewModule(name string) (Module, error) {
	if name == "" {
		return Module{}, fmt.Errorf("module name cannot be empty")
	}
	if !moduleNameRegex.MatchString(name) {
		return Module{}, fmt.Errorf("invalid module name %q: valid names must 1) only contain lowercase letters (a-z), digits (0-9), dots (.), hyphens (-), and underscores (_); 2) begin with a lowercase letter; 3) end with a lowercase letter or digit", name)
	}
	return Module{name: name}, nil
}

// MustModule creates a Module or panics. Use only for constants/tests.
func MustModule(name string) Module {
	m, err := NewModule(name)
	if err != nil {
		panic(err)
	}
	return m
}

// String returns the module name string.
func (m Module) String() string {
	return m.name
}

// IsEmpty returns true if this is a zero-value Module.
func (m Module) IsEmpty() bool {
	return m.name == ""
}

// ApparentRepo represents a repository name as it appears in the current context.
// This is the name used in labels like @repo_name//pkg:target.
type ApparentRepo struct {
	name string
}

var apparentRepoRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]*$`)

// NewApparentRepo creates a validated ApparentRepo.
func NewApparentRepo(name string) (ApparentRepo, error) {
	if name == "" {
		return ApparentRepo{}, nil // Empty is valid (means use module name)
	}
	if !apparentRepoRegex.MatchString(name) {
		return ApparentRepo{}, fmt.Errorf("invalid repo name %q", name)
	}
	return ApparentRepo{name: name}, nil
}

// String returns the repo name or empty string.
func (r ApparentRepo) String() string {
	return r.name
}

// IsEmpty returns true if no custom repo name is set.
func (r ApparentRepo) IsEmpty() bool {
	return r.name == ""
}

// CanonicalRepo represents a fully-qualified repository name.
// Format: module_name+version or module_name~ for root module.
type CanonicalRepo struct {
	module  Module
	version Version
}

// NewCanonicalRepo creates a CanonicalRepo.
func NewCanonicalRepo(module Module, version Version) CanonicalRepo {
	return CanonicalRepo{module: module, version: version}
}

// String returns the canonical repo string representation.
func (r CanonicalRepo) String() string {
	if r.version.IsEmpty() {
		return r.module.String() + "~"
	}
	return r.module.String() + "+" + r.version.String()
}

// Module returns the module component.
func (r CanonicalRepo) Module() Module {
	return r.module
}

// Version returns the version component.
func (r CanonicalRepo) Version() Version {
	return r.version
}

// StarlarkIdentifier represents a valid Starlark identifier.
type StarlarkIdentifier struct {
	name string
}

var starlarkIdentRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// NewStarlarkIdentifier creates a validated StarlarkIdentifier.
func NewStarlarkIdentifier(name string) (StarlarkIdentifier, error) {
	if name == "" {
		return StarlarkIdentifier{}, fmt.Errorf("identifier cannot be empty")
	}
	if !starlarkIdentRegex.MatchString(name) {
		return StarlarkIdentifier{}, fmt.Errorf("invalid Starlark identifier %q", name)
	}
	return StarlarkIdentifier{name: name}, nil
}

// String returns the identifier name.
func (i StarlarkIdentifier) String() string {
	return i.name
}

// ApparentLabel represents a label in the current context.
// Format: @repo//package:target or //package:target or :target
type ApparentLabel struct {
	repo   ApparentRepo
	pkg    string
	target string
	raw    string
}

// ParseApparentLabel parses a label string.
func ParseApparentLabel(s string) (ApparentLabel, error) {
	label := ApparentLabel{raw: s}

	// Handle @repo//pkg:target
	if strings.HasPrefix(s, "@") {
		repoName, rest, found := strings.Cut(s[1:], "//")
		if !found {
			return ApparentLabel{}, fmt.Errorf("invalid label %q: missing //", s)
		}
		repo, err := NewApparentRepo(repoName)
		if err != nil {
			return ApparentLabel{}, fmt.Errorf("invalid label %q: %w", s, err)
		}
		label.repo = repo
		s = "//" + rest
	}

	// Handle //pkg:target
	if strings.HasPrefix(s, "//") {
		s = s[2:]
		pkg, target, found := strings.Cut(s, ":")
		if !found {
			// //pkg means //pkg:pkg
			label.pkg = s
			parts := strings.Split(s, "/")
			label.target = parts[len(parts)-1]
		} else {
			label.pkg = pkg
			label.target = target
		}
	} else if strings.HasPrefix(s, ":") {
		// :target (relative)
		label.target = s[1:]
	} else {
		return ApparentLabel{}, fmt.Errorf("invalid label %q", s)
	}

	return label, nil
}

// String returns the original label string.
func (l ApparentLabel) String() string {
	return l.raw
}

// Repo returns the repository component.
func (l ApparentLabel) Repo() ApparentRepo {
	return l.repo
}

// Package returns the package path.
func (l ApparentLabel) Package() string {
	return l.pkg
}

// Target returns the target name.
func (l ApparentLabel) Target() string {
	return l.target
}
