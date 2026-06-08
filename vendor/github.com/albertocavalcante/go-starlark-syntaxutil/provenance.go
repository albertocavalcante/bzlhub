package syntaxutil

import "go.starlark.net/syntax"

// ProvenanceFrom returns the source coordinates of a syntax node:
// the column and row of the span's start, and the column and row of
// the span's end. The file argument is returned verbatim, so callers
// can pass whatever path representation they want surfaced downstream.
//
// Returning named ints rather than a struct decouples this library
// from any consumer's serialization shape.
func ProvenanceFrom(file string, n syntax.Node) (filePath string, startCol, startRow, endCol, endRow int) {
	start, end := n.Span()
	return file, int(start.Col), int(start.Line), int(end.Col), int(end.Line)
}
