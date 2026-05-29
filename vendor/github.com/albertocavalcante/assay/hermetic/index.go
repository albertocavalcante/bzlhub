package hermetic

// Module-wide pre-pass index. Same shape as bzlwalk's
// moduleSymbolIndex but specialized for hermetic's needs — only
// tracks all-literal dicts (for integrity-hash resolution) and
// load() statements. Source-of-truth comments live in the
// per-field doc.
//
// Why a separate index? hermetic and bzlwalk both walk the same
// tree and parse the same .bzl files (roadmap C3 — merged-walks —
// is the eventual fix). For now correctness > performance: the
// hermetic-specific data is small (a string set + a map per file)
// and rebuilding it independently keeps the two packages decoupled.

import (
	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/internal/walkparse"
)

// importedSymbol describes one local-binding name brought in via load().
type importedSymbol struct {
	ModulePath   string // verbatim load path: ":foo.bzl", "//pkg:foo.bzl", "@ext//..."
	OriginalName string // name in the loaded module (may differ from the local alias)
}

// fileIndex is the per-file slice of the hermetic-wide index.
type fileIndex struct {
	// pinnedDicts names the module-level all-literal dict bindings
	// in this file. A subscript whose base is in this set resolves
	// to a literal sha256 regardless of key.
	pinnedDicts pinnedDictSet
	// loads maps the local-binding name to its import descriptor.
	loads map[string]importedSymbol
}

// hermeticIndex is the module-wide cache of per-file integrity-resolution
// data. Built once at the start of Classify by buildHermeticIndex.
type hermeticIndex struct {
	perFile map[string]fileIndex // module-relative path -> fileIndex
}

// buildHermeticIndexFromFiles consumes a pre-parsed file slice
// (produced by walkparse.Walk) and produces the per-file pinned-dict
// + loads index. No re-walk, no re-parse — reads ASTs straight from
// the input.
//
// Only files with Kind == "bzl" contribute. BUILD/MODULE files don't
// host integrity-hash dicts; including them would just inflate the
// index without changing behaviour.
func buildHermeticIndexFromFiles(files []walkparse.File) *hermeticIndex {
	idx := &hermeticIndex{perFile: map[string]fileIndex{}}
	for _, f := range files {
		if f.Kind != "bzl" || f.AST == nil {
			continue
		}
		idx.perFile[f.Path] = fileIndex{
			pinnedDicts: collectPinnedDicts(f.AST),
			loads:       collectLoadBindings(f.AST),
		}
	}
	return idx
}

// collectLoadBindings scans top-level load() statements and returns
// the local-name → import descriptor map. For
// `load(":foo.bzl", "BAR")` the entry is "BAR" → {":foo.bzl", "BAR"}.
// For `load(":foo.bzl", local = "remote")` the entry is "local" →
// {":foo.bzl", "remote"}.
//
// LoadStmt.From holds the local-binding names (the surprising
// reverse of what the field names suggest — see go.starlark.net's
// syntax.LoadStmt doc comments).
func collectLoadBindings(f *syntax.File) map[string]importedSymbol {
	out := map[string]importedSymbol{}
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
			out[from.Name] = importedSymbol{
				ModulePath:   modulePath,
				OriginalName: to.Name,
			}
		}
	}
	return out
}
