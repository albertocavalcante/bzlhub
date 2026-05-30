package bzlwalk

// EPISTEMIC STATUS — DETERMINISTIC LADDER
// ---------------------------------------
// Symbol folding is deterministic at every tier. The folder either
// resolves an expression to the exact dict Starlark would have
// produced, or it returns ok=false and refuses to guess. There is no
// heuristic fallback that "tries something close."
//
// The tier ladder (see report.AttrsExtractionMethod values):
//
//	Tier 0  AttrsLiteral       — `attrs = {literal_key: attr.X(...)}` read directly.
//	Tier 1  AttrsSymbolFold    — `attrs = BASE | {...}` resolved against same-file
//	                             literal-dict bindings; recursive across `|` and `dict()`.
//	Tier 2  AttrsLoadResolve   — Tier 1 PLUS following load() statements into other
//	                             files in the same module (cycle-defended).
//	Tier 3  AttrsInterpreted   — actually executes the .bzl via starlark-go-bazel
//	                             (assay/interp). Authoritative but heavyweight.
//
// All four ladders share the determinism guarantee: if a result is
// tagged with any of these methods, it's exactly what Starlark would
// have computed. The tag is for confidence-of-effort reporting, not
// confidence-of-correctness — every tag means "exact answer," they
// differ only in how much work was required to find it.
//
// Anything the folder CAN'T statically resolve (helper calls, list
// comprehensions, conditional exprs, opaque external loads) returns
// (nil, false). Callers leave attrs empty so the UI's "dynamic
// schema" message fires — never a half-answer.

import (
	"maps"

	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/internal/syntaxutil"
	"github.com/albertocavalcante/assay/report"
)

// ─────────────────────────────────────────────────────────────────────
//  Tier-1 attrs extraction: same-file symbol folding.
//
//  Real-world .bzl files routinely build rule attrs via
//
//      attrs = BASE_ATTRS | {"extra": attr.string()}
//      attrs = dict(BASE_ATTRS, extra = attr.string())
//      attrs = A | B | {"more": attr.string()}   # chained
//
//  rather than spelling out one literal dict per rule. Tier-0
//  extractAttrs in visitor.go matches only DictExpr literals, so for
//  every shape above it returns nil — which is what produces the UI's
//  "dynamic schema, attrs not statically extractable" message.
//
//  Tier 1 fixes the gap without an interpreter: build a module-local
//  symbol table of `IDENT = literal_dict` bindings, then try to fold
//  the attrs expression by recursively resolving any operand to a
//  literal dict reachable in THIS file. We refuse — cleanly, by
//  returning (nil, false) — the moment we hit anything we can't prove
//  (helper-function calls, conditional exprs, load()-imported names,
//  list comprehensions). That preserves the determinism guarantee:
//  if we tag a result with AttrsSymbolFold the answer is exactly what
//  Starlark would have computed, not a heuristic.
// ─────────────────────────────────────────────────────────────────────

// symbolTable maps a module-level identifier to the literal DictExpr it
// was bound to. Only single-target literal-dict assignments are recorded
// — anything else (`A, B = ...`, `X += ...`, `X = fn()`, conditional
// RHS) is intentionally absent so the folder treats it as unresolvable.
type symbolTable map[string]*syntax.DictExpr

// collectSymbols scans the top-level statements of f and returns the
// bindings the folder can rely on. Order-independent: we run this once
// per file before the main scan, so dict-literal assignments visible
// BELOW the rule() call site still get used — matches Starlark's
// definition-time resolution for module-level constants.
//
// Delegates to [syntaxutil.CollectTopLevelDictBindings] so bzlwalk
// and hermetic's pinned-dict scanner share one pre-pass shape.
func collectSymbols(f *syntax.File) symbolTable {
	return symbolTable(syntaxutil.CollectTopLevelDictBindings(f))
}

// foldContext carries the per-resolution state for foldDictExpr:
// the current file's symbol table + loads, the module-wide index
// (nil if Tier-2 is disabled), the calling file's relative path
// (needed to normalize relative loads), in-flight identifier names
// (cycle defense), and files we've already crossed into (file-level
// cycle defense). crossedLoad becomes true the moment a Tier-2 hop
// occurs, so the top-level caller can pick AttrsLoadResolve over
// AttrsSymbolFold in the result tag.
type foldContext struct {
	sym         symbolTable
	loads       fileLoads
	index       *moduleSymbolIndex
	fromFile    string
	seen        map[string]bool
	seenFiles   map[string]bool
	crossedLoad bool
}

