package syntaxutil

import "go.starlark.net/syntax"

// IdentName returns the trailing identifier of common Expr shapes, or
// "" if the node isn't a simple name.
//
// Handles three patterns:
//
//   - `foo`           → "foo"     (*syntax.Ident)
//   - `a.b.foo`       → "foo"     (*syntax.DotExpr; recursive trailing name)
//   - `name = "x"`    → "name"    (*syntax.BinaryExpr with Op == EQ; LHS-ident
//     of a kwarg pattern or default-value param)
//
// Used by both bzlwalk (rule/provider/etc. callsite classification +
// kwarg LHS extraction) and hermetic (download-call selector lookup).
// Prior to this helper each package had a near-duplicate function
// (`identName` and `selectorName`) with the same first two cases.
func IdentName(e syntax.Node) string {
	switch n := e.(type) {
	case *syntax.Ident:
		return n.Name
	case *syntax.DotExpr:
		return n.Name.Name
	case *syntax.BinaryExpr:
		if n.Op == syntax.EQ {
			return IdentName(n.X)
		}
	}
	return ""
}
