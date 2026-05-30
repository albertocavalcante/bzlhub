package syntaxutil

import "go.starlark.net/syntax"

// CollectTopLevelDictBindings scans top-level statements for
// `IDENT = {literal-dict}` assignments and returns the resulting map.
//
// Used by bzlwalk's Tier-1 symbol-fold (resolving `attrs = BASE | {...}`
// against same-file dict bindings) and by hermetic's integrity-hash
// detector (recognizing `INTEGRITY[platform]` patterns). Each consumer
// applies its own filter on the returned DictExpr (bzlwalk uses the
// raw dict; hermetic checks `isAllNonEmptyStringDict`).
//
// Only the precise `IDENT = DictExpr` shape is recognized. Anything
// more dynamic — `A, B = ...`, `X += ...`, `X = fn()`, conditional
// RHS — is omitted so downstream resolvers can treat the map as a
// closed proof-set.
//
// Returns nil for nil input. Otherwise returns a non-nil (possibly
// empty) map; callers can range over it without nil-guards.
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
