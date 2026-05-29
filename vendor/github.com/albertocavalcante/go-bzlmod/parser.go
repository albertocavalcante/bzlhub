// Package gobzlmod provides parsing and resolution of Bazel module dependencies.
//
// This package implements parsing of MODULE.bazel files following Bazel's bzlmod
// specification. The parsing logic is based on Bazel's ModuleFileGlobals, which
// defines the built-in functions available in MODULE.bazel files.
//
// Reference: ModuleFileGlobals.java
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
package gobzlmod

import (
	"fmt"
	"os"
	"regexp"

	"github.com/albertocavalcante/go-bzlmod/internal/buildutil"
	"github.com/albertocavalcante/go-bzlmod/third_party/buildtools/build"
)

// bazelCompatibilityPattern validates bazel_compatibility entries.
// Format: (>=|<=|>|<|-)X.Y.Z where X, Y, Z are version numbers.
//
// Reference: ModuleFileGlobals.java lines 65-66
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
var bazelCompatibilityPattern = regexp.MustCompile(`^(>=|<=|>|<|-)(\d+\.){2}\d+$`)

// ParseModuleFile reads and parses a MODULE.bazel file from disk.
// This is a convenience wrapper around ParseModuleContent.
//
// For more advanced parsing with error diagnostics and position information,
// use the ast package instead.
func ParseModuleFile(filename string) (*ModuleInfo, error) {
	data, err := os.ReadFile(filename) // #nosec G304 -- intentional file read by caller-provided path
	if err != nil {
		return nil, fmt.Errorf("read module file: %w", err)
	}
	return parseModule(filename, data)
}

// ParseModuleContent parses the content of a MODULE.bazel file.
//
// The parser validates that:
//   - module() is called at most once (ModuleFileGlobals.java lines 166-168)
//   - module() is called before any other directives (ModuleFileGlobals.java lines 169-171)
//   - bazel_compatibility entries match the required format (ModuleFileGlobals.java lines 65-66)
func ParseModuleContent(content string) (*ModuleInfo, error) {
	return parseModule("MODULE.bazel", []byte(content))
}

func parseModule(filename string, content []byte) (*ModuleInfo, error) {
	f, err := build.ParseModule(filename, content)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	info, err := extractModuleInfo(f)
	if err != nil {
		return nil, err
	}
	return info, nil
}

// extractModuleInfo extracts module information from parsed BUILD file.
//
// This function processes the AST to extract module metadata and dependencies,
// implementing validation rules from Bazel's ModuleFileGlobals.
//
// Reference: ModuleFileGlobals.java
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
func extractModuleInfo(f *build.File) (*ModuleInfo, error) {
	info := &ModuleInfo{
		Dependencies:      []Dependency{},
		NodepDependencies: []Dependency{},
		Overrides:         []Override{},
	}

	foundModule := false
	// Track if we've seen any directive before module()
	// Reference: ModuleFileGlobals.java lines 169-171
	seenOtherDirective := false

	for _, stmt := range f.Stmt {
		call, ok := stmt.(*build.CallExpr)
		if !ok {
			continue
		}

		funcName := buildutil.FuncName(call)

		switch funcName {
		// Reference: ModuleFileGlobals.module() - lines 152-217
		// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
		case "module":
			// Validation: module() can only be called once
			// Reference: ModuleFileGlobals.java lines 166-168
			if foundModule {
				return nil, fmt.Errorf("the module() directive can only be called once")
			}

			// Validation: module() must be called before any other functions
			// Reference: ModuleFileGlobals.java lines 169-171
			if seenOtherDirective {
				return nil, fmt.Errorf("if module() is called, it must be called before any other functions")
			}

			foundModule = true
			info.Name = buildutil.String(call, "name")
			info.Version = buildutil.String(call, "version")
			info.CompatibilityLevel = buildutil.Int(call, "compatibility_level")

			// Parse bazel_compatibility list
			// Reference: ModuleFileGlobals.java lines 65-66, 180-182
			bazelCompat := buildutil.StringList(call, "bazel_compatibility")
			if len(bazelCompat) > 0 {
				// Validate each bazel_compatibility entry
				for _, entry := range bazelCompat {
					if !bazelCompatibilityPattern.MatchString(entry) {
						return nil, fmt.Errorf("invalid bazel_compatibility value %q: must match pattern (>=|<=|>|<|-)X.Y.Z", entry)
					}
				}
				info.BazelCompatibility = bazelCompat
			}

		// Reference: ModuleFileGlobals.bazelDep() - lines 219-281
		// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
		case "bazel_dep":
			seenOtherDirective = true
			dep := Dependency{
				Name:                  buildutil.String(call, "name"),
				Version:               buildutil.String(call, "version"),
				MaxCompatibilityLevel: buildutil.Int(call, "max_compatibility_level"),
				RepoName:              buildutil.String(call, "repo_name"),
				DevDependency:         buildutil.Bool(call, "dev_dependency"),
			}
			if dep.Name == "" {
				return nil, fmt.Errorf("bazel_dep requires name")
			}
			if buildutil.IsNone(call, "repo_name") {
				dep.IsNodepDep = true
				info.NodepDependencies = append(info.NodepDependencies, dep)
				continue
			}
			info.Dependencies = append(info.Dependencies, dep)

		// Reference: ModuleFileGlobals.singleVersionOverride() - lines 476-534
		// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
		case "single_version_override":
			seenOtherDirective = true
			override := Override{
				Type:       "single_version",
				ModuleName: buildutil.String(call, "module_name"),
				Version:    buildutil.String(call, "version"),
				Registry:   buildutil.String(call, "registry"),
			}
			if override.ModuleName != "" {
				info.Overrides = append(info.Overrides, override)
			}

			// Bazel parity: parse multiple_version_override directives.
			case "multiple_version_override":
			seenOtherDirective = true
			override := Override{
				Type:       "multiple_version",
				ModuleName: buildutil.String(call, "module_name"),
				Versions:   buildutil.StringList(call, "versions"),
				Registry:   buildutil.String(call, "registry"),
			}
			if override.ModuleName != "" {
				info.Overrides = append(info.Overrides, override)
			}

		// Reference: ModuleFileGlobals.gitOverride() - lines 608-672
		// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
		case "git_override":
			seenOtherDirective = true
			override := Override{
				Type:       "git",
				ModuleName: buildutil.String(call, "module_name"),
			}
			if override.ModuleName != "" {
				info.Overrides = append(info.Overrides, override)
			}

		// Reference: ModuleFileGlobals.localPathOverride() - lines 674-706
		// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
		case "local_path_override":
			seenOtherDirective = true
			override := Override{
				Type:       "local_path",
				ModuleName: buildutil.String(call, "module_name"),
				Path:       buildutil.String(call, "path"),
			}
			if override.ModuleName != "" {
				info.Overrides = append(info.Overrides, override)
			}

		// Reference: ModuleFileGlobals.archiveOverride() - lines 536-606
		// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
		case "archive_override":
			seenOtherDirective = true
			override := Override{
				Type:       "archive",
				ModuleName: buildutil.String(call, "module_name"),
			}
			if override.ModuleName != "" {
				info.Overrides = append(info.Overrides, override)
			}

		default:
			// Other function calls (use_repo_rule, use_extension, etc.) also count
			// as "other directives" for the module() ordering check
			seenOtherDirective = true
		}
	}

	if !foundModule {
		return nil, fmt.Errorf("no module() declaration found")
	}

	return info, nil
}
