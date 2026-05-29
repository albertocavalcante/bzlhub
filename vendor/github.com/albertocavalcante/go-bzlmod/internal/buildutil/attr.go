// Package buildutil provides utilities for extracting attributes from
// buildtools AST nodes.
//
// This package consolidates attribute extraction logic used by both the
// legacy parser (package gobzlmod) and the new AST parser (package ast).
package buildutil

import (
	"strconv"

	"github.com/albertocavalcante/go-bzlmod/third_party/buildtools/build"
)

// String extracts a string attribute from a function call by name.
// If name is empty and the call has positional arguments, returns the first
// positional string argument.
// Returns empty string if the attribute is not found or not a string.
func String(call *build.CallExpr, name string) string {
	// Handle first positional argument when name is empty
	if name == "" && len(call.List) > 0 {
		if str, ok := call.List[0].(*build.StringExpr); ok {
			return str.Value
		}
		return ""
	}

	for _, arg := range call.List {
		assign, ok := arg.(*build.AssignExpr)
		if !ok {
			continue
		}
		lhs, ok := assign.LHS.(*build.Ident)
		if !ok || lhs.Name != name {
			continue
		}
		if str, ok := assign.RHS.(*build.StringExpr); ok {
			return str.Value
		}
	}
	return ""
}

// Int extracts an integer attribute from a function call by name.
// Returns 0 if the attribute is not found or not a valid integer.
func Int(call *build.CallExpr, name string) int {
	for _, arg := range call.List {
		assign, ok := arg.(*build.AssignExpr)
		if !ok {
			continue
		}
		lhs, ok := assign.LHS.(*build.Ident)
		if !ok || lhs.Name != name {
			continue
		}
		if lit, ok := assign.RHS.(*build.LiteralExpr); ok {
			if val, err := strconv.Atoi(lit.Token); err == nil {
				return val
			}
		}
	}
	return 0
}

// Bool extracts a boolean attribute from a function call by name.
// Returns false if the attribute is not found or not a boolean identifier.
func Bool(call *build.CallExpr, name string) bool {
	for _, arg := range call.List {
		assign, ok := arg.(*build.AssignExpr)
		if !ok {
			continue
		}
		lhs, ok := assign.LHS.(*build.Ident)
		if !ok || lhs.Name != name {
			continue
		}
		if ident, ok := assign.RHS.(*build.Ident); ok {
			return ident.Name == "True"
		}
	}
	return false
}

// IsNone returns true if the named attribute exists and is set to None.
func IsNone(call *build.CallExpr, name string) bool {
	for _, arg := range call.List {
		assign, ok := arg.(*build.AssignExpr)
		if !ok {
			continue
		}
		lhs, ok := assign.LHS.(*build.Ident)
		if !ok || lhs.Name != name {
			continue
		}
		if ident, ok := assign.RHS.(*build.Ident); ok {
			return ident.Name == "None"
		}
	}
	return false
}

// StringList extracts a list of strings attribute from a function call by name.
// Returns nil if the attribute is not found or not a list.
// Non-string elements in the list are silently skipped.
func StringList(call *build.CallExpr, name string) []string {
	for _, arg := range call.List {
		assign, ok := arg.(*build.AssignExpr)
		if !ok {
			continue
		}
		lhs, ok := assign.LHS.(*build.Ident)
		if !ok || lhs.Name != name {
			continue
		}
		list, ok := assign.RHS.(*build.ListExpr)
		if !ok {
			return nil
		}
		result := make([]string, 0, len(list.List))
		for _, elem := range list.List {
			if str, ok := elem.(*build.StringExpr); ok {
				result = append(result, str.Value)
			}
		}
		return result
	}
	return nil
}

// PositionalStrings returns all positional string arguments from a call,
// optionally skipping the first n arguments.
func PositionalStrings(call *build.CallExpr, skip int) []string {
	var result []string
	for i, arg := range call.List {
		if i < skip {
			continue
		}
		// Skip named arguments
		if _, ok := arg.(*build.AssignExpr); ok {
			continue
		}
		if str, ok := arg.(*build.StringExpr); ok {
			result = append(result, str.Value)
		}
	}
	return result
}

// ExtractValue converts a build.Expr to a Go value.
// Handles strings, integers, booleans (True/False/None), lists, and dicts.
// Returns the raw expression for unhandled types.
func ExtractValue(expr build.Expr) any {
	switch e := expr.(type) {
	case *build.StringExpr:
		return e.Value
	case *build.LiteralExpr:
		if val, err := strconv.Atoi(e.Token); err == nil {
			return val
		}
		return e.Token
	case *build.Ident:
		switch e.Name {
		case "True":
			return true
		case "False":
			return false
		case "None":
			return nil
		default:
			return e.Name
		}
	case *build.ListExpr:
		result := make([]any, 0, len(e.List))
		for _, item := range e.List {
			result = append(result, ExtractValue(item))
		}
		return result
	case *build.DictExpr:
		result := make(map[string]any)
		for _, kv := range e.List {
			if keyStr, ok := kv.Key.(*build.StringExpr); ok {
				result[keyStr.Value] = ExtractValue(kv.Value)
			}
		}
		return result
	default:
		return expr
	}
}

// FuncName returns the function name from a CallExpr.
// Returns empty string if the call is not a simple function call
// (e.g., method calls like foo.bar()).
func FuncName(call *build.CallExpr) string {
	if ident, ok := call.X.(*build.Ident); ok {
		return ident.Name
	}
	return ""
}

// IsFuncCall returns true if the call is for the specified function name.
func IsFuncCall(call *build.CallExpr, name string) bool {
	return FuncName(call) == name
}