// foldAttrsExpr attempts to resolve expr to a flat (name, value-call)
// list following the same shape rules as extractAttrs: string-literal
// keys, attr.TYPE(...) call values. ok=false means "this expression
// isn't statically resolvable from same-file bindings + loaded
// constants"; the caller then leaves attrs empty (so the UI's
// existing "dynamic schema" message still fires).
//
// crossedLoad reports whether any Tier-2 (cross-file) hop happened
// during the fold — the caller uses this to decide AttrsSymbolFold
// vs AttrsLoadResolve as the result tag.
//
// Right-bias merge: in `A | B` a key present in both wins from B, matching
// Starlark's dict-union semantics. Same for `dict(A, k=v)` where the
// keyword arg shadows a same-key positional entry.
func foldAttrsExpr(expr syntax.Expr, ctx *foldContext) ([]report.AttrSpec, bool, bool) {
	merged, ok := foldDictExpr(expr, ctx)
	if !ok {
		return nil, false, ctx.crossedLoad
	}
	return dictEntriesToAttrs(merged), true, ctx.crossedLoad
}

// foldDictExpr returns the entries of expr as an ordered slice of
// (key-literal, value-expr) preserving insertion order. ctx.seen
// tracks in-flight identifier resolutions for cycle defense —
// A = B; B = A would loop without it. Insert-order matters because
// dictEntriesToAttrs emits AttrSpec in that order and the UI renders
// them top-to-bottom; stable ordering keeps diffs deterministic.
//
// Returned bool ok=false means "give up cleanly" — the caller falls
// back to the empty/dynamic state.
//
// On an Ident miss, foldDictExpr consults ctx.loads (the current
// file's load map) and ctx.index (the module-wide cache). If the
// identifier was load()-imported from a same-module file, the
// resolver follows the load and resumes folding in that file's
// scope, marking ctx.crossedLoad so the top-level caller can tag
// the result as AttrsLoadResolve.
func foldDictExpr(expr syntax.Expr, ctx *foldContext) ([]*syntax.DictEntry, bool) {
	switch n := expr.(type) {
	case *syntax.DictExpr:
		entries := make([]*syntax.DictEntry, 0, len(n.List))
		for _, e := range n.List {
			entry, ok := e.(*syntax.DictEntry)
			if !ok {
				return nil, false
			}
			entries = append(entries, entry)
		}
		return entries, true

	case *syntax.Ident:
		// Cycle guard. A = B; B = A would otherwise recurse forever.
		// In practice these cycles can't actually be bound (Starlark
		// would error at runtime), but the parser doesn't reject them.
		if ctx.seen[n.Name] {
			return nil, false
		}
		if dict, ok := ctx.sym[n.Name]; ok {
			next := *ctx
			next.seen = cloneSeen(ctx.seen)
			next.seen[n.Name] = true
			return foldDictExpr(dict, &next)
		}
		// Tier-2: same-file lookup missed; try the file's load map.
		return foldDictExprAcrossLoad(n.Name, ctx)

	case *syntax.BinaryExpr:
		// Only the `|` operator has dict-union semantics. Everything
		// else (`+`, `and`, etc.) we refuse to interpret.
		if n.Op != syntax.PIPE {
			return nil, false
		}
		left, ok := foldDictExpr(n.X, ctx)
		if !ok {
			return nil, false
		}
		right, ok := foldDictExpr(n.Y, ctx)
		if !ok {
			return nil, false
		}
		return mergeDictEntries(left, right), true

	case *syntax.CallExpr:
		// dict(X, k=v, k2=v2) — Bazel's dict constructor with merge
		// semantics. Only the bare identifier form `dict` is recognized
		// (not `something.dict` etc.).
		callee, ok := n.Fn.(*syntax.Ident)
		if !ok || callee.Name != "dict" {
			return nil, false
		}
		var base []*syntax.DictEntry
		var kwEntries []*syntax.DictEntry
		for _, arg := range n.Args {
			// Positional dict source (first arg, typically); we accept
			// any positional-shaped argument so `dict(A, B, k=v)` would
			// also fold if A and B both resolve.
			if bin, ok := arg.(*syntax.BinaryExpr); ok && bin.Op == syntax.EQ {
				// Keyword argument: key=value where key is an Ident
				// and value is the attr call.
				keyIdent, ok := bin.X.(*syntax.Ident)
				if !ok {
					return nil, false
				}
				kwEntries = append(kwEntries, &syntax.DictEntry{
					Key:   &syntax.Literal{Token: syntax.STRING, Value: keyIdent.Name},
					Value: bin.Y,
				})
				continue
			}
			pos, ok := foldDictExpr(arg, ctx)
			if !ok {
				return nil, false
			}
			base = mergeDictEntries(base, pos)
		}
		return mergeDictEntries(base, kwEntries), true
	}
	return nil, false
}

