package bzlwalk

// Tier-2 cross-file load resolution. When a rule's attrs expression
// references a name brought in via load(":consts.bzl", "X") from
// another file in the same module, this layer follows the load to the
// other file's symbol table and resolves the binding there.
//
// Out of scope: external loads (`@repo//...`) — we only have the local
// module on disk. Those bail cleanly, leaving attrs empty rather than
// guessing.

import (
	"github.com/albertocavalcante/assay/internal/walkparse"
	syntaxutil "github.com/albertocavalcante/go-starlark-syntaxutil"
)

// fileLoads aliases [syntaxutil.ImportedSymbol] maps so legacy bzlwalk
// code that names the wrapper type keeps working. New code can use the
// map type directly.
type fileLoads = map[string]syntaxutil.ImportedSymbol

// moduleSymbolIndex is the module-wide cache built in a pre-walk
// pass. It lets the Tier-2 resolver look up `name` in another file's
// symbol table by the load path from the calling file.
type moduleSymbolIndex struct {
	// perFile maps a module-relative file path (forward slashes) to
	// that file's Tier-1 symbol bindings.
	perFile map[string]symbolTable
	// loads maps a module-relative file path to that file's load
	// statements.
	loads map[string]fileLoads
}

// newModuleSymbolIndex returns an empty index. Useful so that
// downstream code can rely on non-nil maps and skip nil-guards.
func newModuleSymbolIndex() *moduleSymbolIndex {
	return &moduleSymbolIndex{
		perFile: map[string]symbolTable{},
		loads:   map[string]fileLoads{},
	}
}

// buildModuleSymbolIndexFromFiles consumes a pre-parsed file slice
// (produced by walkparse.Walk) and produces the per-file symbol +
// loads index used by the Tier-2 attrs resolver. Reads parsed ASTs
// directly — no re-walk, no re-parse.
//
// Only files with Kind == "bzl" contribute to the index. BUILD files
// don't define dict constants used by rule() attrs expressions, so
// they're skipped at this layer (the main scan still consumes them
// for rule-call extraction).
func buildModuleSymbolIndexFromFiles(files []walkparse.File) *moduleSymbolIndex {
	idx := newModuleSymbolIndex()
	for _, f := range files {
		if f.Kind != "bzl" || f.AST == nil {
			continue
		}
		idx.perFile[f.Path] = collectSymbols(f.AST)
		idx.loads[f.Path] = syntaxutil.CollectLoads(f.AST)
	}
	return idx
}

// resolveLoadedFile delegates to syntaxutil.ResolveLoadedFile so the
// load-path normalization stays a single source of truth shared with
// hermetic's cross-file integrity-hash resolver. Kept as a thin alias
// to avoid touching every call site.
func resolveLoadedFile(callerRelPath, loadPath string) (string, bool) {
	return syntaxutil.ResolveLoadedFile(callerRelPath, loadPath)
}
