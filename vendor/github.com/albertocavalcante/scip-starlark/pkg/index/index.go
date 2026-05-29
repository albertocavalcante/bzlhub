// Package scipstarlark is the public API for the scip-starlark indexer.
//
// scip-starlark indexes the Starlark language (per the spec at
// https://github.com/bazelbuild/starlark/blob/master/spec.md) and emits
// SCIP protobuf. It is dialect-agnostic: Bazel, Buck2, Copybara, Tilt and
// any other Starlark embedder layer their specifics on top in separate
// repositories.
//
// Phase 0 (v0.1.0): definition-only indexing — top-level def + top-level
// assignment statements. See docs/plans/phase-0-design.md.
//
// Phase 1 (v0.2.0): adds within-file references + load() resolution.
// Function call sites, variable reads, and load()-imported names emit
// reference Occurrences. Local scopes (function bodies, list comprehensions)
// produce SCIP local symbols. Cross-module load() targets are resolved
// through Options.CrossModuleResolver, falling back to a deterministic
// placeholder ("unresolved-load <raw-target>#<symbol>") when nil.
package scipstarlark

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	scip "github.com/scip-code/scip/bindings/go/scip"
	"go.starlark.net/syntax"
)

// IndexerName is the tool name written into the SCIP Metadata.
const IndexerName = "scip-starlark"

// IndexerVersion is the tool version written into the SCIP Metadata.
const IndexerVersion = "0.2.0"

// unresolvedLoadPrefix is the placeholder scheme used for load()-imported
// references when no CrossModuleResolver is supplied. The full symbol form
// is: "unresolved-load <raw-load-target>#<symbol>". Downstream consumers
// may safely ignore or rewrite occurrences with this prefix.
const unresolvedLoadPrefix = "unresolved-load "

