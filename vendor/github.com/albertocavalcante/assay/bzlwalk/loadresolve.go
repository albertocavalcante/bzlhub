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
	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/internal/syntaxutil"
	"github.com/albertocavalcante/assay/internal/walkparse"
)

// fileLoads is the per-file map of load()-imported names to their
// source-module descriptor. Populated alongside symbolTable during
// the pre-walk indexing pass.
type fileLoads struct {
	// imports maps the local-binding name to the import descriptor.
	// For `load(":consts.bzl", "BASE")` the entry is "BASE" → {
	// ModulePath: ":consts.bzl", OriginalName: "BASE" }.
	// For `load(":consts.bzl", local = "remote")` the entry is
	// "local" → { ":consts.bzl", "remote" }.
	imports map[string]importedSymbol
}

// importedSymbol describes one `load()` binding.
type importedSymbol struct {
	// ModulePath is the verbatim load string — `:foo.bzl`,
	// `//pkg:foo.bzl`, `@external//...`. resolveLoadedFile turns
	// this into a relative file path or rejects external loads.
	ModulePath string
	// OriginalName is the symbol's name in the loaded module
	// (potentially different from the local alias).
	OriginalName string
}

// collectLoads scans a parsed file for top-level load() statements
// and returns the binding map. Reverse of LoadStmt's From/To naming:
// `From[i].Name` is the local-binding name (the one we look up later
// when something in this file references it); `To[i].Name` is the
// name in the loaded module.
func collectLoads(f *syntax.File) fileLoads {
	out := fileLoads{imports: map[string]importedSymbol{}}
	if f == nil {
		return out
	}
	for _, stmt := range f.Stmts {
		load, ok := stmt.(*syntax.LoadStmt)
		if !ok {
			continue
		}
		if load.Module == nil {
			continue
		}
		modulePath, ok := load.Module.Value.(string)
		if !ok {
			continue
		}
		for i, from := range load.From {
			if from == nil || i >= len(load.To) {
				continue
			}
			to := load.To[i]
			if to == nil {
				continue
			}
			out.imports[from.Name] = importedSymbol{
				ModulePath:   modulePath,
				OriginalName: to.Name,
			}
		}
	}
	return out
}

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
		idx.loads[f.Path] = collectLoads(f.AST)
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
