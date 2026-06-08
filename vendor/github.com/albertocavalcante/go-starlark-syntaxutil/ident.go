package syntaxutil

import "go.starlark.net/syntax"

// IdentName returns the trailing identifier of common expression
// shapes, or "" if the node isn't a simple name.
//
// Recognized patterns:
//
//	foo         => "foo"   (*syntax.Ident)
//	a.b.foo     => "foo"   (*syntax.DotExpr; trailing name)
//	name = "x"  => "name"  (*syntax.BinaryExpr with Op == EQ; LHS ident
//	                        of a kwarg or default-value parameter)
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
