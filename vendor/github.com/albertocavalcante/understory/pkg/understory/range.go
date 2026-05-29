package understory

import scip "github.com/scip-code/scip/bindings/go/scip"

// occurrenceToLocation normalizes a SCIP Occurrence's packed Range into
// the explicit four-field Location shape. SCIP stores ranges as either:
//
//   - 3 ints: [startLine, startChar, endChar] — single-line range; the
//     end line is implied equal to the start line.
//   - 4 ints: [startLine, startChar, endLine, endChar] — multi-line
//     range, fully explicit.
//
// Anything else (nil, fewer than 3, more than 4) is treated as a
// zero-length range pinned to (0, 0); the caller still gets a Location
// it can serialize, but downstream queries (SymbolAtPos) won't match
// any position against it. We tolerate the malformed shape rather than
// erroring because a single bad occurrence shouldn't poison a 100k-doc
// index.
func occurrenceToLocation(file string, occ *scip.Occurrence) Location {
	sl, sc, el, ec := unpackRange(occ.Range)
	return Location{
		File:      file,
		StartLine: sl,
		StartChar: sc,
		EndLine:   el,
		EndChar:   ec,
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