// foldDictExprAcrossLoad handles the Tier-2 path: name wasn't bound
// in the current file's symbol table; check the file's loads. If the
// name was imported from a same-module file we can read, recurse
// into that file's scope and resolve there.
//
// Recursion depth is bounded implicitly by ctx.seenFiles — we never
// re-enter a file we've already crossed into during this resolution,
// which means the chain length is capped by the number of .bzl files
// in the module.
func foldDictExprAcrossLoad(name string, ctx *foldContext) ([]*syntax.DictEntry, bool) {
	if ctx.index == nil {
		return nil, false
	}
	imp, ok := ctx.loads[name]
	if !ok {
		return nil, false
	}
	targetFile, ok := resolveLoadedFile(ctx.fromFile, imp.ModulePath)
	if !ok {
		// External load, unrecognized path form, etc. Bail cleanly.
		return nil, false
	}
	if ctx.seenFiles[targetFile] {
		return nil, false // cycle defense
	}
	targetSym, ok := ctx.index.perFile[targetFile]
	if !ok {
		return nil, false
	}
	targetDict, ok := targetSym[imp.OriginalName]
	if !ok {
		return nil, false
	}

	next := *ctx
	next.sym = targetSym
	next.loads = ctx.index.loads[targetFile]
	next.fromFile = targetFile
	next.seen = map[string]bool{imp.OriginalName: true}
	next.seenFiles = cloneSeen(ctx.seenFiles)
	next.seenFiles[targetFile] = true
	next.crossedLoad = true
	out, ok := foldDictExpr(targetDict, &next)
	ctx.crossedLoad = ctx.crossedLoad || next.crossedLoad
	return out, ok
}

// mergeDictEntries returns a | b — entries from a, then b, with b's
// keys overriding any same-key entries in a. Insert order from a is
// preserved for non-shadowed keys; new keys from b append at the end.
// Matches Starlark dict union semantics so the resulting AttrSpec
// slice mirrors what the interpreter would have produced.
func mergeDictEntries(a, b []*syntax.DictEntry) []*syntax.DictEntry {
	if len(b) == 0 {
		return a
	}
	overrides := map[string]*syntax.DictEntry{}
	for _, e := range b {
		k, ok := dictEntryKey(e)
		if !ok {
			// b has a non-literal key — give up rather than silently
			// drop it; the surrounding fold call will already have
			// bailed if that key couldn't be lifted into a string.
			continue
		}
		overrides[k] = e
	}
	used := map[string]bool{}
	out := make([]*syntax.DictEntry, 0, len(a)+len(b))
	for _, e := range a {
		k, ok := dictEntryKey(e)
		if !ok {
			out = append(out, e)
			continue
		}
		if rep, exists := overrides[k]; exists {
			out = append(out, rep)
			used[k] = true
		} else {
			out = append(out, e)
		}
	}
	for _, e := range b {
		k, ok := dictEntryKey(e)
		if !ok || used[k] {
			continue
		}
		out = append(out, e)
		used[k] = true
	}
	return out
}

// dictEntryKey lifts a string-literal key out of a DictEntry. Anything
// else (computed keys like `(prefix + "name"): ...`) is unsupported.
func dictEntryKey(e *syntax.DictEntry) (string, bool) {
	lit, ok := e.Key.(*syntax.Literal)
	if !ok {
		return "", false
	}
	s, ok := lit.Value.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// dictEntriesToAttrs reuses the same per-entry shape rules as
// Tier-0 extractAttrs — string key, attr.TYPE(...) value — and
// returns a []report.AttrSpec ready to drop into a RuleSpec.
func dictEntriesToAttrs(entries []*syntax.DictEntry) []report.AttrSpec {
	out := make([]report.AttrSpec, 0, len(entries))
	for _, e := range entries {
		key, ok := dictEntryKey(e)
		if !ok {
			continue
		}
		spec := report.AttrSpec{Name: key}
		if valCall, ok := e.Value.(*syntax.CallExpr); ok {
			spec.Type = attrTypeFromCall(valCall)
			spec.Doc = syntaxutil.StringKeywordArg(valCall, "doc")
			spec.Mandatory = syntaxutil.BoolKeywordArg(valCall, "mandatory")
			if def := syntaxutil.KeywordArg(valCall, "default"); def != nil {
				spec.Default = literalAsText(def)
			}
		}
		out = append(out, spec)
	}
	return out
}

// cloneSeen makes a shallow copy so recursive branches in a chained
// expression (`A | B`) don't accidentally share each other's in-flight
// identifier sets — each subexpression has its own resolution path.
func cloneSeen(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	maps.Copy(out, m)
	return out
}
