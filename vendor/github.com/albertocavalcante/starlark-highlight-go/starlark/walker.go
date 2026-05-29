// Package starlark implements the highlight.Dialect for pure
// Starlark — the dialect used in .bzl and .star files outside of
// Bazel's BUILD/MODULE/WORKSPACE shells.
//
// This package depends on starlark-syntax-go/starlark (the synced
// Google parser) and on the framework types in starlark-highlight-go
// /highlight. It does NOT depend on Bazel's buildtools layer; any
// consumer that wants Bazel-flavored highlighting installs
// bazel-highlight-go separately.
package starlark

import (
	"strings"

	syntax "github.com/albertocavalcante/starlark-syntax-go/starlark"

	"github.com/albertocavalcante/starlark-highlight-go/highlight"
)

// Dialect is the highlight.Dialect implementation for pure Starlark
// (.bzl, .star). Stateless — usable as a zero value.
type Dialect struct{}

// Name satisfies highlight.Dialect.
func (Dialect) Name() string { return "starlark" }

// Match returns true for .bzl and .star file extensions.
//
// We intentionally do NOT match BUILD/MODULE.bazel/WORKSPACE — those
// belong to bazel-highlight-go's dialect, even though the pure
// Starlark parser would accept them. The point of having a Bazel-
// flavored dialect is that it knows about Bazel-specific semantics
// (labels, builtins) that the pure dialect doesn't.
func (Dialect) Match(filename string) bool {
	base := filename
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			base = base[i+1:]
			break
		}
	}
	return strings.HasSuffix(base, ".bzl") || strings.HasSuffix(base, ".star")
}

// Fallback for pure Starlark is the identity — every Kind we emit is
// already a core Kind.
func (Dialect) Fallback(k highlight.Kind) highlight.Kind { return k }

// Tokenize walks the parsed AST and emits a highlight.Token for
// every node type we recognize. Comments live on the commentsRef
// indirection (not in the Walk path) so we collect them per node,
// deduplicating by start position since the same comment can be
// reachable via multiple ancestor attachments.
func (Dialect) Tokenize(filename string, src []byte) ([]highlight.Token, error) {
	file, err := syntax.Parse(filename, src, syntax.RetainComments)
	if file == nil {
		return nil, err
	}

	out := make([]highlight.Token, 0, 64)
	commentSeen := make(map[positionKey]bool)
	// LoadStmt special-cases its From/To idents (renders as String
	// with quotes); this set tells the generic Ident handler below
	// to skip those positions so we don't double-emit.
	skipLoadIdent := make(map[positionKey]bool)

	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil {
			return false
		}

		if c := n.Comments(); c != nil {
			collectComments(&out, commentSeen, c.Before)
			collectComments(&out, commentSeen, c.Suffix)
			collectComments(&out, commentSeen, c.After)
		}

		switch v := n.(type) {
		case *syntax.DefStmt:
			emitKeyword(&out, v.Def, "def")
		case *syntax.IfStmt:
			if kw := keywordAt(src, v.If, []string{"if", "elif"}); kw != "" {
				emitKeyword(&out, v.If, kw)
			}
			if v.ElsePos.IsValid() {
				if kw := keywordAt(src, v.ElsePos, []string{"else", "elif"}); kw != "" {
					emitKeyword(&out, v.ElsePos, kw)
				}
			}
		case *syntax.ForStmt:
			emitKeyword(&out, v.For, "for")
			if v.Vars != nil && v.X != nil {
				if pos, ok := findKeywordBetween(src, syntax.End(v.Vars), syntax.Start(v.X), "in"); ok {
					emitKeyword(&out, pos, "in")
				}
			}
		case *syntax.WhileStmt:
			emitKeyword(&out, v.While, "while")
		case *syntax.LoadStmt:
			emitKeyword(&out, v.Load, "load")
			emittedLoadIdent := make(map[positionKey]bool)
			emitLoadIdent := func(id *syntax.Ident) {
				if id == nil {
					return
				}
				key := positionKey{id.NamePos.Line, id.NamePos.Col}
				if emittedLoadIdent[key] {
					return
				}
				emittedLoadIdent[key] = true
				off := byteOffset(src, int(id.NamePos.Line), int(id.NamePos.Col))
				if off <= 0 || off >= len(src) {
					return
				}
				q := src[off-1]
				if q != '"' && q != '\'' {
					return
				}
				start := syntax.MakePosition(nil, id.NamePos.Line, id.NamePos.Col-1)
				emit(&out, highlight.KindString, start, string(q)+id.Name+string(q))
			}
			for _, id := range v.From {
				emitLoadIdent(id)
			}
			for _, id := range v.To {
				emitLoadIdent(id)
			}
			for _, id := range v.From {
				if id != nil {
					skipLoadIdent[positionKey{id.NamePos.Line, id.NamePos.Col}] = true
				}
			}
			for _, id := range v.To {
				if id != nil {
					skipLoadIdent[positionKey{id.NamePos.Line, id.NamePos.Col}] = true
				}
			}
		case *syntax.ReturnStmt:
			emitKeyword(&out, v.Return, "return")
		case *syntax.BranchStmt:
			switch v.Token {
			case syntax.BREAK:
				emitKeyword(&out, v.TokenPos, "break")
			case syntax.CONTINUE:
				emitKeyword(&out, v.TokenPos, "continue")
			case syntax.PASS:
				emitKeyword(&out, v.TokenPos, "pass")
			}
		case *syntax.LambdaExpr:
			emitKeyword(&out, v.Lambda, "lambda")
		case *syntax.ForClause:
			emitKeyword(&out, v.For, "for")
			emitKeyword(&out, v.In, "in")
		case *syntax.IfClause:
			emitKeyword(&out, v.If, "if")
		case *syntax.Ident:
			if skipLoadIdent[positionKey{v.NamePos.Line, v.NamePos.Col}] {
				return true
			}
			switch v.Name {
			case "True", "False", "None":
				emitKeyword(&out, v.NamePos, v.Name)
			default:
				emit(&out, highlight.KindIdentifier, v.NamePos, v.Name)
			}
		case *syntax.Literal:
			switch v.Token {
			case syntax.STRING, syntax.BYTES:
				emit(&out, highlight.KindString, v.TokenPos, v.Raw)
			case syntax.INT, syntax.FLOAT:
				emit(&out, highlight.KindNumber, v.TokenPos, v.Raw)
			}
		}
		return true
	})

	highlight.SortByPosition(out)
	return out, err
}

