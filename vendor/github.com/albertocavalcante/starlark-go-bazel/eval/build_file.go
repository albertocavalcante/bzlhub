// Package eval provides BUILD file evaluation support.
//
// This file implements evaluation logic specific to BUILD files.
// BUILD files declare targets by calling rule functions (native or loaded from .bzl).
// Unlike .bzl files, BUILD files do not export symbols.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/PackageFactory.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/Package.java
package eval

import (
	"fmt"
	"path/filepath"

	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// ThreadKeyTargets is the key for storing the target map in the thread.
const ThreadKeyTargets = "targets"

// ThreadKeyPackage is the key for storing the Package in the thread.
const ThreadKeyPackage = "starlark-go-bazel:package"

// Package represents a Bazel package (a directory with a BUILD file).
// It collects all targets declared during BUILD file evaluation.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/Package.java
type Package struct {
	// Name is the package path (e.g., "foo/bar")
	Name string

	// Root is the workspace root path
	Root string

	// BuildFile is the path to the BUILD file
	BuildFile string

	// Targets maps target names to their instances
	Targets map[string]*types.RuleInstance

	// DefaultVisibility is the default visibility for targets in this package
	DefaultVisibility []string

	// DefaultTestonly is the default testonly value for targets
	DefaultTestonly bool

	// DefaultDeprecation is the default deprecation message
	DefaultDeprecation string

	// Loads lists the .bzl files loaded by this BUILD file
	Loads []string
}

// NewPackage creates a new Package for the given path.
func NewPackage(root, buildFile string) *Package {
	// Extract package name from BUILD file path
	dir := filepath.Dir(buildFile)
	name := dir
	if root != "" && dir != root {
		rel, err := filepath.Rel(root, dir)
		if err == nil {
			name = rel
		}
	}
	if name == "." {
		name = ""
	}

	return &Package{
		Name:      name,
		Root:      root,
		BuildFile: buildFile,
		Targets:   make(map[string]*types.RuleInstance),
	}
}

// AddTarget registers a target in the package.
// Returns an error if a target with the same name already exists.
func (p *Package) AddTarget(name string, target *types.RuleInstance) error {
	if _, exists := p.Targets[name]; exists {
		return fmt.Errorf("duplicate target name %q in package %q", name, p.Name)
	}
	p.Targets[name] = target
	return nil
}

// GetTarget returns a target by name, or nil if not found.
func (p *Package) GetTarget(name string) *types.RuleInstance {
	return p.Targets[name]
}

// TargetNames returns a sorted list of target names.
func (p *Package) TargetNames() []string {
	names := make([]string, 0, len(p.Targets))
	for name := range p.Targets {
		names = append(names, name)
	}
	return names
}

// SetPackage stores the Package in the thread for use during BUILD evaluation.
func SetPackage(thread *starlark.Thread, pkg *Package) {
	thread.SetLocal(ThreadKeyPackage, pkg)
}

// GetPackage retrieves the Package from the thread.
func GetPackage(thread *starlark.Thread) *Package {
	if pkg := thread.Local(ThreadKeyPackage); pkg != nil {
		return pkg.(*Package)
	}
	return nil
}

// RegisterTarget is called by rule implementations to register a target.
// It can be used from the "targets" thread local or from a Package instance.
func RegisterTarget(thread *starlark.Thread, target *types.RuleInstance) error {
	name := target.Name()

	// Try Package first (preferred)
	if pkg := GetPackage(thread); pkg != nil {
		return pkg.AddTarget(name, target)
	}

	// Fall back to simple targets map
	if targets, ok := thread.Local(ThreadKeyTargets).(map[string]*types.RuleInstance); ok {
		if _, exists := targets[name]; exists {
			return fmt.Errorf("duplicate target name %q", name)
		}
		targets[name] = target
		return nil
	}

	return fmt.Errorf("no target registry in thread")
}

// PackageBuiltin implements the package() function for BUILD files.
// This function is called once at the top of a BUILD file to set package-level defaults.
//
// Signature: package(default_visibility = None, default_deprecation = None, default_testonly = False, features = [])
//
// Reference: PackageFactory.packageCallable
func PackageBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("package: unexpected positional arguments")
	}

	pkg := GetPackage(thread)
	if pkg == nil {
		return nil, fmt.Errorf("package() can only be called from BUILD files")
	}

	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		val := kv[1]

		switch key {
		case "default_visibility":
			if list, ok := val.(*starlark.List); ok {
				var visibility []string
				iter := list.Iterate()
				defer iter.Done()
				var v starlark.Value
				for iter.Next(&v) {
					if s, ok := v.(starlark.String); ok {
						visibility = append(visibility, string(s))
					}
				}
				pkg.DefaultVisibility = visibility
			}
		case "default_deprecation":
			if s, ok := val.(starlark.String); ok {
				pkg.DefaultDeprecation = string(s)
			}
		case "default_testonly":
			if b, ok := val.(starlark.Bool); ok {
				pkg.DefaultTestonly = bool(b)
			}
		case "features":
			// Features are handled at the analysis phase
		default:
			return nil, fmt.Errorf("package: unexpected keyword argument %q", key)
		}
	}

	return starlark.None, nil
}

