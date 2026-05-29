// Package highlight implements the Bazel-flavored Starlark dialect
// for starlark-highlight-go. It handles BUILD, BUILD.bazel,
// MODULE.bazel, WORKSPACE (and the bzlmod variant), and `.bzl` files
// when registered ahead of the pure-Starlark dialect.
//
// The dialect emits the five core Kinds plus three Bazel-specific
// ones:
//
//   - KindLabel    — label literals like //foo:bar or @repo//foo:bar
//   - KindBuiltin  — curated Bazel builtin calls (cc_binary, glob,
//                    select, etc.). Each KindBuiltin token carries
//                    a Meta map with "name", "url", "description"
//                    so renderers can surface doc tooltips without
//                    a client-side lookup table.
//   - KindAttribute — attribute keyword positions inside rule calls
//                    (the `name =`, `srcs =`, `deps =` on the left
//                    of a keyword argument). Useful for visual
//                    distinction in dense rule invocations.
//
// Renderers that only know the core Kinds can use Dialect.Fallback
// to coerce these into a core category. We map:
//
//   bazel.label    → string       (labels are quoted string literals)
//   bazel.builtin  → identifier   (calls look like function calls)
//   bazel.attribute → identifier
package highlight

import (
	"strings"

	bzlsyntax "github.com/albertocavalcante/starlark-syntax-go/bzl"

	"github.com/albertocavalcante/starlark-highlight-go/highlight"
)

// Kinds specific to the Bazel dialect. Namespaced with the "bazel."
// prefix so renderers can dispatch on the prefix without coordinating
// per-Kind: a `class:` rule that targets `[class^="bazel-"]` styles
// every Bazel-specific token at once.
const (
	KindLabel     highlight.Kind = "bazel.label"
	KindBuiltin   highlight.Kind = "bazel.builtin"
	KindAttribute highlight.Kind = "bazel.attribute"
)

// Dialect implements highlight.Dialect for Bazel's Starlark flavor.
// Stateless — usable as a zero value.
type Dialect struct{}

// Name satisfies highlight.Dialect.
func (Dialect) Name() string { return "bazel" }

// Match returns true for the well-known Bazel filenames + the .bzl
// extension. We claim .bzl alongside BUILD/MODULE.bazel/WORKSPACE so
// callers that want full Bazel-flavored highlighting can register
// this dialect before the pure-starlark fallback.
//
// The match set mirrors bazel-syntax-go/bzl.getFileType, which is
// the canonical authority on "what counts as a Bazel file."
func (Dialect) Match(filename string) bool {
	base := filename
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			base = base[i+1:]
			break
		}
	}
	switch base {
	case "BUILD", "BUILD.bazel",
		"MODULE.bazel",
		"WORKSPACE", "WORKSPACE.bazel", "WORKSPACE.bzlmod":
		return true
	}
	return strings.HasSuffix(base, ".bzl")
}

// Fallback maps Bazel-specific Kinds to core categories so renderers
// that only style the core five still produce sensible output.
func (Dialect) Fallback(k highlight.Kind) highlight.Kind {
	switch k {
	case KindLabel:
		return highlight.KindString
	case KindBuiltin, KindAttribute:
		return highlight.KindIdentifier
	}
	return k
}

