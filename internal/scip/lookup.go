package scip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/albertocavalcante/understory/pkg/understory"
)

// SymbolLookupResult is the wire shape returned by canopy's
// symbol-lookup surfaces (MCP tool today; future REST endpoint).
//
// Found=false means understory loaded the SCIP index for (module,
// version) but found no Definition occurrence for the queried symbol —
// distinct from "the blob itself isn't stored", which surfaces as a
// not-found error from BlobReader.GetScipBlob.
type SymbolLookupResult struct {
	Module      string   `json:"module"`
	Version     string   `json:"version"`
	Symbol      string   `json:"symbol"`
	Found       bool     `json:"found"`
	File        string   `json:"file,omitempty"`
	StartLine   int32    `json:"start_line,omitempty"`
	StartColumn int32    `json:"start_column,omitempty"`
	EndLine     int32    `json:"end_line,omitempty"`
	EndColumn   int32    `json:"end_column,omitempty"`
	// Documentation carries SymbolInformation.Documentation verbatim —
	// for Bazel-annotated symbols (which scip-bazel produces) this is
	// the "Bazel rule defined via `rule(...)`" style line.
	Documentation []string `json:"documentation,omitempty"`
}

// BlobReader is the slice of canopy's store interface this package
// depends on. Kept as a one-method interface so tests can fake it
// without dragging in a real SQLite store.
type BlobReader interface {
	GetScipBlob(ctx context.Context, module, version string) ([]byte, error)
}

// LookupSymbol fetches the stored SCIP index for (module, version) and
// resolves the FULL SCIP symbol string to its definition site by
// delegating to understory.Open + Index.Definition.
//
// The full SCIP symbol shape canopy emits is:
//
//	bzlmod <module>@<version> <relpath>#<name>
//
// e.g. `bzlmod rules_python@0.40.0 python/defs.bzl#py_library`.
// Callers that only know the short identifier (`py_library`) can
// construct it from bzlhub_get_module's provenance data (each rule /
// provider / macro has a Provenance.File field giving the relpath).
//
// Backed by understory.OpenBytes from v0.1.1 onward — canopy's SCIP
// indexes live as SQLite BLOBs, so we hand the bytes straight to
// understory without a disk round-trip.
func LookupSymbol(ctx context.Context, br BlobReader, module, version, symbol string) (*SymbolLookupResult, error) {
	if module == "" || version == "" || symbol == "" {
		return nil, errors.New("LookupSymbol: module, version, and symbol are all required")
	}
	out := &SymbolLookupResult{Module: module, Version: version, Symbol: symbol}

	blob, err := br.GetScipBlob(ctx, module, version)
	if err != nil {
		return nil, err
	}

	idx, err := understory.OpenBytes(blob)
	if err != nil {
		return nil, fmt.Errorf("understory.OpenBytes: %w", err)
	}

	loc, ok, err := idx.Definition(symbol)
	if err != nil {
		return nil, fmt.Errorf("understory.Definition: %w", err)
	}
	if !ok {
		// Symbol not defined in this index (it might be present purely
		// as an external reference via load()). Not-found is a normal
		// answer, not an error.
		return out, nil
	}
	out.Found = true
	out.File = loc.File
	out.StartLine = loc.StartLine
	out.StartColumn = loc.StartChar
	out.EndLine = loc.EndLine
	out.EndColumn = loc.EndChar
	if docs, derr := idx.Hover(symbol); derr == nil && len(docs) > 0 {
		out.Documentation = docs
	}
	return out, nil
}

// SymbolReferencesResult is the wire shape for "find all usages of
// this symbol across the module's SCIP index". Distinct from
// SymbolLookupResult because the data shape differs (a list of
// locations rather than a single definition) and the empty-set case
// is more common (lots of symbols have zero local refs).
type SymbolReferencesResult struct {
	Module     string                  `json:"module"`
	Version    string                  `json:"version"`
	Symbol     string                  `json:"symbol"`
	Count      int                     `json:"count"`
	References []understory.Location   `json:"references"`
}

