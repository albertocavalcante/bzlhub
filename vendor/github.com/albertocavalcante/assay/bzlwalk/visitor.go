package bzlwalk

import (
	"strconv"
	"strings"

	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/internal/syntaxutil"
	"github.com/albertocavalcante/assay/report"
)

// scanAssign handles top-level `NAME = some_call(...)` statements.
// We recognize the canonical Bazel idioms:
//
//	my_rule = rule(impl = ..., attrs = {...})
//	MyInfo  = provider(fields = [...])
//	_repo   = repository_rule(implementation = ..., attrs = {...})
//	my_ext  = module_extension(implementation = ...)
//	my_asp  = aspect(impl = ..., attrs = {...})
func (v *visitor) scanAssign(s *syntax.AssignStmt, file string) {
	if s.Op != syntax.EQ {
		return
	}
	lhs, ok := s.LHS.(*syntax.Ident)
	if !ok {
		return
	}
	call, ok := s.RHS.(*syntax.CallExpr)
	if !ok {
		return
	}
	callee := identName(call.Fn)
	if callee == "" {
		return
	}

	prov := syntaxutil.ProvenanceFrom(file, call)
	priv := strings.HasPrefix(lhs.Name, "_")
	d := v.dialect

	switch {
	case d.IsRuleSymbol(callee):
		attrs, method := v.extractAttrsWithFold(call)
		v.report.Rules = append(v.report.Rules, report.RuleSpec{
			Name:                  lhs.Name,
			Doc:                   stringKeywordArg(call, "doc"),
			Attrs:                 attrs,
			AttrsExtractionMethod: method,
			Executable:            boolKeywordArg(call, "executable"),
			Test:                  boolKeywordArg(call, "test"),
			Private:               priv,
			Provenance:            prov,
		})

	case d.IsProviderSymbol(callee):
		v.report.Providers = append(v.report.Providers, report.ProviderSpec{
			Name:       lhs.Name,
			Doc:        stringKeywordArg(call, "doc"),
			Fields:     extractProviderFields(call),
			Private:    priv,
			Provenance: prov,
		})

	case d.IsAspectSymbol(callee):
		v.report.Aspects = append(v.report.Aspects, report.AspectSpec{
			Name:              lhs.Name,
			Doc:               stringKeywordArg(call, "doc"),
			AttrAspects:       stringListKeywordArg(call, "attr_aspects"),
			RequiredProviders: stringListKeywordArg(call, "required_providers"),
			Private:           priv,
			Provenance:        prov,
		})

	case d.IsRepositoryRuleSymbol(callee):
		attrs, method := v.extractAttrsWithFold(call)
		v.report.RepositoryRules = append(v.report.RepositoryRules, report.RepoRuleSpec{
			Name:                  lhs.Name,
			Doc:                   stringKeywordArg(call, "doc"),
			Attrs:                 attrs,
			AttrsExtractionMethod: method,
			Local:                 boolKeywordArg(call, "local"),
			Private:               priv,
			Provenance:            prov,
		})

	case d.IsModuleExtensionSymbol(callee):
		v.report.ModuleExtensions = append(v.report.ModuleExtensions, report.ModuleExtSpec{
			Name:       lhs.Name,
			Doc:        stringKeywordArg(call, "doc"),
			Private:    priv,
			Provenance: prov,
		})
	}
}

// scanTopLevelCall handles top-level calls without an assignment, e.g.,
// `toolchain_type(name = "...")` or `package(default_visibility = ...)`.
func (v *visitor) scanTopLevelCall(call *syntax.CallExpr, file string) {
	callee := identName(call.Fn)
	if callee == "" {
		return
	}
	if v.dialect.IsToolchainTypeSymbol(callee) {
		name := stringKeywordArg(call, "name")
		if name == "" {
			return
		}
		v.report.Toolchains = append(v.report.Toolchains, report.ToolchainSpec{
			Name:       name,
			Provenance: syntaxutil.ProvenanceFrom(file, call),
		})
	}
}

