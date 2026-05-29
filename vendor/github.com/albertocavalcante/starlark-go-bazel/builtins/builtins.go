// Package builtins provides Bazel's predeclared Starlark builtins.
//
// This includes top-level symbols available in .bzl files such as:
//   - rule() - for defining rules
//   - provider() - for defining providers
//   - aspect() - for defining aspects
//   - select() - for configurable attributes
//   - struct() - for creating immutable structs
//   - depset() - for creating depsets
//   - Label() - for creating labels
//   - attr module - for defining rule attributes
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/analysis/starlark/StarlarkGlobalsImpl.java
package builtins

import (
	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// Predeclared returns all predeclared Bazel builtins for .bzl files.
// These are the top-level symbols available when evaluating a .bzl file.
func Predeclared() starlark.StringDict {
	return starlark.StringDict{
		// Core builtins
		"rule":     starlark.NewBuiltin("rule", Rule),
		"provider": starlark.NewBuiltin("provider", Provider),
		"aspect":   starlark.NewBuiltin("aspect", Aspect),
		"select":   starlark.NewBuiltin("select", Select),
		"struct":   starlark.NewBuiltin("struct", StructBuiltin),

		// Type constructors
		"depset": starlark.NewBuiltin("depset", DepsetBuiltin),
		"Label":  starlark.NewBuiltin("Label", types.LabelBuiltin),

		// Modules
		"attr": AttrModule(),
	}
}

// BuildFilePredeclared returns predeclared builtins for BUILD files.
// BUILD files have a subset of .bzl file builtins plus native rule functions.
func BuildFilePredeclared() starlark.StringDict {
	return starlark.StringDict{
		"select": starlark.NewBuiltin("select", Select),
		"depset": starlark.NewBuiltin("depset", DepsetBuiltin),
		"Label":  starlark.NewBuiltin("Label", types.LabelBuiltin),
	}
}
