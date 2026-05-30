// Package bzlwalk parses .bzl and BUILD files and extracts rule, provider,
// macro, repository-rule, and module-extension definitions using static AST
// analysis.
//
// The analysis is dialect-aware via the [dialect.Dialect] interface. AST
// pattern-matching catches literal and idiomatic forms (Tier 0), same-file
// dict-union and `dict()` folds (Tier 1), and cross-file `load()`-resolved
// references (Tier 2). Modules that construct rule attributes via helper
// function calls (e.g. `attrs = make_attrs()`) need the Tier-3 interpreter
// fallback in assay/interp, exposed via [assay.WithInterpreterFallback].
//
// Entry points:
//
//   - [Walk] does the whole pipeline given a directory: walkparse + WalkParsed.
//     Convenient for standalone use; the package's tests use this form.
//   - [WalkParsed] consumes a pre-parsed file slice and is what
//     [assay.Analyze] calls so the same parsed ASTs flow into both
//     bzlwalk and the hermeticity classifier (single parse per file).
package bzlwalk

import (
	"context"
	"fmt"
	"os"

	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/dialect"
	"github.com/albertocavalcante/assay/internal/syntaxutil"
	"github.com/albertocavalcante/assay/internal/walkparse"
	"github.com/albertocavalcante/assay/report"
)

// WalkOption configures Walk.
type WalkOption func(*walkOptions)

type walkOptions struct {
	onFileError func(path string, err error)
}

// WithFileErrorHandler routes per-file parse errors to fn instead of writing
// them to stderr. Walk continues past the failed file in either case.
func WithFileErrorHandler(fn func(path string, err error)) WalkOption {
	return func(o *walkOptions) { o.onFileError = fn }
}

// Walk traverses rootDir, parses every .bzl / BUILD / MODULE.bazel file,
// and accumulates findings into a *report.ModuleReport.
//
// Standalone convenience wrapper — it invokes walkparse.Walk to do the
// single shared walk + parse, then delegates to WalkParsed for the
// actual extraction. assay.Analyze calls WalkParsed directly with a
// file slice shared between bzlwalk and hermetic so each file is
// parsed exactly once per Analyze().
//
// Per-file Starlark parse errors are logged (to stderr by default, or
// to a handler installed via WithFileErrorHandler) and the walk
// continues. Only ctx cancellation or filesystem-traversal errors
// abort.
func Walk(ctx context.Context, rootDir string, d dialect.Dialect, r *report.ModuleReport, opts ...WalkOption) error {
	files, err := walkparse.Walk(ctx, rootDir)
	if err != nil {
		return err
	}
	return WalkParsed(ctx, d, files, r, opts...)
}

// WalkParsed runs the extraction over a pre-parsed file slice
// (typically produced by walkparse.Walk). The function does not touch
// the filesystem and does not re-parse anything — both the per-file
// symbol-index pre-pass and the main extraction pass iterate the
// same in-memory ASTs.
//
// Use this entry point when orchestrating bzlwalk + hermetic together
// (assay.Analyze does so) — passing the same file slice to both
// eliminates the historical double-parse.
func WalkParsed(ctx context.Context, d dialect.Dialect, files []walkparse.File, r *report.ModuleReport, opts ...WalkOption) error {
	o := walkOptions{}
	for _, opt := range opts {
		opt(&o)
	}
	if o.onFileError == nil {
		o.onFileError = func(path string, err error) {
			fmt.Fprintf(os.Stderr, "bzlwalk: skipping %s: %v\n", path, err)
		}
	}

	// Pre-pass: per-file symbol bindings + loads for the Tier-2
	// resolver. Reads parsed ASTs straight from the input slice — no
	// re-walk, no re-parse.
	idx := buildModuleSymbolIndexFromFiles(files)

	v := &visitor{
		dialect:        d,
		report:         r,
		onFileError:    o.onFileError,
		moduleIndex:    idx,
		macroCtxByFile: map[string]fileMacroContext{},
		composedMacros: map[string]map[string]bool{},
	}
	for _, f := range files {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		v.consumeFile(f)
	}
	v.fixpointMacros()
	sortMacrosByProvenance(r.Macros)
	return nil
}