// Index walks rootDir, parses every Starlark file that passes
// opts.FileMatcher, and emits a SCIP index.
//
// Phase 0 emitted top-level definitions only. Phase 1 additionally emits:
//
//   - reference Occurrences for identifier reads / call sites that resolve
//     to a top-level binding;
//   - load() statements: the locally bound name receives a Definition|Import
//     occurrence in the consuming file, plus a reference Occurrence to the
//     external symbol (resolved via Options.CrossModuleResolver or a
//     placeholder);
//   - local scopes for def bodies, lambdas, and list/dict comprehension
//     binders. Names defined locally use SCIP `local <id>` symbols and stay
//     out of the global symbol space.
//
// Per-file parse errors are surfaced as Diagnostics on the corresponding
// Document; they do NOT abort the walk. The returned error is reserved
// for catastrophic failures (e.g. rootDir not readable).
func Index(rootDir string, opts Options) (*scip.Index, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("scip-starlark: resolve rootDir: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("scip-starlark: stat rootDir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scip-starlark: rootDir %q is not a directory", absRoot)
	}

	matcher := opts.FileMatcher
	if matcher == nil {
		matcher = matcherForDialect(opts.Dialect)
	}

	idx := &scip.Index{
		Metadata: &scip.Metadata{
			Version: scip.ProtocolVersion_UnspecifiedProtocolVersion,
			ToolInfo: &scip.ToolInfo{
				Name:    IndexerName,
				Version: IndexerVersion,
			},
			ProjectRoot:          "file://" + absRoot,
			TextDocumentEncoding: scip.TextEncoding_UTF8,
		},
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == absRoot {
				return err
			}
			return nil
		}
		if d.IsDir() {
			if path == absRoot {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		if !matcher(relPath) {
			return nil
		}
		doc := indexFile(path, relPath, opts)
		if doc != nil {
			idx.Documents = append(idx.Documents, doc)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scip-starlark: walk %q: %w", absRoot, walkErr)
	}
	return idx, nil
}

// parseFileOptions holds the FileOptions used by the indexer. We instantiate
// once and reuse; FileOptions is value-immutable in practice.
var parseFileOptions = syntax.LegacyFileOptions()

// indexFile parses a single Starlark source file and produces a SCIP Document.
// Parse failures are attached as Diagnostics on a single placeholder Occurrence
// at the file's start so the failure surfaces without aborting the walk.
func indexFile(absPath, relPath string, opts Options) *scip.Document {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return documentWithReadError(relPath, err)
	}
	file, err := parseFileOptions.Parse(absPath, data, 0)
	if err != nil {
		return documentWithParseError(relPath, err)
	}

	doc := &scip.Document{
		Language:         "starlark",
		RelativePath:     relPath,
		PositionEncoding: scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart,
	}
	idx := newFileIndexer(doc, relPath, opts)
	idx.run(file)
	return doc
}

// fileIndexer carries per-file state through the AST walk.
type fileIndexer struct {
	doc     *scip.Document
	relPath string
	opts    Options
	root    *scope
}

func newFileIndexer(doc *scip.Document, relPath string, opts Options) *fileIndexer {
	return &fileIndexer{
		doc:     doc,
		relPath: relPath,
		opts:    opts,
		root:    newRootScope(),
	}
}

func (fi *fileIndexer) run(file *syntax.File) {
	// Pass 1: discover all top-level bindings (def names, assignment LHS
	// idents, load()-imported names) and pre-populate the root scope, so
	// that forward references in function bodies resolve correctly.
	fi.preDeclareTopLevel(file)

	// Pass 2: emit definition + reference occurrences. This is one walk
	// over the AST with scope tracking.
	for _, stmt := range file.Stmts {
		fi.walkStmt(stmt, fi.root)
	}
}

// preDeclareTopLevel populates fi.root with the SCIP symbol for every
// top-level def, top-level assignment LHS ident, and load()-imported name.
// It does NOT emit Occurrences; those are produced in the main walk.
func (fi *fileIndexer) preDeclareTopLevel(file *syntax.File) {
	for _, stmt := range file.Stmts {
		switch s := stmt.(type) {
		case *syntax.DefStmt:
			if s.Name != nil {
				fi.root.bind(s.Name.Name, makeSymbol(fi.relPath, s.Name.Name, fi.opts))
			}
		case *syntax.AssignStmt:
			if s.Op == syntax.EQ {
				fi.collectAssignTargetsForBinding(s.LHS)
			}
		case *syntax.LoadStmt:
			// s.To holds the LOCAL binding name; s.From is the original
			// in-module name. See walkLoadStmt for the full discussion.
			for _, to := range s.To {
				if to == nil {
					continue
				}
				fi.root.bind(to.Name, makeSymbol(fi.relPath, to.Name, fi.opts))
			}
		}
	}
}

func (fi *fileIndexer) collectAssignTargetsForBinding(lhs syntax.Expr) {
	switch e := lhs.(type) {
	case *syntax.Ident:
		fi.root.bind(e.Name, makeSymbol(fi.relPath, e.Name, fi.opts))
	case *syntax.TupleExpr:
		for _, elem := range e.List {
			if id, ok := elem.(*syntax.Ident); ok {
				fi.root.bind(id.Name, makeSymbol(fi.relPath, id.Name, fi.opts))
			}
		}
	case *syntax.ParenExpr:
		fi.collectAssignTargetsForBinding(e.X)
	}
}

// walkStmt emits Occurrences for stmt under sc.
func (fi *fileIndexer) walkStmt(stmt syntax.Stmt, sc *scope) {
	if stmt == nil {
		return
	}
	switch s := stmt.(type) {
	case *syntax.DefStmt:
		fi.walkDefStmt(s, sc)
	case *syntax.AssignStmt:
		fi.walkAssignStmt(s, sc)
	case *syntax.LoadStmt:
		fi.walkLoadStmt(s, sc)
	case *syntax.ExprStmt:
		fi.walkExpr(s.X, sc)
	case *syntax.IfStmt:
		fi.walkExpr(s.Cond, sc)
		for _, st := range s.True {
			fi.walkStmt(st, sc)
		}
		for _, st := range s.False {
			fi.walkStmt(st, sc)
		}
	case *syntax.ForStmt:
		// Per Starlark spec, `for` does NOT introduce a new scope: the
		// loop variable is bound in the enclosing scope. We still need
		// to bind the loop variable for subsequent references.
		fi.walkExpr(s.X, sc)
		fi.bindAssignTargets(s.Vars, sc)
		for _, st := range s.Body {
			fi.walkStmt(st, sc)
		}
	case *syntax.WhileStmt:
		fi.walkExpr(s.Cond, sc)
		for _, st := range s.Body {
			fi.walkStmt(st, sc)
		}
	case *syntax.ReturnStmt:
		if s.Result != nil {
			fi.walkExpr(s.Result, sc)
		}
	case *syntax.BranchStmt:
		// break/continue/pass: no expressions, no bindings.
	}
}

func (fi *fileIndexer) walkDefStmt(s *syntax.DefStmt, sc *scope) {
	if s.Name == nil {
		return
	}
	// If this def is top-level (sc is root), emit a Definition occurrence +
	// SymbolInformation. Nested defs are not indexed as definitions in
	// Phase 1 — they would need a local-symbol scheme. Phase 2 territory.
	if sc == fi.root {
		sym := fi.root.lookup(s.Name.Name)
		if sym == "" {
			// Shouldn't happen: preDeclareTopLevel runs first.
			sym = makeSymbol(fi.relPath, s.Name.Name, fi.opts)
			fi.root.bind(s.Name.Name, sym)
		}
		fi.emitSymbolInfo(sym, s.Name.Name, scip.SymbolInformation_Function)
		fi.emitOccurrence(s.Name, sym, int32(scip.SymbolRole_Definition))
	}

	// Function body: push a new scope; bind parameters as local symbols.
	body := sc.push()
	for _, p := range s.Params {
		fi.bindParam(p, body)
	}
	for _, st := range s.Body {
		fi.walkStmt(st, body)
	}
}

// bindParam binds a single function parameter into body. Supports
//
//	ident                                  -> plain positional
//	BinaryExpr{Op:EQ, X:ident, Y:default}  -> keyword with default
//	UnaryExpr{Op:STAR, X:nil}              -> bare `*` separator (no name)
//	UnaryExpr{Op:STAR, X:ident}            -> `*args`
//	UnaryExpr{Op:STARSTAR, X:ident}        -> `**kwargs`
func (fi *fileIndexer) bindParam(p syntax.Expr, body *scope) {
	switch e := p.(type) {
	case *syntax.Ident:
		fi.bindAndEmitLocal(e, body)
	case *syntax.BinaryExpr:
		if e.Op == syntax.EQ {
			if id, ok := e.X.(*syntax.Ident); ok {
				fi.bindAndEmitLocal(id, body)
			}
			// Walk the default-value expression under the enclosing
			// (parent) scope — Starlark evaluates defaults at def time.
			if body.parent != nil {
				fi.walkExpr(e.Y, body.parent)
			} else {
				fi.walkExpr(e.Y, body)
			}
		}
	case *syntax.UnaryExpr:
		if e.X != nil {
			if id, ok := e.X.(*syntax.Ident); ok {
				fi.bindAndEmitLocal(id, body)
			}
		}
		// Bare `*` separator: no binding.
	}
}

// bindAndEmitLocal allocates a fresh local symbol for id, binds it in sc,
// emits SymbolInformation, and emits a Definition occurrence.
func (fi *fileIndexer) bindAndEmitLocal(id *syntax.Ident, sc *scope) {
	sym := fi.allocLocalSymbol(id.Name)
	sc.bind(id.Name, sym)
	fi.emitSymbolInfo(sym, id.Name, scip.SymbolInformation_Parameter)
	fi.emitOccurrence(id, sym, int32(scip.SymbolRole_Definition))
}

// allocLocalSymbol returns a fresh SCIP local symbol unique within the file.
// Format: "local <relpath-escaped>$<sequence>$<name>"
func (fi *fileIndexer) allocLocalSymbol(name string) string {
	id := fi.root.nextLocalID()
	return "local " + escapeLocalID(fmt.Sprintf("%s$%d$%s", fi.relPath, id, name))
}

func (fi *fileIndexer) walkAssignStmt(s *syntax.AssignStmt, sc *scope) {
	// Walk RHS first so any uses of pre-existing bindings resolve before
	// the LHS rebinds them.
	fi.walkExpr(s.RHS, sc)
	if s.Op != syntax.EQ {
		// Augmented assignment: LHS is a read of an existing binding.
		fi.walkExpr(s.LHS, sc)
		return
	}
	if sc == fi.root {
		// Top-level: emit Definition occurrences + SymbolInformation.
		fi.emitTopLevelAssignTargets(s.LHS)
	} else {
		// Inside a function/comprehension body: introduce locals.
		fi.bindAssignTargets(s.LHS, sc)
	}
}

func (fi *fileIndexer) emitTopLevelAssignTargets(lhs syntax.Expr) {
	switch e := lhs.(type) {
	case *syntax.Ident:
		sym := fi.root.lookup(e.Name)
		if sym == "" {
			sym = makeSymbol(fi.relPath, e.Name, fi.opts)
			fi.root.bind(e.Name, sym)
		}
		fi.emitSymbolInfo(sym, e.Name, scip.SymbolInformation_Variable)
		fi.emitOccurrence(e, sym, int32(scip.SymbolRole_Definition))
	case *syntax.TupleExpr:
		for _, elem := range e.List {
			if id, ok := elem.(*syntax.Ident); ok {
				fi.emitTopLevelAssignTargets(id)
			}
		}
	case *syntax.ParenExpr:
		fi.emitTopLevelAssignTargets(e.X)
	}
}

// bindAssignTargets introduces local bindings (and emits Definition
// occurrences) for the LHS of an assignment or `for` statement inside a
// non-root scope.
func (fi *fileIndexer) bindAssignTargets(lhs syntax.Expr, sc *scope) {
	switch e := lhs.(type) {
	case *syntax.Ident:
		// If this name is already bound locally, treat as a write to the
		// existing binding (still ReadAccess-shaped here for simplicity,
		// but a true second def-occurrence is fine semantically).
		if existing := sc.lookup(e.Name); existing != "" && isLocalSCIPSymbol(existing) {
			fi.emitOccurrence(e, existing, int32(scip.SymbolRole_WriteAccess))
			return
		}
		fi.bindAndEmitLocal(e, sc)
	case *syntax.TupleExpr:
		for _, elem := range e.List {
			fi.bindAssignTargets(elem, sc)
		}
	case *syntax.ListExpr:
		for _, elem := range e.List {
			fi.bindAssignTargets(elem, sc)
		}
	case *syntax.ParenExpr:
		fi.bindAssignTargets(e.X, sc)
	case *syntax.UnaryExpr:
		// `*rest` form in unpacking.
		if e.X != nil {
			fi.bindAssignTargets(e.X, sc)
		}
	default:
		// Indexed / dotted LHS (`x[0] = ...`, `x.y = ...`): treat as a
		// read of the container.
		fi.walkExpr(lhs, sc)
	}
}

func (fi *fileIndexer) walkLoadStmt(s *syntax.LoadStmt, sc *scope) {
	if s.Module == nil {
		return
	}
	raw, _ := s.Module.Value.(string)
	if raw == "" {
		return
	}
	path, _ := splitLoadTarget(raw)

	// Per go.starlark.net's parser: s.To holds the LOCAL binding name
	// (i.e. the name introduced into this file), and s.From holds the
	// ORIGINAL name in the loaded module. For the plain form
	// `load("//x:y.bzl", "helper")` they point at the same *Ident (the
	// position just past the opening quote); for the aliased form
	// `load("//x:y.bzl", aliased = "real")` To is the bare identifier and
	// From is a synthetic ident at the string-literal position.
	for i, to := range s.To {
		if to == nil {
			continue
		}
		var from *syntax.Ident
		if i < len(s.From) {
			from = s.From[i]
		}
		sourceName := to.Name
		if from != nil && from.Name != "" {
			sourceName = from.Name
		}

		// 1) Emit the local binding occurrence: Definition|Import at the
		//    To ident's position. Top-level so it gets a file-scoped
		//    symbol (or local if underscore-prefixed).
		var localSym string
		if sc == fi.root {
			localSym = fi.root.lookup(to.Name)
			if localSym == "" {
				localSym = makeSymbol(fi.relPath, to.Name, fi.opts)
				fi.root.bind(to.Name, localSym)
			}
		} else {
			localSym = fi.allocLocalSymbol(to.Name)
			sc.bind(to.Name, localSym)
		}
		fi.emitSymbolInfo(localSym, to.Name, scip.SymbolInformation_Variable)
		fi.emitOccurrence(to, localSym, int32(scip.SymbolRole_Definition)|int32(scip.SymbolRole_Import))

		// 2) Emit a reference Occurrence to the EXTERNAL symbol. Resolve
		//    via CrossModuleResolver if set; else fall back to a
		//    deterministic placeholder.
		target := LoadTarget{Raw: raw, Path: path, Symbol: sourceName}
		externSym := ""
		if fi.opts.CrossModuleResolver != nil {
			externSym = fi.opts.CrossModuleResolver(target)
		}
		if externSym == "" {
			externSym = unresolvedLoadPrefix + raw + "#" + sourceName
		}
		// Place the external-reference occurrence at the From ident
		// (which the parser positions at the original-name string
		// literal). It points to the imported symbol's home.
		externIdent := from
		if externIdent == nil {
			externIdent = to
		}
		fi.emitOccurrence(externIdent, externSym, int32(scip.SymbolRole_ReadAccess)|int32(scip.SymbolRole_Import))
	}
}

// walkExpr walks expr emitting reference Occurrences. For identifier reads
// that resolve in sc, a ReadAccess Occurrence is produced.
func (fi *fileIndexer) walkExpr(expr syntax.Expr, sc *scope) {
	if expr == nil {
		return
	}
	switch e := expr.(type) {
	case *syntax.Ident:
		sym := sc.lookup(e.Name)
		if sym != "" {
			fi.emitOccurrence(e, sym, int32(scip.SymbolRole_ReadAccess))
		}
		// Unresolved free reference: silently skip (per design).
	case *syntax.Literal:
		// no-op
	case *syntax.CallExpr:
		fi.walkExpr(e.Fn, sc)
		for _, a := range e.Args {
			// Keyword argument shape: BinaryExpr{Op:EQ, X:ident, Y:expr}.
			// The X (parameter name) is NOT a reference to a local symbol;
			// skip it but walk the value.
			if b, ok := a.(*syntax.BinaryExpr); ok && b.Op == syntax.EQ {
				if _, isIdent := b.X.(*syntax.Ident); isIdent {
					fi.walkExpr(b.Y, sc)
					continue
				}
			}
			fi.walkExpr(a, sc)
		}
	case *syntax.DotExpr:
		fi.walkExpr(e.X, sc)
		// e.Name is a field/method selector — not a reference to a
		// resolvable binding in this file. Skip.
	case *syntax.IndexExpr:
		fi.walkExpr(e.X, sc)
		fi.walkExpr(e.Y, sc)
	case *syntax.SliceExpr:
		fi.walkExpr(e.X, sc)
		fi.walkExpr(e.Lo, sc)
		fi.walkExpr(e.Hi, sc)
		fi.walkExpr(e.Step, sc)
	case *syntax.BinaryExpr:
		fi.walkExpr(e.X, sc)
		fi.walkExpr(e.Y, sc)
	case *syntax.UnaryExpr:
		fi.walkExpr(e.X, sc)
	case *syntax.CondExpr:
		fi.walkExpr(e.Cond, sc)
		fi.walkExpr(e.True, sc)
		fi.walkExpr(e.False, sc)
	case *syntax.ParenExpr:
		fi.walkExpr(e.X, sc)
	case *syntax.TupleExpr:
		for _, x := range e.List {
			fi.walkExpr(x, sc)
		}
	case *syntax.ListExpr:
		for _, x := range e.List {
			fi.walkExpr(x, sc)
		}
	case *syntax.DictExpr:
		for _, x := range e.List {
			fi.walkExpr(x, sc)
		}
	case *syntax.DictEntry:
		fi.walkExpr(e.Key, sc)
		fi.walkExpr(e.Value, sc)
	case *syntax.LambdaExpr:
		body := sc.push()
		for _, p := range e.Params {
			fi.bindParam(p, body)
		}
		fi.walkExpr(e.Body, body)
	case *syntax.Comprehension:
		// List/dict comprehension introduces its own scope for the
		// iteration variables. Walk clauses left-to-right, binding then
		// using.
		comp := sc.push()
		for _, c := range e.Clauses {
			switch cl := c.(type) {
			case *syntax.ForClause:
				fi.walkExpr(cl.X, comp)
				fi.bindAssignTargets(cl.Vars, comp)
			case *syntax.IfClause:
				fi.walkExpr(cl.Cond, comp)
			}
		}
		fi.walkExpr(e.Body, comp)
	}
}

// emitOccurrence appends an Occurrence pointing at id's source range.
func (fi *fileIndexer) emitOccurrence(id *syntax.Ident, sym string, roles int32) {
	fi.doc.Occurrences = append(fi.doc.Occurrences, &scip.Occurrence{
		Range:       identRange(id),
		Symbol:      sym,
		SymbolRoles: roles,
	})
}

// emitSymbolInfo appends SymbolInformation if sym is not already present.
// Duplicates would be valid SCIP but waste bytes.
func (fi *fileIndexer) emitSymbolInfo(sym, displayName string, kind scip.SymbolInformation_Kind) {
	for _, s := range fi.doc.Symbols {
		if s.Symbol == sym {
			return
		}
	}
	fi.doc.Symbols = append(fi.doc.Symbols, &scip.SymbolInformation{
		Symbol:      sym,
		DisplayName: displayName,
		Kind:        kind,
	})
}

// splitLoadTarget parses a load() module string into (path, file). The
// historical Bazel form is "@repo//pkg:file.bzl"; we split on the LAST ':'
// so callers can recover the bzl filename. Generic / non-Bazel forms (e.g.
// Tilt's relative paths) fall through with path == "" and file == raw.
//
// This function is intentionally syntax-only; it has no Bazel knowledge.
// We use the same colon convention that Starlark's load() examples use.
func splitLoadTarget(raw string) (path, file string) {
	idx := strings.LastIndex(raw, ":")
	if idx < 0 {
		return "", raw
	}
	return raw[:idx], raw[idx+1:]
}

// identRange returns a SCIP 4-element [startLine, startCol, endLine, endCol]
// range for an Ident. Starlark Position.Line is 1-based and Position.Col is
// a 1-based rune column, which matches the Document's
// UTF32CodeUnitOffsetFromLineStart encoding (rune = UTF-32 code unit) once
// converted to 0-based.
func identRange(id *syntax.Ident) []int32 {
	start, end := id.Span()
	return []int32{
		zeroBased(start.Line),
		zeroBased(start.Col),
		zeroBased(end.Line),
		zeroBased(end.Col),
	}
}

func zeroBased(n int32) int32 {
	if n <= 0 {
		return 0
	}
	return n - 1
}

// makeSymbol formats a SCIP symbol string for a top-level name.
//
// Format follows docs/plans/phase-0-design.md:
//
//	Default                : "starlark <relpath>#<name>"
//	With SymbolPrefix "P"  : "P <relpath>#<name>"
//
// Underscore-prefixed names (Starlark's convention for module-private)
// are emitted as SCIP local symbols ("local <id>") because SCIP has no
// visibility flag on SymbolInformation. This keeps them out of the global
// symbol space while still indexing them for in-file navigation. See
// docs/plans/phase-0-design.md test case #7.
func makeSymbol(relPath, name string, opts Options) string {
	if strings.HasPrefix(name, "_") {
		return "local " + escapeLocalID(relPath+":"+name)
	}
	prefix := opts.SymbolPrefix
	if prefix == "" {
		prefix = "starlark"
	}
	return prefix + " " + relPath + "#" + name
}

// isLocalSCIPSymbol reports whether sym begins with the SCIP local prefix.
// We avoid the dependency on scip.IsLocalSymbol here so the predicate stays
// cheap inside hot AST loops.
func isLocalSCIPSymbol(sym string) bool {
	return strings.HasPrefix(sym, "local ")
}

// escapeLocalID maps a free-form string to a SCIP-safe local identifier.
// SCIP's <local-id> ::= (<identifier-character>)+ where
// identifier-character is '_' | '+' | '-' | '$' | ASCII letter/digit.
// Everything else becomes '$'.
func escapeLocalID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '_' || r == '+' || r == '-' || r == '$':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('$')
		}
	}
	return b.String()
}