// ---------------------------------------------------------------------------
// Helpers — exported below for bazel-highlight-go (and future custom dialects)
// to reuse. The Bazel dialect needs the same position math + keyword-at
// machinery; rather than duplicate, we promote them to package-public.
// ---------------------------------------------------------------------------

type positionKey struct{ line, col int32 }

func collectComments(out *[]highlight.Token, seen map[positionKey]bool, comments []syntax.Comment) {
	for _, c := range comments {
		key := positionKey{c.Start.Line, c.Start.Col}
		if seen[key] {
			continue
		}
		seen[key] = true
		emit(out, highlight.KindComment, c.Start, c.Text)
	}
}

// emit appends a token spanning `text` from `start`. Multi-line spans
// have their end computed by walking the bytes.
func emit(out *[]highlight.Token, kind highlight.Kind, start syntax.Position, text string) {
	if text == "" {
		return
	}
	endLine := int(start.Line)
	endCol := int(start.Col) - 1 + runeLen(text)
	if nl := strings.Count(text, "\n"); nl > 0 {
		endLine += nl
		lastNL := strings.LastIndex(text, "\n")
		endCol = runeLen(text[lastNL+1:])
	}
	*out = append(*out, highlight.Token{
		Kind:      kind,
		StartLine: int(start.Line),
		StartChar: int(start.Col) - 1,
		EndLine:   endLine,
		EndChar:   endCol,
	})
}

func emitKeyword(out *[]highlight.Token, pos syntax.Position, keyword string) {
	emit(out, highlight.KindKeyword, pos, keyword)
}

// keywordAt returns whichever candidate keyword matches the source at
// `pos`. Used to disambiguate AST positions whose node type alone
// doesn't tell us the spelling (IfStmt covers both `if` and `elif`).
func keywordAt(src []byte, pos syntax.Position, candidates []string) string {
	off := byteOffset(src, int(pos.Line), int(pos.Col))
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

// findKeywordBetween scans src[after, before) for `keyword` as a
// whole-word match. Used for keywords whose AST node doesn't carry
// their position directly (ForStmt's `in`).
func findKeywordBetween(src []byte, after, before syntax.Position, keyword string) (syntax.Position, bool) {
	startOff := byteOffset(src, int(after.Line), int(after.Col))
	endOff := byteOffset(src, int(before.Line), int(before.Col))
	if startOff < 0 {
		return syntax.Position{}, false
	}
	if endOff < 0 || endOff > len(src) {
		endOff = len(src)
	}
	if startOff >= endOff {
		return syntax.Position{}, false
	}
	region := src[startOff:endOff]
	for i := 0; i+len(keyword) <= len(region); i++ {
		if string(region[i:i+len(keyword)]) != keyword {
			continue
		}
		if i > 0 && isIdentChar(region[i-1]) {
			continue
		}
		if i+len(keyword) < len(region) && isIdentChar(region[i+len(keyword)]) {
			continue
		}
		line := int(after.Line)
		col := int(after.Col) + i
		for j := 0; j < i; j++ {
			if region[j] == '\n' {
				line++
				col = i - j
			}
		}
		return syntax.MakePosition(nil, int32(line), int32(col)), true
	}
	return syntax.Position{}, false
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
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
