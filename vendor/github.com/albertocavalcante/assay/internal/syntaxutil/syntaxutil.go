// Package syntaxutil holds helpers shared by the bzlwalk and hermetic
// packages for working with go.starlark.net AST nodes. It exists to
// keep one source of truth for cross-cutting helpers like provenance
// extraction; consumers outside assay aren't expected to depend on it.
package syntaxutil

import (
	"go.starlark.net/syntax"

	"github.com/albertocavalcante/assay/report"
)

// ProvenanceFrom records the file path and source span of a syntax node.
// File should be the module-relative path (caller's responsibility).
func ProvenanceFrom(file string, n syntax.Node) report.Provenance {
	start, end := n.Span()
	return report.Provenance{
		File:     file,
		StartCol: int(start.Col),
		StartRow: int(start.Line),
		EndCol:   int(end.Col),
		EndRow:   int(end.Line),
	}
}
