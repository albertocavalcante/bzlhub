package bzlwalk

import (
	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/report"
)

// attrSpecFromCall builds a single AttrSpec from a dict entry's
// (name, value) pair. The value is expected to be an attr.<type>(...)
// call; a non-CallExpr value yields an AttrSpec with only Name set.
//
// Shared between the Tier-0 literal walker ([extractAttrs]) and the
// Tier-1/2 symbol-fold walker ([dictEntriesToAttrs]) so the
// kwarg-extraction policy lives in exactly one place.
//
// Implementation walks valCall.Args once, dispatching on each
// keyword's identifier. Duplicate keys keep the first occurrence
// (matches the semantics of go-starlark-syntaxutil's KeywordArg).
func attrSpecFromCall(name string, value syntax.Expr) report.AttrSpec {
	spec := report.AttrSpec{Name: name}
	valCall, ok := value.(*syntax.CallExpr)
	if !ok {
		return spec
	}
	spec.Type = attrTypeFromCall(valCall)

	var (
		sawDoc, sawMandatory, sawDefault, sawProviders bool
	)
	for _, arg := range valCall.Args {
		bin, ok := arg.(*syntax.BinaryExpr)
		if !ok || bin.Op != syntax.EQ {
			continue
		}
		key, ok := bin.X.(*syntax.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "doc":
			if sawDoc {
				continue
			}
			sawDoc = true
			if lit, ok := bin.Y.(*syntax.Literal); ok {
				if s, ok := lit.Value.(string); ok {
					spec.Doc = s
				}
			}
		case "mandatory":
			if sawMandatory {
				continue
			}
			sawMandatory = true
			if id, ok := bin.Y.(*syntax.Ident); ok && id.Name == "True" {
				spec.Mandatory = true
			}
		case "default":
			if sawDefault {
				continue
			}
			sawDefault = true
			spec.Default = literalAsText(bin.Y)
		case "providers":
			if sawProviders {
				continue
			}
			sawProviders = true
			spec.ProviderGroups = providerGroupsFromExpr(bin.Y)
		}
	}
	return spec
}
