package syntaxutil

import "go.starlark.net/syntax"

// CollectTopLevelDictBindings scans top-level statements for
// IDENT = {literal-dict} assignments and returns the resulting map.
//
// Only the precise IDENT = DictExpr shape is recognized. More dynamic
// forms (tuple destructuring, augmented assignment, call-valued RHS,
// conditional RHS) are omitted so downstream resolvers can treat the
// result as a closed set.
//
// Returns nil for nil input; otherwise returns a non-nil (possibly
// empty) map.
func CollectTopLevelDictBindings(f *syntax.File) map[string]*syntax.DictExpr {
	if f == nil {
		return nil
	}
	out := map[string]*syntax.DictExpr{}
	for _, stmt := range f.Stmts {
		assign, ok := stmt.(*syntax.AssignStmt)
		if !ok || assign.Op != syntax.EQ {
			continue
		}
		ident, ok := assign.LHS.(*syntax.Ident)
		if !ok {
			continue
		}
		dict, ok := assign.RHS.(*syntax.DictExpr)
		if !ok {
			continue
		}
		out[ident.Name] = dict
	}
	return out
}
