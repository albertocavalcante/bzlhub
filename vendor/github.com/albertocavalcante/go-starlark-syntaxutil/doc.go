// Package syntaxutil provides generic helpers for working with
// go.starlark.net/syntax AST nodes. It is pure Starlark, with no
// knowledge of any specific dialect (Bazel, Buck2, Copybara, Tilt).
//
// The helpers are intentionally narrow: keyword-argument extraction
// for call expressions, identifier name resolution for common node
// shapes, load() collection, top-level dict-binding discovery, path
// classification, and source-span extraction.
//
// Source positions are exposed via [ProvenanceFrom], which returns
// four named integers (start col/row, end col/row) rather than a
// struct, so callers can wrap them in whatever serialization shape
// they need.
//
// Dialect-specific predicates (rule symbol classification, bazel_dep
// recognition, and so on) belong in the consumer, not here.
package syntaxutil
