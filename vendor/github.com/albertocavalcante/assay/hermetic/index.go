package hermetic

// Module-wide pre-pass index. Same shape as bzlwalk's
// moduleSymbolIndex but specialized for hermetic's needs — only
// tracks all-literal dicts (for integrity-hash resolution) and
// load() statements. The shared load shape (local-name -> import
// descriptor) lives in internal/syntaxutil; hermetic only owns the
// pinned-dict-specific data.
//
// Why a separate index? hermetic and bzlwalk both walk the same
// tree and parse the same .bzl files (roadmap C3 — merged-walks —
// is the eventual fix). For now correctness > performance: the
// hermetic-specific data is small and rebuilding it independently
// keeps the two packages decoupled.

import (
	"github.com/albertocavalcante/assay/internal/syntaxutil"
	"github.com/albertocavalcante/assay/internal/walkparse"
)

// fileIndex is the per-file slice of the hermetic-wide index.
type fileIndex struct {
	// pinnedDicts names the module-level all-literal dict bindings
	// in this file. A subscript whose base is in this set resolves
	// to a literal sha256 regardless of key.
	pinnedDicts pinnedDictSet
	// loads maps the local-binding name to its import descriptor.
	loads map[string]syntaxutil.ImportedSymbol
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
			loads:       syntaxutil.CollectLoads(f.AST),
		}
	}
	return idx
}
