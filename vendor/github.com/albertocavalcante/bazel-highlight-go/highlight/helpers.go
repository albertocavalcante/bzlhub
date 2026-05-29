package highlight

import (
	"strings"

	bzlsyntax "github.com/albertocavalcante/starlark-syntax-go/bzl"

	"github.com/albertocavalcante/starlark-highlight-go/highlight"
)

// positionKey hashes a bzl.Position for dedup.
type positionKey struct{ line, col int }

func collectComments(out *[]highlight.Token, seen map[positionKey]bool, comments []bzlsyntax.Comment) {
	for _, c := range comments {
		key := positionKey{c.Start.Line, c.Start.LineRune}
		if seen[key] {
			continue
		}
		seen[key] = true
		emit(out, highlight.KindComment, c.Start, c.Token)
	}
}

// emit appends a token spanning `text` from `start`. Multi-line spans
// have their end computed by walking the bytes. Same logic as the
// pure-Starlark dialect's emit; duplicated rather than shared because
// the Position types are package-distinct.
func emit(out *[]highlight.Token, kind highlight.Kind, start bzlsyntax.Position, text string) {
	if text == "" {
		return
	}
	startLine := start.Line
	startChar := start.LineRune - 1
	endLine := startLine
	endChar := startChar + runeLen(text)
	if hasNewline(text) {
		endLine = startLine + countNewlines(text)
		lastNL := lastNewline(text)
		endChar = runeLen(text[lastNL+1:])
	}
	*out = append(*out, highlight.Token{
		Kind:      kind,
		StartLine: startLine,
		StartChar: startChar,
		EndLine:   endLine,
		EndChar:   endChar,
	})
}

// emitBuiltin is emit() for a curated Bazel builtin. Adds Meta with
// the canonical name, doc URL, and description so renderers can
// surface a hover/tooltip without a parallel lookup table.
func emitBuiltin(out *[]highlight.Token, start bzlsyntax.Position, name string, info BuiltinInfo) {
	tok := highlight.Token{
		Kind:      KindBuiltin,
		StartLine: start.Line,
		StartChar: start.LineRune - 1,
		EndLine:   start.Line,
		EndChar:   start.LineRune - 1 + runeLen(name),
		Meta: map[string]string{
			"name":        info.Name,
			"url":         info.URL,
			"description": info.Description,
		},
	}
	*out = append(*out, tok)
}

// stringSpan emits a String/Label token whose exact source range is
// known from a StringExpr's Start/End positions (quotes included).
func stringSpan(out *[]highlight.Token, kind highlight.Kind, start, end bzlsyntax.Position) {
	*out = append(*out, highlight.Token{
		Kind:      kind,
		StartLine: start.Line,
		StartChar: start.LineRune - 1,
		EndLine:   end.Line,
		EndChar:   end.LineRune - 1,
	})
}

// keywordAt: same role as the pure-Starlark dialect's helper — peek
// the source byte to disambiguate AST positions that share a node
// type (IfStmt: if vs elif, ElsePos: else vs elif).
func keywordAt(src []byte, pos bzlsyntax.Position, candidates []string) string {
	off := byteOffset(src, pos.Line, pos.LineRune)
	if off < 0 {
		return ""
	}
	for _, c := range candidates {
		if off+len(c) <= len(src) && string(src[off:off+len(c)]) == c {
			return c
		}
	}
	return ""
}

func byteOffset(src []byte, line, col int) int {
	if line < 1 || col < 1 {
		return -1
	}
	cur := 0
	for l := 1; l < line; l++ {
		i := indexByte(src[cur:], '\n')
		if i < 0 {
			return -1
		}
		cur += i + 1
	}
	target := cur + col - 1
	if target > len(src) {
		return -1
	}
	return target
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func hasNewline(s string) bool {
	return strings.Contains(s, "\n")
}

func countNewlines(s string) int {
	return strings.Count(s, "\n")
}

func lastNewline(s string) int {
	return strings.LastIndex(s, "\n")
}

// looksLikeLabel returns true when `s` matches the shape of a Bazel
// label. The accepted forms are:
//
//   //pkg:target          — same-repo absolute
//   //pkg                 — same-repo, implicit target=pkg leaf
//   //pkg/sub             — nested package
//   @repo//pkg:target     — cross-repo
//   @repo                 — cross-repo, implicit
//   :target               — relative-to-current-package
//
// We're permissive here intentionally — false positives degrade
// gracefully (a string accidentally rendered as a label still
// displays as text; the only visible difference is the CSS class).
// The strict label grammar lives in bazel-syntax-go/bzl/labels.
func looksLikeLabel(s string) bool {
	if s == "" {
		return false
	}
	switch s[0] {
	case '/':
		return len(s) >= 2 && s[1] == '/'
	case '@':
		return true
	case ':':
		return len(s) >= 2 && isLabelChar(s[1])
	}
	return false
}

func isLabelChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '-' || c == '.' || c == '/' || c == ':'
}

// promoteBuiltins walks the existing tokens and upgrades any
// Identifier whose value is in the curated Bazel builtins registry
// to a Builtin token with Meta. We don't have token text at this
// point (Token doesn't carry it), so we keep a parallel index of
// emitted identifier names alongside positions in the registry
// metadata lookup.
//
// In practice the post-pass needs to know which Identifier tokens
// correspond to which AST Idents. We approximate by re-walking
// would-be cheaper, but the simpler API is for the caller to track
// names; for now the registry stores name → BuiltinInfo and the
// caller of promoteBuiltins is the Tokenize body which knows names.
// We expose helpers so tests + future maintainers can reason about
// the post-pass without re-reading the walker.
func promoteBuiltins(out []highlight.Token) {
	// Builtin promotion is handled by tokenizeStarlark-style
	// post-passes that DO know names. The current implementation
	// performs builtin lookup inside the Ident case in dialect.go
	// — but we kept the visitor lean. This stub is reserved for a
	// future pass that walks the rendered token stream alongside
	// the source bytes; today builtin promotion is name-driven from
	// inside the walker via promoteIdentToBuiltin.
	_ = out
}

// promoteToAttribute upgrades the Identifier token at `pos` to an
// Attribute. Linear scan over `out` is fine — attribute promotion
// runs once per CallExpr in a small per-file set.
func promoteToAttribute(out []highlight.Token, pos bzlsyntax.Position) {
	targetLine := pos.Line
	targetChar := pos.LineRune - 1
	for i := range out {
		if out[i].Kind != highlight.KindIdentifier {
			continue
		}
		if out[i].StartLine == targetLine && out[i].StartChar == targetChar {
			out[i].Kind = KindAttribute
			return
		}
	}
}
