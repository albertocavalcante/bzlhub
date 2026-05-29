// Package scipbazel is the public API for the scip-bazel indexer.
//
// scip-bazel is a thin Bazel-flavored layer over the dialect-agnostic
// scip-starlark indexer. It delegates parsing to scip-starlark, then
// enriches the resulting SCIP index with Bazel semantic tags so that
// downstream consumers (e.g. canopy) can render rules, providers,
// aspects, repository rules, module extensions, and macros without
// re-parsing the source.
//
// scip-bazel never re-implements Starlark parsing. Bazel-specific
// knowledge lives here; language-level concerns stay in scip-starlark.
//
// Phase 0 (v0.1.0): post-process annotation of top-level definitions.
// See docs/plans/phase-0-design.md.
package scipbazel

import (
	"fmt"
	"os"
	"path/filepath"

	scipstarlark "github.com/albertocavalcante/scip-starlark/pkg/index"
	scip "github.com/scip-code/scip/bindings/go/scip"
	"go.starlark.net/syntax"
)

// IndexerName is the tool name written into SCIP metadata after we
// re-stamp the index. Callers that need the underlying language
// indexer name can still inspect scipstarlark.IndexerName.
const IndexerName = "scip-bazel"

// IndexerVersion is the tool version written into SCIP metadata.
// Bumped on every release; downstream consumers can read this off
// produced indexes to feature-flag against scip-bazel behaviors.
// Keep in lockstep with the git tag at release time.
const IndexerVersion = "0.2.0"

// LoadTarget mirrors scip-starlark's LoadTarget. Re-exported so
// consumers that integrate with scip-bazel don't need to import
// scip-starlark just for this type.
type LoadTarget = scipstarlark.LoadTarget

// Options configures an Index run. Zero value is a sensible default
// (Bazel dialect file-matcher, no symbol prefix, no resolver).
type Options struct {
	// FileMatcher overrides scip-starlark's bazel-preset matcher.
	// Most callers leave this empty.
	FileMatcher func(relPath string) bool

	// CrossModuleResolver, when set, is forwarded to scip-starlark for
	// load() resolution. Phase 0 of scip-starlark does not yet exercise
	// the resolver; the parameter is wired so consumers can pass it
	// through without changing their API later.
	CrossModuleResolver func(target LoadTarget) string

	// SymbolPrefix is forwarded to scip-starlark. Consumers that pin
	// symbols to a (module, version) coordinate (e.g. canopy) supply
	// this; the default is empty (scip-starlark falls back to the
	// "starlark" prefix).
	SymbolPrefix string
}

// Index runs scip-starlark with the Bazel dialect preset and then
// annotates top-level symbols with Bazel-specific descriptors (rule,
// provider, aspect, repository_rule, module_extension, macro). The
// returned *scip.Index has its tool metadata re-stamped to scip-bazel.
//
// Per-file parse errors from scip-starlark are preserved as
// Diagnostics; they do not abort the walk. A nil error from this
// function means the rootDir was walkable; individual documents may
// still carry diagnostics.
func Index(rootDir string, opts Options) (*scip.Index, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("scip-bazel: resolve rootDir: %w", err)
	}

	starlarkOpts := scipstarlark.Options{
		FileMatcher:         opts.FileMatcher,
		Dialect:             "bazel",
		CrossModuleResolver: opts.CrossModuleResolver,
		SymbolPrefix:        opts.SymbolPrefix,
	}

	idx, err := scipstarlark.Index(absRoot, starlarkOpts)
	if err != nil {
		return nil, fmt.Errorf("scip-bazel: %w", err)
	}

	// Re-stamp tool metadata so consumers know the index is annotated.
	if idx.Metadata != nil {
		idx.Metadata.ToolInfo = &scip.ToolInfo{
			Name:    IndexerName,
			Version: IndexerVersion,
		}
	}

	for _, doc := range idx.Documents {
		annotateDocument(absRoot, doc)
	}
	return idx, nil
}

// annotateDocument re-parses the source file referenced by doc and
// applies Bazel annotations to each SymbolInformation whose top-level
// definition matches a known Bazel concept. Unparseable files leave
// the document's symbols untouched.
func annotateDocument(absRoot string, doc *scip.Document) {
	if doc == nil || len(doc.Symbols) == 0 {
		return
	}
	absPath := filepath.Join(absRoot, filepath.FromSlash(doc.RelativePath))
	data, err := os.ReadFile(absPath)
	if err != nil {
		return
	}
	// Use FileOptions.Parse (the post-1.0 go.starlark.net API) rather
	// than the deprecated package-level syntax.Parse. Default options
	// are good for Bazel-flavored Starlark — we just want the AST.
	var opts syntax.FileOptions
	file, err := opts.Parse(absPath, data, 0)
	if err != nil {
		return
	}
	concepts := collectConcepts(file)
	if len(concepts) == 0 {
		return
	}
	for _, sym := range doc.Symbols {
		concept, ok := concepts[sym.DisplayName]
		if !ok {
			continue
		}
		applyConcept(sym, concept)
	}
}

// bazelConcept enumerates the Bazel definition forms scip-bazel
// recognises at top level.
type bazelConcept int