// scanDef handles top-level `def NAME(...)`. A top-level def is a macro
// candidate when ALL of the following hold:
//
//	(a) the name doesn't start with underscore (so it's exported);
//	(b) the first parameter isn't named "ctx" (the universal Bazel
//	    convention for rule/aspect implementation functions);
//	(c) the body contains at least one call to a rule-instantiating
//	    symbol (native.X, a load()-imported name, or a same-file
//	    rule-like binding). See bodyCallsRuleLike for details.
//	(d) the file path doesn't go through a test/example/vendor/
//	    third_party directory segment — those host test fixtures
//	    and vendored copies, not the ruleset's ship surface. See
//	    docs/macro-detection-plan.md for the data behind this rule.
//
// Condition (c) is what separates real macros from utility helpers like
// `def join_paths(a, b): return a + "/" + b` — without it, the macro
// list balloons with string/dict helpers on most real modules.
// Condition (d) cuts ~35% additional noise across the corpus (up to
// ~58% on rules_python / bazel-gazelle / rules_kotlin / rules_swift).
func (v *visitor) scanDef(s *syntax.DefStmt, file string) {
	name := s.Name.Name
	if strings.HasPrefix(name, "_") {
		return
	}
	params := make([]string, 0, len(s.Params))
	for _, p := range s.Params {
		if id := identName(p); id != "" {
			params = append(params, id)
		}
	}
	if len(params) > 0 && params[0] == "ctx" {
		return // rule/aspect implementation, not a macro
	}
	if syntaxutil.IsTestOrExamplePath(file) {
		return // test/example/vendor fixture, not part of the ship surface
	}
	candidate := pendingMacroCandidate{
		file:       file,
		stmt:       s,
		name:       name,
		params:     params,
		doc:        docStringFromBody(s.Body),
		provenance: syntaxutil.ProvenanceFrom(file, s),
	}
	if bodyCallsRuleLike(s.Body, v.macroCtx, v.composedMacros[file]) {
		v.emitMacro(candidate)
		return
	}
	// Body didn't (yet) call a rule-instantiating symbol. Queue for
	// the Phase B fixpoint — once other defs in the module are
	// identified as macros, this candidate may yet qualify.
	v.pendingMacros = append(v.pendingMacros, candidate)
}

// emitMacro records a candidate as a macro and marks its name as
// rule-like in its file's composed-macros set so subsequent
// fixpoint iterations can find it.
func (v *visitor) emitMacro(c pendingMacroCandidate) {
	v.report.Macros = append(v.report.Macros, report.MacroSpec{
		Name:       c.name,
		Params:     c.params,
		Doc:        c.doc,
		Provenance: c.provenance,
	})
	v.composedMacros[c.file][c.name] = true
}

// identName returns the identifier name of common Expr shapes, or "" if not
// a simple name. It handles bare Ident, DotExpr (a.b → "b"), and
// parameter declarations.
func identName(e syntax.Node) string {
	switch n := e.(type) {
	case *syntax.Ident:
		return n.Name
	case *syntax.DotExpr:
		return n.Name.Name
	case *syntax.BinaryExpr:
		// Default-value params like `name = "x"` show up as binary EQ.
		if n.Op == syntax.EQ {
			return identName(n.X)
		}
	}
	return ""
}

