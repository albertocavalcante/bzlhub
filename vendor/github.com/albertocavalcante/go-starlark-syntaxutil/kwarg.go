package syntaxutil

import "go.starlark.net/syntax"

// KeywordArg returns the value expression of the named keyword
// argument in a call, or nil if the argument isn't present.
//
// In go.starlark.net's AST, kwargs are BinaryExpr{Op: EQ, X: Ident,
// Y: value} entries in the call's combined Args slice.
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
// present with the literal True identifier. False covers both absent
// and `= False`; callers that need to distinguish should use
// [KeywordArg] directly.
func BoolKeywordArg(call *syntax.CallExpr, name string) bool {
	if expr := KeywordArg(call, name); expr != nil {
		if id, ok := expr.(*syntax.Ident); ok {
			return id.Name == "True"
		}
	}
	return false
}

// IntKeywordArg returns the literal int value of a `name = N` keyword
// argument. The second return reports whether the kwarg was present
// and the value was an integer literal, so callers can distinguish
// absent from `= 0`.
func IntKeywordArg(call *syntax.CallExpr, name string) (int64, bool) {
	if expr := KeywordArg(call, name); expr != nil {
		if lit, ok := expr.(*syntax.Literal); ok {
			if n, ok := lit.Value.(int64); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// PositionalStrings returns each positional argument that is a
// string literal, in source order. Keyword arguments and non-string
// positional arguments are silently skipped.
func PositionalStrings(call *syntax.CallExpr) []string {
	var out []string
	for _, arg := range call.Args {
		if bin, isBin := arg.(*syntax.BinaryExpr); isBin && bin.Op == syntax.EQ {
			continue
		}
		if lit, ok := arg.(*syntax.Literal); ok {
			if s, ok := lit.Value.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// IdentListKeywordArg returns the names of bare-ident entries in a
// list-valued keyword argument, in source order. Non-ident entries
// (string literals, calls, etc.) are silently skipped.
//
// Useful for kwargs whose values are symbol references rather than
// strings, for example `provides = [Foo, Bar]`.
func IdentListKeywordArg(call *syntax.CallExpr, name string) []string {
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
		if id, ok := el.(*syntax.Ident); ok {
			out = append(out, id.Name)
		}
	}
	return out
}

// StringListKeywordArg returns the literal string entries of a
// list-valued keyword argument, in source order. Non-literal entries
// are silently skipped.
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
