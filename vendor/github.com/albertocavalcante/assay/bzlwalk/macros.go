package bzlwalk

// EPISTEMIC STATUS — HEURISTIC
// ----------------------------
// Macro detection is a heuristic. There's no AST-direct signal for
// "this def is a Bazel macro." We approximate it with four filters
// PLUS a fixpoint composition pass:
//
//   1. Name doesn't start with `_` (exported).
//   2. First parameter isn't named `ctx` (rule/aspect impl convention).
//   3. Body invokes a rule-instantiating call: `native.X`, a
//      load()-imported name, a same-file binding of rule() /
//      repository_rule() / module_extension() / aspect(), OR a
//      previously-identified macro in the same file (Phase B
//      fixpoint).
//   4. File path doesn't traverse a test/example/vendor segment
//      (Phase A).
//
// Phase B fixpoint: defs that don't pass (3) on the first scan are
// queued. After all files scan, identified macros become evidence
// for queued candidates; the queue re-evaluates. Iterate until no
// new macros emerge. Bounded to 8 iterations as a safety net; real
// call graphs don't get that deep.
//
// Known weaknesses:
//
//   - A utility helper that happens to call a load()-imported function
//     not actually a rule (e.g., a string formatter from skylib) would
//     false-positive as a macro. Curating load() targets is out of
//     scope.
//   - Cross-file load resolution for the macro context isn't done — a
//     macro that calls a name from another local file's def-macro
//     chain (and doesn't go through native or a direct rule binding)
//     would be missed. Phase C2 would close this; deferred per
//     docs/macro-detection-plan.md.
//   - Private (`_`-prefixed) defs are never candidates, so a public
//     macro that ONLY composes a private def-macro is missed. This
//     pattern is uncommon enough to not warrant including private
//     candidates today.
//
// MacroSpec output entries should be treated as "exported defs likely
// to be macros" rather than "macros, period."

import (
	"sort"

	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/dialect"
	"github.com/albertocavalcante/assay/report"
)

// fileMacroContext is the per-file set of names that, when called from
// a top-level def body, mark the def as a rule-instantiating macro
// rather than a utility helper. Populated once per file before any
// scanDef call.
type fileMacroContext struct {
	// loaded are bare identifiers brought in via load() at file top level.
	loaded map[string]bool
	// ruleLike are bare identifiers bound to rule(), repository_rule(),
	// module_extension(), or aspect() calls in this file.
	ruleLike map[string]bool
}

// collectMacroContext pre-scans a file's top-level statements to gather
// the symbols a macro might invoke. Two independent sources:
//
//  1. load() statements — anything imported is assumed callable.
//  2. Top-level assignments where RHS is a rule()/repository_rule()/
//     module_extension()/aspect() call — these define rule-like symbols
//     callable from a macro in the same file.
//
// Native built-ins (native.cc_library, native.genrule, …) are NOT in
// either map; they're detected directly by bodyCallsRuleLike via the
// `native.X` AST shape.
func collectMacroContext(f *syntax.File, d dialect.Dialect) fileMacroContext {
	ctx := fileMacroContext{
		loaded:   map[string]bool{},
		ruleLike: map[string]bool{},
	}
	if f == nil {
		return ctx
	}
	for _, stmt := range f.Stmts {
		switch s := stmt.(type) {
		case *syntax.LoadStmt:
			// LoadStmt.From holds the local-binding names in this file
			// (despite the counterintuitive name — see go.starlark.net
			// syntax.LoadStmt doc comments).
			for _, id := range s.From {
				if id != nil {
					ctx.loaded[id.Name] = true
				}
			}
		case *syntax.AssignStmt:
			if s.Op != syntax.EQ {
				continue
			}
			lhs, ok := s.LHS.(*syntax.Ident)
			if !ok {
				continue
			}
			call, ok := s.RHS.(*syntax.CallExpr)
			if !ok {
				continue
			}
			callee := identName(call.Fn)
			if callee == "" {
				continue
			}
			if d.IsRuleSymbol(callee) ||
				d.IsRepositoryRuleSymbol(callee) ||
				d.IsModuleExtensionSymbol(callee) ||
				d.IsAspectSymbol(callee) {
				ctx.ruleLike[lhs.Name] = true
			}
		}
	}
	return ctx
}

