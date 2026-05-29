// Package understory is a code navigation library. Given a code
// intelligence index (SCIP today; LSIF and heuristic fallbacks on the
// roadmap), it answers the four navigation primitives every IDE and
// code-browser needs: Definition, References, Hover, and SymbolAtPos.
//
// understory is a consumer of code intelligence, not a producer. It does
// not parse source; it reads indexes that some other tool emitted.
//
// The public API surface is locked. See docs/plans/phase-0-design.md.
package understory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	scip "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// Open reads a code intel index from path and returns an in-memory
// queryable view. Format is detected by file extension:
//
//   - ".scip" — SCIP protobuf (v0.1.0).
//   - ".lsif" — LSIF JSON-lines (v0.5.0; not yet supported).
//
// Returns an error if the file cannot be opened, the format is
// unrecognized, or the contents fail to parse.
func Open(path string) (*Index, error) {
	if path == "" {
		return nil, errors.New("understory: Open: empty path")
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".scip":
		// supported
	case ".lsif":
		return nil, fmt.Errorf("understory: Open %q: LSIF support is planned for v0.5.0", path)
	default:
		// Be permissive: many real-world fixtures (and canopy's stored
		// blobs) don't carry the .scip suffix. Treat anything else as
		// SCIP protobuf and let the unmarshaler decide.
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("understory: Open %q: %w", path, err)
	}
	return parseSCIP(b)
}

// OpenBytes parses a SCIP index from the provided byte slice. Lets
// consumers that already have the index in memory (e.g. canopy serves
// SCIP blobs out of a SQLite BLOB column) skip the disk round-trip.
//
// SCIP-only in v0.1.x; future formats will dispatch based on a small
// magic-byte sniff once the reader-layer abstraction lands in v0.5.0.
func OpenBytes(b []byte) (*Index, error) {
	if len(b) == 0 {
		return nil, errors.New("understory: OpenBytes: empty payload")
	}
	return parseSCIP(b)
}

// OpenReader parses a SCIP index from an arbitrary io.Reader. Drains
// the reader into memory first — SCIP is a single protobuf message
// with no streaming representation, so the whole payload must be
// buffered before unmarshal. The reader is NOT closed; the caller
// retains ownership.
//
// Convenient for HTTP response bodies, gRPC streams, gzip-wrapped
// fixtures, and any future v0.2.0+ HTTP server use case where the
// index arrives over the wire.
func OpenReader(r io.Reader) (*Index, error) {
	if r == nil {
		return nil, errors.New("understory: OpenReader: nil reader")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("understory: OpenReader: %w", err)
	}
	return OpenBytes(b)
}

// parseSCIP unmarshals a SCIP protobuf payload and builds the in-memory
// lookup tables.
func parseSCIP(b []byte) (*Index, error) {
	var raw scip.Index
	if err := proto.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("understory: parse SCIP: %w", err)
	}
	return buildIndex(&raw), nil
}

// buildIndex walks every document/occurrence and pre-computes the
// lookup tables the query layer needs.
func buildIndex(raw *scip.Index) *Index {
	idx := &Index{
		documents:    raw.Documents,
		byDefinition: make(map[string]Location),
		byOccurrence: make(map[string][]Location),
		bySymbolInfo: make(map[string]*scip.SymbolInformation),
		byFile:       make(map[string][]docOccurrence),
	}
	for _, doc := range raw.Documents {
		if doc == nil {
			continue
		}
		for _, si := range doc.Symbols {
			if si == nil || si.Symbol == "" {
				continue
			}
			// First definition wins; SCIP allows the same symbol's
			// SymbolInformation to appear in multiple documents (e.g.
			// re-exports) but for v0.1.0 Hover returns whichever was
			// observed first.
			if _, present := idx.bySymbolInfo[si.Symbol]; !present {
				idx.bySymbolInfo[si.Symbol] = si
			}
		}
		for _, occ := range doc.Occurrences {
			if occ == nil || occ.Symbol == "" {
				continue
			}
			loc := occurrenceToLocation(doc.RelativePath, occ)
			idx.byOccurrence[occ.Symbol] = append(idx.byOccurrence[occ.Symbol], loc)
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 {
				if _, present := idx.byDefinition[occ.Symbol]; !present {
					idx.byDefinition[occ.Symbol] = loc
				}
			}
			idx.byFile[doc.RelativePath] = append(idx.byFile[doc.RelativePath], docOccurrence{
				symbol:       occ.Symbol,
				loc:          loc,
				isDefinition: occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0,
			})
		}
	}
	return idx
}

// Index is the queryable form of a code intel database. It holds the
// normalized in-memory model produced by the reader layer (see
// docs/plans/architecture.md). Construct via Open; do not zero-value.
type Index struct {
	documents    []*scip.Document
	byDefinition map[string]Location
	byOccurrence map[string][]Location
	bySymbolInfo map[string]*scip.SymbolInformation
	byFile       map[string][]docOccurrence
}

// docOccurrence is a per-file occurrence record used by SymbolAtPos
// and Occurrences. isDefinition mirrors the SCIP role bit so the v0.2.1
// per-file overlay can distinguish definition spans from references
// without re-querying byDefinition.
type docOccurrence struct {
	symbol       string
	loc          Location
	isDefinition bool
}

