package syntaxutil

import "go.starlark.net/syntax"

// KeywordArg returns the value expression of the named keyword argument
// in a call, or nil if the argument isn't present.
//
// Bazel-Starlark calls represent kwargs as `BinaryExpr{Op: EQ, X: Ident,
// Y: <value>}` entries in the call's positional-and-keyword Args slice.
// This helper walks the slice and returns the first match.
func KeywordArg(call *syntax.CallExpr, name string) syntax.Expr {
	for _, arg := range call.Args {
		bin, ok := arg.(*syntax.BinaryExpr)
		if !ok || bin.Op != syntax.EQ {
			continue
		}
		key, ok := bin.X.(*syntax.Ident)
		if !ok {
			continue
		}
		if key.Name == name {
			return bin.Y
		}
	}
	return nil
}

// StringKeywordArg returns the literal string value of a `name = "..."`
// keyword argument, or "" when the argument is absent or its value is
// non-literal.
func StringKeywordArg(call *syntax.CallExpr, name string) string {
	if expr := KeywordArg(call, name); expr != nil {
		if lit, ok := expr.(*syntax.Literal); ok {
			if s, ok := lit.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// BoolKeywordArg reports whether a `name = True` keyword argument is
// present with the literal `True` identifier. False covers both
// "absent" and "= False"; callers that need to distinguish should use
// KeywordArg directly.
func BoolKeywordArg(call *syntax.CallExpr, name string) bool {
	if expr := KeywordArg(call, name); expr != nil {
		if id, ok := expr.(*syntax.Ident); ok {
			return id.Name == "True"
		}
	}
	return false
}

// StringListKeywordArg returns the literal string entries of a list-
// valued keyword argument, in source order. Non-literal entries are
// silently skipped — the function returns the prefix that DID
// statically resolve.
func StringListKeywordArg(call *syntax.CallExpr, name string) []string {
	expr := KeywordArg(call, name)
	if expr == nil {
		return nil
	}
	list, ok := expr.(*syntax.ListExpr)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range list.List {
		if lit, ok := el.(*syntax.Literal); ok {
			if s, ok := lit.Value.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
