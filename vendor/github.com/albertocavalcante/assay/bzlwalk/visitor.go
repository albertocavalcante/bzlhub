package bzlwalk

import (
	"strconv"
	"strings"

	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/report"
	syntaxutil "github.com/albertocavalcante/go-starlark-syntaxutil"
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
	callee := syntaxutil.IdentName(call.Fn)
	if callee == "" {
		return
	}

	prov := report.ProvenanceFromNode(file, call)
	priv := strings.HasPrefix(lhs.Name, "_")
	d := v.dialect

	switch {
	case d.IsRuleSymbol(callee):
		attrs, method := v.extractAttrsWithFold(call)
		v.report.Rules = append(v.report.Rules, report.RuleSpec{
			Name:                  lhs.Name,
			Doc:                   syntaxutil.StringKeywordArg(call, "doc"),
			Attrs:                 attrs,
			AttrsExtractionMethod: method,
			Executable:            syntaxutil.BoolKeywordArg(call, "executable"),
			Test:                  syntaxutil.BoolKeywordArg(call, "test"),
			Private:               priv,
			Provenance:            prov,
		})

	case d.IsProviderSymbol(callee):
		v.report.Providers = append(v.report.Providers, report.ProviderSpec{
			Name:       lhs.Name,
			Doc:        syntaxutil.StringKeywordArg(call, "doc"),
			Fields:     extractProviderFields(call),
			Private:    priv,
			Provenance: prov,
		})

	case d.IsAspectSymbol(callee):
		attrs, method := v.extractAttrsWithFold(call)
		v.report.Aspects = append(v.report.Aspects, report.AspectSpec{
			Name:                   lhs.Name,
			Doc:                    syntaxutil.StringKeywordArg(call, "doc"),
			AttrAspects:            syntaxutil.StringListKeywordArg(call, "attr_aspects"),
			RequiredProviders:      syntaxutil.IdentListKeywordArg(call, "required_providers"),
			Attrs:                  attrs,
			AttrsExtractionMethod:  method,
			Provides:               syntaxutil.IdentListKeywordArg(call, "provides"),
			Fragments:              syntaxutil.StringListKeywordArg(call, "fragments"),
			HostFragments:          syntaxutil.StringListKeywordArg(call, "host_fragments"),
			Toolchains:             syntaxutil.StringListKeywordArg(call, "toolchains"),
			ApplyToGeneratingRules: syntaxutil.BoolKeywordArg(call, "apply_to_generating_rules"),
			Private:                priv,
			Provenance:             prov,
		})

	case d.IsRepositoryRuleSymbol(callee):
		attrs, method := v.extractAttrsWithFold(call)
		v.report.RepositoryRules = append(v.report.RepositoryRules, report.RepoRuleSpec{
			Name:                  lhs.Name,
			Doc:                   syntaxutil.StringKeywordArg(call, "doc"),
			Attrs:                 attrs,
			AttrsExtractionMethod: method,
			Local:                 syntaxutil.BoolKeywordArg(call, "local"),
			Private:               priv,
			Provenance:            prov,
		})

	case d.IsModuleExtensionSymbol(callee):
		v.report.ModuleExtensions = append(v.report.ModuleExtensions, report.ModuleExtSpec{
			Name:       lhs.Name,
			Doc:        syntaxutil.StringKeywordArg(call, "doc"),
			TagClasses: v.extractTagClasses(call),
			Private:    priv,
			Provenance: prov,
		})

	case d.IsTagClassSymbol(callee):
		// tag_class bindings are recorded in the pre-pass (see
		// collectTagClassBindings) and surfaced only as children of
		// module_extension's tag_classes dict. They have no
		// independent report entry — nothing to do here.
		return
	}
}