type visitor struct {
	dialect     dialect.Dialect
	report      *report.ModuleReport
	onFileError func(path string, err error)

	// symbols holds the module-level `IDENT = literal_dict` bindings
	// in the file currently being scanned. Repopulated per file in
	// scanFile.
	symbols symbolTable

	// macroCtx holds the per-file set of rule-like + load()-imported
	// names a top-level def can call to qualify as a macro (rather
	// than a utility helper). Repopulated per file in scanFile.
	macroCtx fileMacroContext

	// loads holds the per-file `load()` bindings used by the Tier-2
	// resolver to follow cross-file references. Repopulated per file
	// in scanFile. Empty when the file has no top-level load()s.
	loads fileLoads

	// currentFile is the module-relative path of the file currently
	// being scanned. Needed to normalize same-package load paths
	// (`:foo.bzl`) against the caller's directory.
	currentFile string

	// moduleIndex is the module-wide pre-pass cache that lets
	// foldDictExpr follow loads into other files. Nil disables the
	// Tier-2 path cleanly (every cross-file lookup returns ok=false).
	moduleIndex *moduleSymbolIndex

	// macroCtxByFile saves the per-file macro context so the Phase B
	// fixpoint can re-evaluate pending def candidates with the
	// composed-macros set added without re-parsing each file.
	macroCtxByFile map[string]fileMacroContext

	// composedMacros holds the per-file set of def names already
	// identified as macros. The fixpoint augments this set; bodyCallsRuleLike
	// reads it via the third argument so a def calling another same-
	// file macro qualifies after the first round identifies that macro.
	composedMacros map[string]map[string]bool

	// pendingMacros are def candidates that passed the
	// exported / not-ctx-impl / not-test-path filters but didn't
	// (yet) call a rule-instantiating symbol. The fixpoint phase
	// re-evaluates these with the growing composedMacros set per
	// file. Each candidate carries everything needed to emit the
	// macro without revisiting the AST node.
	pendingMacros []pendingMacroCandidate
}

// pendingMacroCandidate is a def that may yet be classified as a macro
// once the fixpoint adds more identified-macro names to its file's
// context. Kept as a struct rather than a *DefStmt + lookup so the
// emit path doesn't have to re-derive params / doc / provenance.
type pendingMacroCandidate struct {
	file       string
	stmt       *syntax.DefStmt
	name       string
	params     []string
	doc        string
	provenance report.Provenance
}

// consumeFile is the per-file step over a pre-parsed walkparse.File.
// Records the file in the inventory, surfaces parse errors via the
// configured handler, and dispatches to scanFile for .bzl + BUILD
// ASTs. MODULE.bazel files are inventoried but not scanned (handled
// by the modulefile package).
func (v *visitor) consumeFile(f walkparse.File) {
	v.report.FileInventory = append(v.report.FileInventory, report.FileEntry{
		Path: f.Path,
		Size: f.Size,
		Kind: f.Kind,
	})
	if f.Kind != "bzl" && f.Kind != "build" {
		return
	}
	if f.ParseErr != nil {
		v.onFileError(f.Path, f.ParseErr)
		return
	}
	if f.AST == nil {
		return
	}
	v.scanFile(f.AST, f.Path)
}

// scanFile walks the top-level statements of a parsed Starlark file and
// extracts findings. Before the main walk, we build the file's
// symbol table so the Tier-1 symbol-fold path (see symbolfold.go) can
// resolve attrs expressions like `BASE | {...}` against module-local
// bindings discovered ANYWHERE in this file — order-independent,
// matching Starlark's module-scope resolution.
func (v *visitor) scanFile(f *syntax.File, relPath string) {
	v.symbols = collectSymbols(f)
	v.macroCtx = collectMacroContext(f, v.dialect)
	v.loads = syntaxutil.CollectLoads(f)
	// relPath already comes in slash-form from walkparse.File.Path.
	v.currentFile = relPath
	// Persist per-file state so the Phase B fixpoint can re-evaluate
	// pending def candidates with the composed-macros set without
	// redoing collection or re-parsing the file.
	v.macroCtxByFile[relPath] = v.macroCtx
	if _, exists := v.composedMacros[relPath]; !exists {
		v.composedMacros[relPath] = map[string]bool{}
	}
	for _, stmt := range f.Stmts {
		switch s := stmt.(type) {
		case *syntax.AssignStmt:
			// Patterns like `my_rule = rule(...)`, `MyInfo = provider(...)`,
			// `_my_repo = repository_rule(...)`, etc.
			v.scanAssign(s, relPath)
		case *syntax.DefStmt:
			// Top-level def: maybe a macro (if exported) or an impl function.
			v.scanDef(s, relPath)
		case *syntax.ExprStmt:
			// Top-level call without assignment — toolchain_type(), etc.
			if call, ok := s.X.(*syntax.CallExpr); ok {
				v.scanTopLevelCall(call, relPath)
			}
		}
	}
}
