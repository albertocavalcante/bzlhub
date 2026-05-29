// Package eval provides .bzl file evaluation support.
//
// This file implements evaluation logic specific to .bzl files.
// .bzl files are Starlark modules that can define:
// - Rules (via rule())
// - Providers (via provider())
// - Macros (plain functions)
// - Constants and data structures
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/skyframe/BzlLoadFunction.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/cmdline/BazelModuleContext.java
package eval

import (
	"go.starlark.net/starlark"
)

// BzlModuleContext holds context information about a .bzl module.
// This is similar to Bazel's BazelModuleContext which stores metadata
// about the loaded module.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/cmdline/BazelModuleContext.java
type BzlModuleContext struct {
	// Label is the canonical label of the .bzl file (e.g., "//pkg:foo.bzl")
	Label string

	// RepoMapping is the repository mapping for this module.
	RepoMapping map[string]string

	// Loads is the list of directly loaded modules (not transitive).
	Loads []string

	// TransitiveDigest is a hash of this module and all its dependencies.
	TransitiveDigest []byte
}

// ExportableValue is an interface for values that need to be "exported"
// when assigned to a top-level variable in a .bzl file.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/StarlarkExportable.java
type ExportableValue interface {
	starlark.Value

	// IsExported returns true if this value has been exported.
	IsExported() bool

	// Export is called when the value is assigned to a top-level variable.
	// The name is the variable name being assigned to.
	Export(name string) error
}

// BzlVisibility defines who can load a .bzl file.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/BzlVisibility.java
type BzlVisibility int

const (
	// BzlVisibilityPublic means the .bzl can be loaded by any package.
	BzlVisibilityPublic BzlVisibility = iota

	// BzlVisibilityPrivate means the .bzl can only be loaded by the same package.
	BzlVisibilityPrivate

	// BzlVisibilityPackage means the .bzl can be loaded by specific packages.
	BzlVisibilityPackage
)

// BzlInitThreadContext holds thread-local state during .bzl execution.
// This is updated by builtins like visibility() and is read after execution
// to determine the module's visibility.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/BzlInitThreadContext.java
type BzlInitThreadContext struct {
	// Label of the .bzl file being evaluated
	Label string

	// TransitiveDigest accumulated from this file and its loads
	TransitiveDigest []byte

	// BzlVisibility set by visibility() builtin
	Visibility BzlVisibility

	// VisibilityPackages set by visibility() when using package list
	VisibilityPackages []string
}

// ThreadKeyBzlContext is the key for storing BzlInitThreadContext in the thread.
const ThreadKeyBzlContext = "starlark-go-bazel:bzl_context"

// SetBzlContext stores the BzlInitThreadContext in the thread.
func SetBzlContext(thread *starlark.Thread, ctx *BzlInitThreadContext) {
	thread.SetLocal(ThreadKeyBzlContext, ctx)
}

// GetBzlContext retrieves the BzlInitThreadContext from the thread.
func GetBzlContext(thread *starlark.Thread) *BzlInitThreadContext {
	if ctx := thread.Local(ThreadKeyBzlContext); ctx != nil {
		return ctx.(*BzlInitThreadContext)
	}
	return nil
}

// FilterExports returns only the exported values from a globals dict.
// In Bazel, this filters out private names (those starting with _).
func FilterExports(globals starlark.StringDict) starlark.StringDict {
	exports := make(starlark.StringDict)
	for name, value := range globals {
		// Skip private names
		if len(name) > 0 && name[0] == '_' {
			continue
		}
		exports[name] = value
	}
	return exports
}

// ExtractLoads extracts load statement information from a .bzl file.
// This is used to determine the module's dependencies.
//
// The returned map has module labels as keys and lists of imported symbols as values.
// For example, load("//pkg:foo.bzl", "a", b="c") would produce:
// {"//pkg:foo.bzl": ["a", "c"]}  (the original names, not aliases)
//
// Reference: BzlLoadFunction uses getLoadsFromProgram() to extract loads
func ExtractLoads(source []byte) (map[string][]string, error) {
	// This would typically parse the source to extract load() statements.
	// For now, we return an empty map as the actual parsing would be
	// done by the Starlark parser/compiler.
	return make(map[string][]string), nil
}