// bodyCallsRuleLike reports whether any call expression inside body has
// a callee that qualifies as rule-instantiating:
//
//   - native.X — any DotExpr rooted at the `native` identifier
//   - an Ident matching a load()-imported name
//   - an Ident matching a rule-like name defined in the same file
//   - an Ident matching a previously-identified macro in the same
//     file (Phase B fixpoint — composed may be nil on the initial scan)
//
// A def whose body contains none of these is treated as a utility
// helper, not a macro. The walk descends through nested ifs, fors, and
// expression statements so a rule call wrapped in `if cond:` still
// counts.
func bodyCallsRuleLike(body []syntax.Stmt, ctx fileMacroContext, composed map[string]bool) bool {
	var found bool
	visit := func(n syntax.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fn.(type) {
		case *syntax.DotExpr:
			if base, ok := fn.X.(*syntax.Ident); ok && base.Name == "native" {
				found = true
				return false
			}
		case *syntax.Ident:
			if ctx.loaded[fn.Name] || ctx.ruleLike[fn.Name] || composed[fn.Name] {
				found = true
				return false
			}
		}
		return true
	}
	for _, stmt := range body {
		syntax.Walk(stmt, visit)
		if found {
			return true
		}
	}
	return false
}

// fixpointMacros iterates over the pending def candidates collected
// during the main scan, re-evaluating each with the growing per-file
// composed-macros set. A candidate qualifies once its body calls a
// rule-instantiating symbol — either a primitive (native.X, loaded,
// rule-bound) OR a previously-identified macro in its own file.
//
// Terminates when an iteration produces no new emissions. Bounded
// at maxFixpointIterations as a safety net; real call graphs in
// Bazel rulesets are typically 1-3 levels deep, so 8 is comfortable
// headroom.
//
// Cycles (A→B→A with neither calling a rule) terminate naturally:
// neither candidate ever gains rule-instantiating evidence, so the
// iteration produces no emissions and breaks out.
func (v *visitor) fixpointMacros() {
	const maxIterations = 8
	for range maxIterations {
		if len(v.pendingMacros) == 0 {
			return
		}
		newlyEmitted := false
		remaining := v.pendingMacros[:0]
		for _, c := range v.pendingMacros {
			fileCtx := v.macroCtxByFile[c.file]
			if bodyCallsRuleLike(c.stmt.Body, fileCtx, v.composedMacros[c.file]) {
				v.emitMacro(c)
				newlyEmitted = true
				continue
			}
			remaining = append(remaining, c)
		}
		v.pendingMacros = remaining
		if !newlyEmitted {
			return
		}
	}
}

// sortMacrosByProvenance restores source-order over the Macros slice
// after fixpoint emission may have interleaved entries from different
// files. The sort key is (file, start row) — the same ordering the
// natural single-pass scan produces, kept stable across runs and
// independent of fixpoint convergence order.
//
// Also locks in the determinism contract (see
// docs/epistemic-status.md): byte-identical input yields
// byte-identical output, with macro order keyed to source location
// rather than emission timing.
func sortMacrosByProvenance(macros []report.MacroSpec) {
	sort.SliceStable(macros, func(i, j int) bool {
		if macros[i].Provenance.File != macros[j].Provenance.File {
			return macros[i].Provenance.File < macros[j].Provenance.File
		}
		if macros[i].Provenance.StartRow != macros[j].Provenance.StartRow {
			return macros[i].Provenance.StartRow < macros[j].Provenance.StartRow
		}
		return macros[i].Name < macros[j].Name
	})
}