// stringKeywordArg returns the literal string value of a `name = "..."`
// keyword argument, or "" if absent or non-literal.
func stringKeywordArg(call *syntax.CallExpr, name string) string {
	if expr := keywordArg(call, name); expr != nil {
		if lit, ok := expr.(*syntax.Literal); ok {
			if s, ok := lit.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

func boolKeywordArg(call *syntax.CallExpr, name string) bool {
	if expr := keywordArg(call, name); expr != nil {
		if id, ok := expr.(*syntax.Ident); ok {
			return id.Name == "True"
		}
	}
	return false
}

func stringListKeywordArg(call *syntax.CallExpr, name string) []string {
	expr := keywordArg(call, name)
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

func keywordArg(call *syntax.CallExpr, name string) syntax.Expr {
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

// extractAttrsWithFold tries Tier-0 (literal dict) extraction first;
// on miss it falls through to Tier-1 (same-file symbol fold). Returns
// the attrs slice and the provenance tag the caller should set on
// the resulting RuleSpec/RepoRuleSpec.
//
// Returning ("", nil) — both empty — means even the symbol folder
// couldn't resolve the expression; the UI then renders the rule with
// its existing "dynamic schema" message, which is the honest answer.
func (v *visitor) extractAttrsWithFold(call *syntax.CallExpr) ([]report.AttrSpec, report.AttrsExtractionMethod) {
	if literal := extractAttrs(call, "attrs"); len(literal) > 0 {
		return literal, report.AttrsLiteral
	}
	attrsExpr := keywordArg(call, "attrs")
	if attrsExpr == nil {
		return nil, ""
	}
	ctx := &foldContext{
		sym:       v.symbols,
		loads:     v.loads,
		index:     v.moduleIndex,
		fromFile:  v.currentFile,
		seen:      map[string]bool{},
		seenFiles: map[string]bool{},
	}
	folded, ok, crossedLoad := foldAttrsExpr(attrsExpr, ctx)
	if !ok || len(folded) == 0 {
		return nil, ""
	}
	if crossedLoad {
		return folded, report.AttrsLoadResolve
	}
	return folded, report.AttrsSymbolFold
}

// extractAttrs reads `attrs = { "name": attr.string(...), ... }` literally.
// Non-literal entries are skipped with no error (best-effort static analysis).
func extractAttrs(call *syntax.CallExpr, keyword string) []report.AttrSpec {
	dict, ok := keywordArg(call, keyword).(*syntax.DictExpr)
	if !ok {
		return nil
	}
	var out []report.AttrSpec
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
		spec := report.AttrSpec{Name: keyStr}
		if valCall, ok := entry.Value.(*syntax.CallExpr); ok {
			spec.Type = attrTypeFromCall(valCall)
			spec.Doc = stringKeywordArg(valCall, "doc")
			spec.Mandatory = boolKeywordArg(valCall, "mandatory")
			if def := keywordArg(valCall, "default"); def != nil {
				spec.Default = literalAsText(def)
			}
		}
		out = append(out, spec)
	}
	return out
}

// attrTypeFromCall extracts "string" from a call like `attr.string(...)`.
// Returns "" if the call isn't of that shape.
func attrTypeFromCall(call *syntax.CallExpr) string {
	dot, ok := call.Fn.(*syntax.DotExpr)
	if !ok {
		return ""
	}
	if base, ok := dot.X.(*syntax.Ident); ok && base.Name == "attr" {
		return dot.Name.Name
	}
	return ""
}

func literalAsText(e syntax.Expr) string {
	switch n := e.(type) {
	case *syntax.Literal:
		switch v := n.Value.(type) {
		case string:
			return strconv.Quote(v)
		case int64:
			return strconv.FormatInt(v, 10)
		}
	case *syntax.Ident:
		return n.Name
	}
	return ""
}

// extractProviderFields parses provider(fields = [...]) or
// provider(fields = {"x": "doc"}).
func extractProviderFields(call *syntax.CallExpr) []string {
	expr := keywordArg(call, "fields")
	if expr == nil {
		return nil
	}
	switch n := expr.(type) {
	case *syntax.ListExpr:
		var out []string
		for _, el := range n.List {
			if lit, ok := el.(*syntax.Literal); ok {
				if s, ok := lit.Value.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	case *syntax.DictExpr:
		var out []string
		for _, e := range n.List {
			entry, ok := e.(*syntax.DictEntry)
			if !ok {
				continue
			}
			if lit, ok := entry.Key.(*syntax.Literal); ok {
				if s, ok := lit.Value.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	return nil
}

// docStringFromBody returns the first string-literal statement in a body, if any.
func docStringFromBody(body []syntax.Stmt) string {
	if len(body) == 0 {
		return ""
	}
	es, ok := body[0].(*syntax.ExprStmt)
	if !ok {
		return ""
	}
	lit, ok := es.X.(*syntax.Literal)
	if !ok {
		return ""
	}
	s, ok := lit.Value.(string)
	if !ok {
		return ""
	}
	return s
}