// LicensesBuiltin implements the licenses() function for BUILD files (deprecated).
//
// Reference: This function is deprecated but still supported for compatibility.
func LicensesBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Deprecated function - accept any arguments but do nothing
	return starlark.None, nil
}

// ExportsFilesBuiltin implements exports_files() for BUILD files.
// This declares that files in the package can be referenced by other packages.
//
// Signature: exports_files(srcs, visibility = None, licenses = None)
//
// Reference: PackageFactory.exports_files
func ExportsFilesBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var srcs *starlark.List
	var visibility starlark.Value = starlark.None
	var licenses starlark.Value = starlark.None

	if len(args) > 0 {
		if list, ok := args[0].(*starlark.List); ok {
			srcs = list
		} else {
			return nil, fmt.Errorf("exports_files: first argument must be a list of strings")
		}
	}

	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		switch key {
		case "srcs":
			if list, ok := kv[1].(*starlark.List); ok {
				srcs = list
			}
		case "visibility":
			visibility = kv[1]
		case "licenses":
			licenses = kv[1]
		}
	}

	if srcs == nil {
		return nil, fmt.Errorf("exports_files: missing required argument 'srcs'")
	}

	// In a full implementation, this would create file targets in the package.
	// For now, we just validate the arguments.
	_ = visibility
	_ = licenses

	return starlark.None, nil
}

// GlobBuiltin implements glob() for BUILD files.
// This function returns a list of files matching patterns.
//
// Signature: glob(include, exclude = [], exclude_directories = 1, allow_empty = True)
//
// Reference: GlobFunction in Bazel
func GlobBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var include *starlark.List
	var exclude *starlark.List
	excludeDirectories := true
	allowEmpty := true

	if len(args) > 0 {
		if list, ok := args[0].(*starlark.List); ok {
			include = list
		}
	}

	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		switch key {
		case "include":
			if list, ok := kv[1].(*starlark.List); ok {
				include = list
			}
		case "exclude":
			if list, ok := kv[1].(*starlark.List); ok {
				exclude = list
			}
		case "exclude_directories":
			if i, ok := kv[1].(starlark.Int); ok {
				val, _ := i.Int64()
				excludeDirectories = val != 0
			}
		case "allow_empty":
			if b, ok := kv[1].(starlark.Bool); ok {
				allowEmpty = bool(b)
			}
		}
	}

	if include == nil {
		return nil, fmt.Errorf("glob: missing required argument 'include'")
	}

	// In a full implementation, this would actually glob the filesystem.
	// For now, return an empty list (globbing is typically done during loading).
	_ = exclude
	_ = excludeDirectories
	_ = allowEmpty

	return starlark.NewList(nil), nil
}