const (
	conceptNone bazelConcept = iota
	conceptRule
	conceptProvider
	conceptAspect
	conceptRepositoryRule
	conceptModuleExtension
	conceptMacro
)

// collectConcepts walks the top-level statements of file and returns a
// map from defined name to the Bazel concept it represents. Names that
// don't match a known form are omitted.
func collectConcepts(file *syntax.File) map[string]bazelConcept {
	out := map[string]bazelConcept{}
	for _, stmt := range file.Stmts {
		switch s := stmt.(type) {
		case *syntax.DefStmt:
			if s.Name == nil {
				continue
			}
			name := s.Name.Name
			// Underscore-prefixed top-level defs are Starlark's
			// convention for module-private helpers (rule/aspect
			// implementations, internal utilities). Bazel macros are
			// conventionally exported, so we don't annotate the
			// private ones to keep counts and UI hints accurate.
			if len(name) > 0 && name[0] == '_' {
				continue
			}
			out[name] = conceptMacro
		case *syntax.AssignStmt:
			if s.Op != syntax.EQ {
				continue
			}
			c := conceptFromCallExpr(s.RHS)
			if c == conceptNone {
				continue
			}
			// Apply the concept to every identifier on the LHS.
			for _, name := range lhsNames(s.LHS) {
				out[name] = c
			}
		}
	}
	return out
}

// conceptFromCallExpr inspects an expression and, if it's a call whose
// callee is a recognised Bazel constructor identifier, returns the
// matching concept. Anything else returns conceptNone.
func conceptFromCallExpr(expr syntax.Expr) bazelConcept {
	call, ok := expr.(*syntax.CallExpr)
	if !ok {
		return conceptNone
	}
	id, ok := call.Fn.(*syntax.Ident)
	if !ok {
		return conceptNone
	}
	switch id.Name {
	case "rule":
		return conceptRule
	case "provider":
		return conceptProvider
	case "aspect":
		return conceptAspect
	case "repository_rule":
		return conceptRepositoryRule
	case "module_extension":
		return conceptModuleExtension
	}
	return conceptNone
}

// lhsNames extracts identifier names from an assignment LHS. Mirrors
// scip-starlark's Phase 0 LHS handling: bare Ident, TupleExpr of
// Idents, ParenExpr wrapping either.
func lhsNames(lhs syntax.Expr) []string {
	switch e := lhs.(type) {
	case *syntax.Ident:
		return []string{e.Name}
	case *syntax.TupleExpr:
		var out []string
		for _, elem := range e.List {
			if id, ok := elem.(*syntax.Ident); ok {
				out = append(out, id.Name)
			}
		}
		return out
	case *syntax.ParenExpr:
		return lhsNames(e.X)
	}
	return nil
}

// applyConcept mutates sym to carry the Bazel concept's Kind plus a
// Documentation line naming the concept. Existing Documentation is
// preserved.
func applyConcept(sym *scip.SymbolInformation, c bazelConcept) {
	kind, doc := conceptMetadata(c)
	if kind != scip.SymbolInformation_UnspecifiedKind {
		sym.Kind = kind
	}
	if doc != "" {
		sym.Documentation = append(sym.Documentation, doc)
	}
}

// conceptMetadata returns the SCIP Kind and descriptive documentation
// line for a Bazel concept.
//
// SymbolKind choice notes — important because SCIP's enum was authored
// for class-based languages (Go, Java, TS, Python) and Starlark/Bazel
// have no class concept whatsoever:
//
//   - rule / repository_rule / module_extension / macro → Function.
//     All four are callables; consumers' "go to definition" lands on
//     a function-shaped symbol (the rule() call returning a function
//     in Starlark; the `def` for macros).
//   - provider → Interface. A provider is a typed record of named
//     fields ("contract that bearing targets satisfy"). SCIP has no
//     Struct/Record kind; Interface is the closest semantic match
//     and renders as a type-shaped icon in most consumers. Using
//     Class would falsely imply OO semantics that Starlark lacks.
//   - aspect → Function. An aspect is a callable analysis pass over
//     the build graph. Its implementation function is what users want
//     to navigate to.
//
// The Documentation field always carries the exact Bazel concept name
// so consumers can route on it precisely when Kind isn't fine enough.
func conceptMetadata(c bazelConcept) (scip.SymbolInformation_Kind, string) {
	switch c {
	case conceptRule:
		return scip.SymbolInformation_Function, "Bazel rule defined via `rule(...)`"
	case conceptProvider:
		return scip.SymbolInformation_Interface, "Bazel provider defined via `provider(...)`"
	case conceptAspect:
		return scip.SymbolInformation_Function, "Bazel aspect defined via `aspect(...)`"
	case conceptRepositoryRule:
		return scip.SymbolInformation_Function, "Bazel repository_rule defined via `repository_rule(...)`"
	case conceptModuleExtension:
		return scip.SymbolInformation_Function, "Bazel module_extension defined via `module_extension(...)`"
	case conceptMacro:
		return scip.SymbolInformation_Function, "Bazel macro (top-level Starlark function)"
	}
	return scip.SymbolInformation_UnspecifiedKind, ""
}
