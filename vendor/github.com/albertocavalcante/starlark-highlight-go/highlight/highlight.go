// Package highlight defines the core types + registry for Starlark-
// flavored syntax-highlighting. The package itself knows NOTHING
// about specific dialects (Bazel, Buck2, Copybara, custom flavors).
// Pure Starlark lives in the sibling `starlark/` subpackage; Bazel,
// Buck2 etc. live in separate repos that implement Dialect.
//
// Design goals:
//   - Determinism. Every Token comes from a real parser's AST walk.
//     No regex priority orderings.
//   - Decoupling. Pure-Starlark consumers don't pull in Bazel.
//     Bazel consumers don't pull in Buck2. Custom dialect implementers
//     reuse the framework without forking it.
//   - Extensibility. Dialects can add their own Kinds without
//     coordinating with this package; Token.Meta lets dialects carry
//     dialect-specific data (e.g. doc URLs for Bazel builtins) on a
//     per-token basis without protocol changes here.
//   - Reusability. Anyone shipping a tool that views .bzl files —
//     LSP server, web viewer, terminal pretty-printer, codemod
//     summarizer — can import one library and get the right tokens
//     for whatever flavor of Starlark they're handling.
package highlight

import (
	"errors"
	"sort"
)

// Kind classifies a highlight span. The five core kinds below are
// what every dialect MUST be able to emit; richer dialects add their
// own namespaced kinds (e.g. "bazel.label", "buck2.target") and
// document them in their own packages.
//
// Renderers that only know the core kinds can either:
//   - Pass dialect-specific kinds through as opaque CSS classes (the
//     theme palette decides what styling, if any, to apply).
//   - Ask the Dialect for a Fallback mapping to coerce
//     "bazel.label" → "string" or similar.
type Kind string

const (
	KindKeyword    Kind = "keyword"
	KindString     Kind = "string"
	KindNumber     Kind = "number"
	KindComment    Kind = "comment"
	KindIdentifier Kind = "identifier"
)

// Token is one highlight span. Positions follow LSP/SCIP convention:
// lines are 1-based for the library output (consumers shift to
// 0-based at the wire boundary if they're SCIP-aligning, as
// understory does).
//
// Meta carries dialect-specific data. Common patterns:
//   - bazel.builtin tokens use Meta["name"] (rule name, e.g. "cc_binary")
//     and Meta["url"] (link to bazel.build docs) so renderers can
//     surface tooltips/links without an out-of-band lookup table.
//   - bazel.label tokens use Meta["label"] (the parsed label canonical
//     form, e.g. "@//foo:bar") for click-to-navigate.
//
// Unknown Meta keys are renderer-ignored. Adding a new key in a
// dialect is non-breaking.
type Token struct {
	Kind      Kind              `json:"kind"`
	StartLine int               `json:"start_line"`
	StartChar int               `json:"start_char"`
	EndLine   int               `json:"end_line"`
	EndChar   int               `json:"end_char"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Dialect is what a Starlark flavor implements to participate in the
// highlight pipeline.
//
// The framework owns coordinate sorting + registry-level dispatch.
// The Dialect owns "what's a keyword in this dialect, what counts as
// a builtin, which AST nodes get highlighted."
type Dialect interface {
	// Name identifies the dialect for logging + diagnostics.
	// Convention: lowercase, no slashes, e.g. "starlark", "bazel",
	// "buck2".
	Name() string

	// Match returns true if this dialect should handle `filename`.
	// Filename-based dispatch mirrors how Bazel itself picks a
	// parser; multiple dialects can match the same filename — the
	// Registry's first-registered-wins rule resolves the tie.
	Match(filename string) bool

	// Tokenize parses src under this dialect's grammar and returns
	// highlight tokens. The framework sorts the output and may
	// deduplicate same-span tokens; the Dialect just needs to
	// produce the set, not worry about order.
	//
	// On parse error, returns the tokens collected before the
	// failure plus the error. Partial highlighting strictly beats
	// nothing in editors that show malformed buffers.
	Tokenize(filename string, src []byte) ([]Token, error)

	// Fallback maps a dialect-specific Kind to one of the five core
	// Kinds. Renderers that only style the core categories use this
	// to coerce extended kinds (e.g. "bazel.label" → "string").
	// Dialects that only emit core kinds can return the input
	// verbatim.
	Fallback(Kind) Kind
}

// Registry is the top-level entry point. Dialects register in
// priority order (first match wins). Tokenize dispatches to the
// matching dialect or returns ErrUnsupportedDialect when none match.
type Registry struct {
	dialects []Dialect
}

// NewRegistry returns an empty Registry. Callers register dialects
// before calling Tokenize:
//
//	reg := highlight.NewRegistry()
//	reg.Register(bazel.Dialect{})
//	reg.Register(starlark.Dialect{})
//	tokens, _ := reg.Tokenize("BUILD.bazel", src)
//
// Why empty by default rather than auto-populated with starlark:
// the library has no view on what dialect set a consumer wants. An
// LSP that only handles Bazel doesn't want the starlark fallback to
// match its .bzl files. An editor that handles many flavors picks
// the order explicitly. We keep policy out of the library.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a dialect to the registry. Returns the receiver for
// chained registration.
func (r *Registry) Register(d Dialect) *Registry {
	r.dialects = append(r.dialects, d)
	return r
}

// ErrUnsupportedDialect is returned by Tokenize when no registered
// dialect's Match returns true. Callers should treat this as "render
// plain text" rather than as a hard failure — it's the normal answer
// for non-Starlark files a viewer might see (README.md, *.json, etc).
var ErrUnsupportedDialect = errors.New("highlight: no registered dialect matched")

// Tokenize dispatches to the first dialect whose Match returns true.
// Output is sorted by (StartLine, StartChar) — the per-dialect
// Tokenize is allowed to return any order, the registry normalizes.
func (r *Registry) Tokenize(filename string, src []byte) ([]Token, error) {
	for _, d := range r.dialects {
		if d.Match(filename) {
			toks, err := d.Tokenize(filename, src)
			SortByPosition(toks)
			return toks, err
		}
	}
	return nil, ErrUnsupportedDialect
}

// SortByPosition is exported as a helper for dialect implementers
// who want to maintain sorted order during construction. The Registry
// also calls it on the post-Tokenize stream.
func SortByPosition(toks []Token) {
	sort.SliceStable(toks, func(i, j int) bool {
		a, b := toks[i], toks[j]
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return a.StartChar < b.StartChar
	})
}
