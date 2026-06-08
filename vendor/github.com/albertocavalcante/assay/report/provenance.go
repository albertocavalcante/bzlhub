package report

import (
	"go.starlark.net/syntax"

	syntaxutil "github.com/albertocavalcante/go-starlark-syntaxutil"
)

// ProvenanceFromNode extracts a [Provenance] from a `go.starlark.net`
// AST node, given the module-relative file path the node was parsed
// from.
//
// This is a thin assay-local wrapper around
// `go-starlark-syntaxutil.ProvenanceFrom`, which returns the four
// span coordinates as named ints (decoupled from any consumer's
// serialization shape). assay packs them into [Provenance] — the
// JSON-tagged struct that flows into ModuleReport.
//
// Living here in the report package puts the wrapper next to the
// struct it produces, so the boundary is one import for every
// caller (bzlwalk, hermetic, modulefile).
func ProvenanceFromNode(file string, n syntax.Node) Provenance {
	f, startCol, startRow, endCol, endRow := syntaxutil.ProvenanceFrom(file, n)
	return Provenance{
		File:     f,
		StartCol: startCol,
		StartRow: startRow,
		EndCol:   endCol,
		EndRow:   endRow,
	}
}
