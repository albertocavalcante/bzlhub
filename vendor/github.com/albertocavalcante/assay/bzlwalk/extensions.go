package bzlwalk

import (
	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/dialect"
	"github.com/albertocavalcante/assay/report"
	syntaxutil "github.com/albertocavalcante/go-starlark-syntaxutil"
)

// collectTagClassBindings pre-scans a file's top-level statements for
// `IDENT = tag_class(...)` bindings. The result maps each LHS name to
// its tag_class call expression so module_extension's tag_classes dict
// can resolve Ident values regardless of order — Starlark module-scope
// resolution doesn't care whether the binding appears above or below
// its use site.
//
// Returns an empty map when no tag_class bindings exist; never nil so
// the caller can write into it without a nil-check (it's read-only
// post-collection in practice).
func collectTagClassBindings(f *syntax.File, d dialect.Dialect) map[string]*syntax.CallExpr {
	out := map[string]*syntax.CallExpr{}
	for _, stmt := range f.Stmts {
		assign, ok := stmt.(*syntax.AssignStmt)
		if !ok || assign.Op != syntax.EQ {
			continue
		}
		lhs, ok := assign.LHS.(*syntax.Ident)
		if !ok {
			continue
		}
		call, ok := assign.RHS.(*syntax.CallExpr)
		if !ok {
			continue
		}
		callee := syntaxutil.IdentName(call.Fn)
		if callee == "" || !d.IsTagClassSymbol(callee) {
			continue
		}
		out[lhs.Name] = call
	}
	return out
}

// extractTagClasses resolves a module_extension call's `tag_classes`
// kwarg into a slice of TagClassSpecs. Each dict entry's key must be a
// string literal (the public tag-class name); each value must be an
// Ident that resolves to a same-file `tag_class(...)` binding through
// v.tagClassBindings. Entries that don't fit this shape (unresolved
// Idents, non-string keys, inline calls) are silently dropped — best-
// effort static analysis.
//
// Attrs follow the existing extraction-tier ladder via
// extractAttrsWithFold, so the same literal / symbol-fold /
// load-resolve coverage rules apply.
func (v *visitor) extractTagClasses(call *syntax.CallExpr) []report.TagClassSpec {
	dict, ok := syntaxutil.KeywordArg(call, "tag_classes").(*syntax.DictExpr)
	if !ok {
		return nil
	}
	out := make([]report.TagClassSpec, 0, len(dict.List))
	for _, e := range dict.List {
		entry, ok := e.(*syntax.DictEntry)
		if !ok {
			continue
		}
		keyLit, ok := entry.Key.(*syntax.Literal)
		if !ok {
			continue
		}
		keyStr, ok := keyLit.Value.(string)
		if !ok {
			continue
		}
		valIdent, ok := entry.Value.(*syntax.Ident)
		if !ok {
			continue
		}
		tcCall, ok := v.tagClassBindings[valIdent.Name]
		if !ok {
			continue
		}
		attrs, method := v.extractAttrsWithFold(tcCall)
		out = append(out, report.TagClassSpec{
			Name:                  keyStr,
			Doc:                   syntaxutil.StringKeywordArg(tcCall, "doc"),
			Attrs:                 attrs,
			AttrsExtractionMethod: method,
			Provenance:            report.ProvenanceFromNode(v.currentFile, tcCall),
		})
	}
	return out
}