// Tokenize walks the parsed Bazel AST and emits highlight tokens.
// Layers, in order:
//
//  1. Core grammar (keywords, identifiers, strings, numbers, comments)
//     — same shape as pure Starlark, just walking the bzl AST.
//  2. Label promotion — string literals that match the Bazel label
//     grammar (//foo:bar, @repo//foo:bar, :name) get KindLabel
//     instead of KindString.
//  3. Builtin promotion — identifier tokens whose name appears in
//     the curated registry get KindBuiltin with Meta{name,url,desc}.
//  4. Attribute promotion — Idents that appear as the LHS of a
//     keyword argument inside a CallExpr get KindAttribute.
//
// On parse error, returns partial tokens — partial highlighting beats
// nothing in editors.
func (Dialect) Tokenize(filename string, src []byte) ([]highlight.Token, error) {
	file, err := bzlsyntax.Parse(filename, src)
	if file == nil {
		return nil, err
	}

	out := make([]highlight.Token, 0, 64)
	commentSeen := make(map[positionKey]bool)

	for _, stmt := range file.Stmt {
		bzlsyntax.Walk(stmt, func(n bzlsyntax.Expr, stk []bzlsyntax.Expr) {
			if n == nil {
				return
			}

			if c := n.Comment(); c != nil {
				collectComments(&out, commentSeen, c.Before)
				collectComments(&out, commentSeen, c.Suffix)
				collectComments(&out, commentSeen, c.After)
			}

			switch v := n.(type) {
			case *bzlsyntax.DefStmt:
				emit(&out, highlight.KindKeyword, v.Function.StartPos, "def")
				// DefStmt.Name is a plain string on the bzl AST (not
				// an Ident node), so it isn't visited by Walk and
				// wouldn't get an Identifier token otherwise.
				// Synthesize one at `def ` + 4 column-rune offset.
				if v.Name != "" {
					namePos := v.Function.StartPos
					namePos.LineRune += 4
					emit(&out, highlight.KindIdentifier, namePos, v.Name)
				}
			case *bzlsyntax.IfStmt:
				if kw := keywordAt(src, v.If, []string{"if", "elif"}); kw != "" {
					emit(&out, highlight.KindKeyword, v.If, kw)
				}
				if v.ElsePos.Pos.Line > 0 {
					if kw := keywordAt(src, v.ElsePos.Pos, []string{"else", "elif"}); kw != "" {
						emit(&out, highlight.KindKeyword, v.ElsePos.Pos, kw)
					}
				}
			case *bzlsyntax.ForStmt:
				emit(&out, highlight.KindKeyword, v.Function.StartPos, "for")
			case *bzlsyntax.ReturnStmt:
				emit(&out, highlight.KindKeyword, v.Return, "return")
			case *bzlsyntax.LoadStmt:
				emit(&out, highlight.KindKeyword, v.Load, "load")
			case *bzlsyntax.LambdaExpr:
				emit(&out, highlight.KindKeyword, v.Function.StartPos, "lambda")
			case *bzlsyntax.BranchStmt:
				emit(&out, highlight.KindKeyword, v.TokenPos, v.Token)
			case *bzlsyntax.ForClause:
				emit(&out, highlight.KindKeyword, v.For, "for")
				emit(&out, highlight.KindKeyword, v.In, "in")
			case *bzlsyntax.IfClause:
				emit(&out, highlight.KindKeyword, v.If, "if")
			case *bzlsyntax.Ident:
				switch v.Name {
				case "True", "False", "None":
					emit(&out, highlight.KindKeyword, v.NamePos, v.Name)
				default:
					if b, ok := Builtins[v.Name]; ok {
						// Builtin promotion happens inline because we
						// have the name in scope right now. The Token
						// carries Meta so renderers can surface a
						// tooltip with the doc URL — no client-side
						// lookup table required.
						emitBuiltin(&out, v.NamePos, v.Name, b)
					} else {
						emit(&out, highlight.KindIdentifier, v.NamePos, v.Name)
					}
				}
			case *bzlsyntax.LiteralExpr:
				// Non-string literal (number, etc.).
				emit(&out, highlight.KindNumber, v.Start, v.Token)
			case *bzlsyntax.StringExpr:
				// Label promotion: a string matching the Bazel label
				// grammar becomes KindLabel; everything else stays
				// KindString. The Span covers quotes; the Value is
				// the decoded text.
				kind := highlight.KindString
				if looksLikeLabel(v.Value) {
					kind = KindLabel
				}
				stringSpan(&out, kind, v.Start, v.End)
			}
		})
	}

	// Post-pass: promote identifiers to builtin/attribute as
	// warranted. Done after the walk so we don't need to track
	// CallExpr/keyword-argument context inside the visitor.
	promoteBuiltins(out)
	// Attribute promotion needs CallExpr context — do a second walk
	// rather than threading state through the first.
	for _, stmt := range file.Stmt {
		bzlsyntax.Walk(stmt, func(n bzlsyntax.Expr, stk []bzlsyntax.Expr) {
			if call, ok := n.(*bzlsyntax.CallExpr); ok {
				for _, arg := range call.List {
					if assign, ok := arg.(*bzlsyntax.AssignExpr); ok {
						if id, ok := assign.LHS.(*bzlsyntax.Ident); ok {
							promoteToAttribute(out, id.NamePos)
						}
					}
				}
			}
		})
	}

	highlight.SortByPosition(out)
	return out, err
}