// LookupReferences fetches the SCIP blob for (module, version) and
// returns every occurrence of the given symbol — call sites, variable
// reads, plus (optionally) the definition itself.
//
// includeDefinition is exposed because some agent prompts want
// "where is this used?" (just refs, def excluded) while others want
// "show me everywhere this name appears" (refs + def). Mirrors the
// understory library's References signature.
//
// Returns Count=0 + References=empty (not nil) when the symbol exists
// in the index but has no occurrences, OR when the symbol isn't in
// the index at all. Distinguishing the two would require an extra
// bySymbolInfo check; for now both collapse to "no refs to surface."
func LookupReferences(ctx context.Context, br BlobReader, module, version, symbol string, includeDefinition bool) (*SymbolReferencesResult, error) {
	if module == "" || version == "" || symbol == "" {
		return nil, errors.New("LookupReferences: module, version, and symbol are all required")
	}
	out := &SymbolReferencesResult{
		Module:     module,
		Version:    version,
		Symbol:     symbol,
		References: []understory.Location{},
	}

	blob, err := br.GetScipBlob(ctx, module, version)
	if err != nil {
		return nil, err
	}
	idx, err := understory.OpenBytes(blob)
	if err != nil {
		return nil, fmt.Errorf("understory.OpenBytes: %w", err)
	}
	refs, err := idx.References(symbol, includeDefinition)
	if err != nil {
		return nil, fmt.Errorf("understory.References: %w", err)
	}
	if refs != nil {
		out.References = refs
		out.Count = len(refs)
	}
	return out, nil
}

// ModuleVersion is the per-(module, version) coordinate the xrefs walker
// iterates over. Mirrors store.ModuleVersion shape so the server adapter
// is a trivial copy, but kept in this package so it doesn't depend on
// the SQLite-bound store package.
type ModuleVersion struct {
	Module  string
	Version string
}

// XRefsLister is the slice of canopy's store interface LookupXRefs
// depends on for enumerating indexed coordinates. Kept narrow so tests
// can fake it.
type XRefsLister interface {
	ListScipVersions(ctx context.Context) ([]ModuleVersion, error)
}

// XRefsGroup is the per-module slice of a cross-module references
// result: every occurrence of the queried symbol that this module's
// SCIP index contains.
type XRefsGroup struct {
	Module     string                `json:"module"`
	Version    string                `json:"version"`
	References []understory.Location `json:"references"`
}

// XRefsResult aggregates references to a single SCIP symbol across
// every (module, version) canopy has an index for. Used by the
// `/api/xrefs` endpoint that the UI calls when running under a
// per-module code-nav mount — without it, the references panel would
// only ever see the symbol's own module, missing every consumer.
//
// Skipped counts coordinates whose blob couldn't be loaded or parsed
// (corrupt write, ingest race, etc.). The walker continues so a single
// bad index doesn't deny service for the whole catalogue, but surfacing
// the number means operators can correlate "missing refs" with bad
// blobs without grepping logs.
type XRefsResult struct {
	Symbol  string       `json:"symbol"`
	Count   int          `json:"count"`
	Skipped int          `json:"skipped"`
	Groups  []XRefsGroup `json:"groups"`
}

// LookupXRefs walks every indexed (module, version), asks each SCIP
// index for occurrences of `symbol`, and aggregates by module. Empty
// groups are omitted from the result so a UI can render "Used by"
// directly off `Groups` without filtering.
//
// Resilience: a single broken SCIP blob doesn't abort the walk — we
// log nothing here (callers can wrap if they need observability) and
// just skip that coordinate. Partial results beat a hard error when
// half the ingest pipeline is mid-rebuild.
//
// Ordering: groups are returned sorted by module ASC, then version
// ASC, so the wire output is deterministic across calls.
func LookupXRefs(ctx context.Context, br BlobReader, lister XRefsLister, symbol string, includeDefinition bool) (*XRefsResult, error) {
	if symbol == "" {
		return nil, errors.New("LookupXRefs: symbol is required")
	}
	out := &XRefsResult{Symbol: symbol, Groups: []XRefsGroup{}}

	versions, err := lister.ListScipVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scip versions: %w", err)
	}

	for _, mv := range versions {
		blob, err := br.GetScipBlob(ctx, mv.Module, mv.Version)
		if err != nil {
			out.Skipped++
			slog.Warn("xrefs: skip module (blob load)", "module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		idx, err := understory.OpenBytes(blob)
		if err != nil {
			out.Skipped++
			slog.Warn("xrefs: skip module (parse)", "module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		refs, err := idx.References(symbol, includeDefinition)
		if err != nil {
			out.Skipped++
			slog.Warn("xrefs: skip module (references)", "module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		if len(refs) == 0 {
			// Not a skip — the index was readable, the symbol just
			// isn't used here. Empty result is the answer.
			continue
		}
		out.Groups = append(out.Groups, XRefsGroup{
			Module:     mv.Module,
			Version:    mv.Version,
			References: refs,
		})
		out.Count += len(refs)
	}

	sort.SliceStable(out.Groups, func(i, j int) bool {
		if out.Groups[i].Module != out.Groups[j].Module {
			return out.Groups[i].Module < out.Groups[j].Module
		}
		return out.Groups[i].Version < out.Groups[j].Version
	})

	return out, nil
}