// documentWithParseError returns a Document whose only content is a single
// Occurrence at file start tagged with a parse-error Diagnostic.
func documentWithParseError(relPath string, err error) *scip.Document {
	return &scip.Document{
		Language:         "starlark",
		RelativePath:     relPath,
		PositionEncoding: scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart,
		Occurrences: []*scip.Occurrence{
			{
				Range: []int32{0, 0, 0, 0},
				Diagnostics: []*scip.Diagnostic{
					{
						Severity: scip.Severity_Error,
						Code:     "parse-error",
						Message:  err.Error(),
						Source:   IndexerName,
					},
				},
			},
		},
	}
}

func documentWithReadError(relPath string, err error) *scip.Document {
	return &scip.Document{
		Language:         "starlark",
		RelativePath:     relPath,
		PositionEncoding: scip.PositionEncoding_UTF32CodeUnitOffsetFromLineStart,
		Occurrences: []*scip.Occurrence{
			{
				Range: []int32{0, 0, 0, 0},
				Diagnostics: []*scip.Diagnostic{
					{
						Severity: scip.Severity_Error,
						Code:     "read-error",
						Message:  err.Error(),
						Source:   IndexerName,
					},
				},
			},
		},
	}
}

// Options configures an Index run. All fields are optional; zero values
// pick sensible defaults driven by Dialect.
type Options struct {
	// FileMatcher decides which files (by path relative to rootDir) get
	// indexed. When nil, the Dialect preset's matcher is used.
	FileMatcher func(relPath string) bool

	// Dialect selects a file-matcher + builtin-symbol preset.
	// One of: "bazel", "buck2", "copybara", "tilt", "plain".
	// Empty string is treated as "plain".
	Dialect string

	// BuiltinSymbols maps dialect builtin names (e.g. "rule", "glob") to
	// fully-qualified SCIP symbols that a downstream tool wants references
	// to attach to. Reserved for a future phase.
	BuiltinSymbols map[string]string

	// CrossModuleResolver resolves a load() target to a canonical SCIP
	// symbol in another module. If nil (or it returns ""), the indexer
	// emits a deterministic placeholder symbol of the form:
	//
	//	"unresolved-load <raw-load-target>#<symbol>"
	//
	// e.g. "unresolved-load @rules_python//python:defs.bzl#py_library".
	// Downstream consumers may rewrite or ignore unresolved references.
	CrossModuleResolver func(target LoadTarget) string

	// SymbolPrefix is prepended to every emitted symbol. Lets consumers
	// namespace symbols by package or registry coordinate
	// (e.g. "bzlmod rules_python@0.40.0").
	SymbolPrefix string
}

// LoadTarget describes a Starlark load() statement, parsed but not resolved.
//
// Resolution is the consumer's responsibility via Options.CrossModuleResolver.
type LoadTarget struct {
	// Raw is the original first argument to load(), verbatim
	// (e.g. "@rules_python//python:defs.bzl").
	Raw string
	// Path is the portion of Raw before the LAST ':' separator, when
	// present (e.g. "@rules_python//python:defs.bzl" ->
	// "@rules_python//python:defs.bzl"... wait, actually -> "@rules_python//python").
	// For inputs without a ':' (rare; e.g. some Tilt-style relative
	// includes) Path is "".
	//
	// This is a syntactic split only; scip-starlark applies NO Bazel,
	// Buck2 or other dialect interpretation to load targets.
	Path string
	// Symbol is the name being imported out of the load target — i.e. the
	// SECOND positional argument to load() (the "loaded module" name),
	// not the consuming file's local binding name. For
	// `load("//x:y.bzl", aliased = "real")`, Symbol == "real".
	Symbol string
}