// Definition returns the location where the given SCIP symbol is
// defined.
//
// ok is false when the symbol appears in the index only as an external
// reference (no definition occurrence is recorded in this file) or when
// the symbol is not present at all. Both cases return err == nil; err is
// reserved for I/O or invariant failures.
func (i *Index) Definition(symbol string) (loc Location, ok bool, err error) {
	if i == nil {
		return Location{}, false, errors.New("understory: Definition on nil *Index")
	}
	loc, ok = i.byDefinition[symbol]
	return loc, ok, nil
}

// References returns every read-access occurrence of the symbol across
// all documents in the index. The definition occurrence is included in
// the result iff includeDefinition is true.
//
// A symbol with no occurrences returns (nil, nil) — "no references" is a
// valid, empty answer, not an error.
func (i *Index) References(symbol string, includeDefinition bool) ([]Location, error) {
	if i == nil {
		return nil, errors.New("understory: References on nil *Index")
	}
	occs := i.byOccurrence[symbol]
	if len(occs) == 0 {
		return nil, nil
	}
	if includeDefinition {
		out := make([]Location, len(occs))
		copy(out, occs)
		return out, nil
	}
	defLoc, hasDef := i.byDefinition[symbol]
	out := make([]Location, 0, len(occs))
	for _, l := range occs {
		if hasDef && l == defLoc {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Hover returns the documentation strings attached to the symbol's
// SymbolInformation record, verbatim. Most indexers emit a single
// markdown-formatted entry; some emit one entry per format variant.
//
// A symbol with no attached documentation returns (nil, nil).
func (i *Index) Hover(symbol string) ([]string, error) {
	if i == nil {
		return nil, errors.New("understory: Hover on nil *Index")
	}
	si, ok := i.bySymbolInfo[symbol]
	if !ok || si == nil || len(si.Documentation) == 0 {
		return nil, nil
	}
	out := make([]string, len(si.Documentation))
	copy(out, si.Documentation)
	return out, nil
}

// SymbolAtPos resolves the symbol whose occurrence range covers
// (file, line, character). Useful for editor-style "click to navigate":
// the surface layer receives a position from the user and asks the query
// layer which symbol lives there.
//
// Returns "" (empty string), nil if no occurrence covers that position
// or if the file is not present in the index.
func (i *Index) SymbolAtPos(file string, line, character int32) (string, error) {
	if i == nil {
		return "", errors.New("understory: SymbolAtPos on nil *Index")
	}
	occs, ok := i.byFile[file]
	if !ok {
		return "", nil
	}
	for _, oc := range occs {
		if locationContains(oc.loc, line, character) {
			return oc.symbol, nil
		}
	}
	return "", nil
}

// Location is a single occurrence range in a single document. The JSON
// tags are intentional: v0.2.0 will serve Location values directly over
// HTTP, so locking the wire shape here means the future server doesn't
// need a parallel DTO type.
type Location struct {
	File      string `json:"file"`
	StartLine int32  `json:"start_line"`
	StartChar int32  `json:"start_char"`
	EndLine   int32  `json:"end_line"`
	EndChar   int32  `json:"end_char"`
}

// Occurrence is one symbol-position record within a single document.
// Returned by Index.Occurrences; the file is the caller-side query
// context and so is omitted from the wire shape.
//
// IsDefinition is the SCIP SymbolRole_Definition bit; references and
// other role bits collapse to IsDefinition: false.
type Occurrence struct {
	Symbol       string `json:"symbol"`
	StartLine    int32  `json:"start_line"`
	StartChar    int32  `json:"start_char"`
	EndLine      int32  `json:"end_line"`
	EndChar      int32  `json:"end_char"`
	IsDefinition bool   `json:"is_definition"`
}

// Files returns every file path the index has any occurrence in,
// sorted ASCII ascending. Paths come from SCIP Document.RelativePath
// verbatim — neither cleaned nor normalized — so the wire shape
// mirrors what the indexer recorded.
//
// Returns an empty (non-nil) slice for an empty index.
func (i *Index) Files() []string {
	if i == nil {
		return []string{}
	}
	out := make([]string, 0, len(i.byFile))
	for f := range i.byFile {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Occurrences returns every occurrence in the given file in
// document order (the order the indexer emitted them; not sorted).
// Returns an empty (non-nil) slice for an unknown file — "the index
// has no occurrences in this file" is a normal answer for source files
// the indexer didn't cover.
func (i *Index) Occurrences(file string) []Occurrence {
	if i == nil {
		return []Occurrence{}
	}
	docOccs := i.byFile[file]
	out := make([]Occurrence, len(docOccs))
	for j, d := range docOccs {
		out[j] = Occurrence{
			Symbol:       d.symbol,
			StartLine:    d.loc.StartLine,
			StartChar:    d.loc.StartChar,
			EndLine:      d.loc.EndLine,
			EndChar:      d.loc.EndChar,
			IsDefinition: d.isDefinition,
		}
	}
	return out
}
