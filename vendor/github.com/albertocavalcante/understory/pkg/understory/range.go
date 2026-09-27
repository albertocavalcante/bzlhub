package understory

import scip "github.com/scip-code/scip/bindings/go/scip"

// occurrenceToLocation normalizes a SCIP Occurrence's range into the explicit
// four-field Location shape.
//
// It reads through occ.SourceRange() rather than occ.Range, and that is not a
// stylistic choice. Schema v0.9.0 replaced the flat `range` field with a typed
// oneof (single_line_range / multi_line_range) and deprecated the old one. A
// consumer that reads occ.Range directly sees NOTHING from any index a modern
// producer emits -- nil for every occurrence, so every Location collapses to
// (0,0,0,0), every query returns a zero range, and nothing anywhere reports an
// error. understory did exactly that until this change.
//
// SourceRange() prefers the typed form and falls back to the flat one, so both
// old and new producers are read correctly.
//
// An occurrence with no readable range at all is treated as a zero-length
// range pinned to (0, 0): the caller still gets a Location it can serialize,
// but SymbolAtPos will not match any position against it. That is deliberate
// tolerance -- a single malformed occurrence should not poison a 100k-document
// index -- and is why the typed-range blindness was survivable enough to go
// unnoticed.
func occurrenceToLocation(file string, occ *scip.Occurrence) Location {
	r, ok := occ.SourceRange()
	if !ok {
		return Location{File: file}
	}
	return Location{
		File:      file,
		StartLine: r.Start.Line,
		StartChar: r.Start.Character,
		EndLine:   r.End.Line,
		EndChar:   r.End.Character,
	}
}

// unpackRange returns (startLine, startChar, endLine, endChar) from a
// SCIP packed range. The 3-int form implies endLine == startLine.
func unpackRange(r []int32) (sl, sc, el, ec int32) {
	switch len(r) {
	case 3:
		return r[0], r[1], r[0], r[2]
	case 4:
		return r[0], r[1], r[2], r[3]
	default:
		return 0, 0, 0, 0
	}
}

// locationContains reports whether the half-open range
// [(StartLine, StartChar), (EndLine, EndChar)) covers (line, character).
//
// SCIP ranges are half-open, matching LSP semantics: the end position
// is exclusive. A click on the cursor immediately after an identifier
// does NOT land on that identifier.
func locationContains(l Location, line, character int32) bool {
	// Before the range start.
	if line < l.StartLine {
		return false
	}
	if line == l.StartLine && character < l.StartChar {
		return false
	}
	// At or past the range end (exclusive).
	if line > l.EndLine {
		return false
	}
	if line == l.EndLine && character >= l.EndChar {
		return false
	}
	return true
}