// scanTopLevelCall handles top-level calls without an assignment, e.g.,
// `toolchain_type(name = "...")`, `toolchain(name = ..., toolchain_type = ...)`,
// or `package(default_visibility = ...)`.
func (v *visitor) scanTopLevelCall(call *syntax.CallExpr, file string) {
	callee := syntaxutil.IdentName(call.Fn)
	if callee == "" {
		return
	}
	switch {
	case v.dialect.IsToolchainTypeSymbol(callee):
		name := syntaxutil.StringKeywordArg(call, "name")
		if name == "" {
			return
		}
		v.report.Toolchains = append(v.report.Toolchains, report.ToolchainSpec{
			Name:       name,
			Provenance: report.ProvenanceFromNode(file, call),
		})

	case v.dialect.IsToolchainSymbol(callee):
		name := syntaxutil.StringKeywordArg(call, "name")
		tt := syntaxutil.StringKeywordArg(call, "toolchain_type")
		// A toolchain registration without a name or a type is
		// unusable downstream (Bazel itself will error); skip
		// rather than emit a half-populated entry.
		if name == "" || tt == "" {
			return
		}
		v.report.ToolchainImpls = append(v.report.ToolchainImpls, report.ToolchainImpl{
			Name:                 name,
			ToolchainType:        tt,
			ToolchainImpl:        syntaxutil.StringKeywordArg(call, "toolchain"),
			ExecCompatibleWith:   syntaxutil.StringListKeywordArg(call, "exec_compatible_with"),
			TargetCompatibleWith: syntaxutil.StringListKeywordArg(call, "target_compatible_with"),
			TargetSettings:       syntaxutil.StringListKeywordArg(call, "target_settings"),
			Provenance:           report.ProvenanceFromNode(file, call),
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
		if id := syntaxutil.IdentName(p); id != "" {
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
		provenance: report.ProvenanceFromNode(file, s),
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
	attrsExpr := syntaxutil.KeywordArg(call, "attrs")
	if attrsExpr == nil {
		return nil, report.AttrsUnresolved
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
		return nil, report.AttrsUnresolved
	}
	if crossedLoad {
		return folded, report.AttrsLoadResolve
	}
	return folded, report.AttrsSymbolFold
}

// extractAttrs reads `attrs = { "name": attr.string(...), ... }` literally.
// Non-literal entries are skipped with no error (best-effort static analysis).
func extractAttrs(call *syntax.CallExpr, keyword string) []report.AttrSpec {
	dict, ok := syntaxutil.KeywordArg(call, keyword).(*syntax.DictExpr)
	if !ok {
		return nil
	}
	out := make([]report.AttrSpec, 0, len(dict.List))
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
		out = append(out, attrSpecFromCall(keyStr, entry.Value))
	}
	return out
}

// providerGroupsFromExpr maps the value expression of a
// `providers = ...` kwarg into the disjunction-of-conjunctions
// shape: outer slices are OR alternatives, inner slices are AND
// requirements within an alternative.
//
// Three shapes are recognized literally:
//
//	providers = [GoInfo]        -> [[GoInfo]]
//	providers = [A, B, C]       -> [[A, B, C]]   (one conjunction)
//	providers = [[A], [B, C]]   -> [[A], [B, C]] (disjunction)
//
// Bare provider names collapse into the first conjunction so
// simple-form semantics ("all of these required") are preserved.
// Non-list values and inner entries that aren't idents or
// lists-of-idents are silently dropped.
func providerGroupsFromExpr(expr syntax.Expr) [][]string {
	list, ok := expr.(*syntax.ListExpr)
	if !ok {
		return nil
	}
	out := make([][]string, 0, len(list.List))
	for _, el := range list.List {
		switch n := el.(type) {
		case *syntax.Ident:
			if len(out) == 0 {
				out = append(out, []string{n.Name})
			} else {
				out[0] = append(out[0], n.Name)
			}
		case *syntax.ListExpr:
			var conj []string
			for _, inner := range n.List {
				if id, ok := inner.(*syntax.Ident); ok {
					conj = append(conj, id.Name)
				}
			}
			if len(conj) > 0 {
				out = append(out, conj)
			}
		}
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
	expr := syntaxutil.KeywordArg(call, "fields")
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
