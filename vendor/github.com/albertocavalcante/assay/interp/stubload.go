package interp

import (
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/syntax"
)

// stubExternalLoads rewrites a .bzl source so external `load()`
// statements (those referencing an `@external_repo//...` label that
// won't resolve against the local workspace) become no-op stub
// bindings — one `name = None` per requested symbol. The rewritten
// source is then safe to hand to the interpreter: external loads
// don't abort the eval, in-module loads still run, and any rule
// definition that doesn't actually USE the stubbed symbols evaluates
// to completion.
//
// Why source-level: the Starlark frontend checks "name X exists in
// loaded module" AFTER our loader returns the StringDict. There's no
// public hook between Load returning and that check, so injecting
// stubs at the source layer is the simplest deterministic way to
// make load() statements transparent for introspection.
//
// Inputs:
//   - src: the original .bzl source bytes
//   - workspaceRoot: same root the interpreter's loader is rooted at;
//     used to decide which load labels CAN resolve (those without an
//     @repo prefix, plus @main).
//
// Output:
//   - rewritten source (always same length-or-larger; original bytes
//     for non-load lines are preserved byte-for-byte so line numbers
//     for downstream diagnostics stay close to the original)
//   - count of stubbed loads (for logging / test assertions)
//
// Determinism: rewriting touches ONLY top-level `load(...)` statements
// whose module string starts with `@`. Anything else — relative loads,
// `//...` loads, conditional loads inside function bodies (which
// aren't valid Starlark anyway), comments containing the word "load"
// — is untouched.
func stubExternalLoads(src []byte, filename string) ([]byte, int, error) {
	f, err := (&syntax.FileOptions{}).Parse(filename, src, syntax.RetainComments)
	if err != nil {
		// Parse errors mean we can't rewrite confidently — return the
		// original source and let the interpreter fail the same way it
		// would have. interp.evalCache logs + caches the failure.
		return src, 0, err
	}

	type loadEdit struct {
		startByte, endByte int
		stubLines          string
	}
	var edits []loadEdit
	for _, stmt := range f.Stmts {
		ls, ok := stmt.(*syntax.LoadStmt)
		if !ok {
			continue
		}
		moduleLit := ls.Module
		if moduleLit == nil {
			continue
		}
		moduleStr, ok := moduleLit.Value.(string)
		if !ok {
			continue
		}
		// Only stub loads we know won't resolve. Relative ("//pkg:...")
		// and bare ("file.bzl") loads stay in the source — the
		// in-workspace loader will handle them. The "main" repo is
		// implicit, so "@main//..." stays too.
		if !strings.HasPrefix(moduleStr, "@") {
			continue
		}
		if strings.HasPrefix(moduleStr, "@main//") || moduleStr == "@main" {
			continue
		}

		// Build the stub block. Each `to` name in `load(mod, to1, to2,
		// to3 = "from_name")` becomes `to = None`. The To slice
		// mirrors the local-binding identifiers Starlark would have
		// created.
		var stubs []string
		for _, to := range ls.To {
			if to == nil || to.Name == "" {
				continue
			}
			stubs = append(stubs, to.Name+" = None")
		}
		stubLines := strings.Join(stubs, "\n")

		startOff, endOff, err := stmtByteRange(src, ls)
		if err != nil {
			return src, 0, err
		}
		edits = append(edits, loadEdit{
			startByte: startOff,
			endByte:   endOff,
			stubLines: stubLines,
		})
	}

	if len(edits) == 0 {
		return src, 0, nil
	}

	// Apply edits back-to-front so earlier offsets stay valid as we
	// splice. Each edit replaces the full `load(...)` statement with
	// its stub block, padded with a trailing newline so the next
	// statement still starts on a fresh line.
	out := append([]byte(nil), src...)
	for _, e := range slices.Backward(edits) {
		replacement := []byte(e.stubLines)
		out = append(append(out[:e.startByte:e.startByte], replacement...), out[e.endByte:]...)
	}
	return out, len(edits), nil
}

// stmtByteRange returns the [start, end) byte offsets for the entire
// load statement. syntax.LoadStmt.Span() returns (Load, Rparen) where
// Rparen is the position of the ')' character itself, so we add 1 to
// the end to make the splice include the closing paren — otherwise
// the rewrite leaves a stray ')' behind on the line that used to be
// `load(...)`.
func stmtByteRange(src []byte, ls *syntax.LoadStmt) (start, end int, err error) {
	startPos, endPos := ls.Span()
	start, err = byteOffsetForPos(src, int(startPos.Line), int(startPos.Col))
	if err != nil {
		return 0, 0, err
	}
	end, err = byteOffsetForPos(src, int(endPos.Line), int(endPos.Col))
	if err != nil {
		return 0, 0, err
	}
	return start, end + 1, nil
}

// byteOffsetForPos translates a (1-based line, 1-based col) into the
// corresponding byte offset in src. Walks the source once per call —
// acceptable because the typical .bzl has a single-digit number of
// load statements.
func byteOffsetForPos(src []byte, line, col int) (int, error) {
	if line < 1 || col < 1 {
		return 0, fmt.Errorf("invalid 1-based position: line=%d col=%d", line, col)
	}
	curLine := 1
	for i := range src {
		if curLine == line {
			return i + (col - 1), nil
		}
		if src[i] == '\n' {
			curLine++
		}
	}
	if curLine == line {
		// Position points at EOF — clamp to last byte.
		return len(src), nil
	}
	return 0, fmt.Errorf("line %d out of range (source has %d lines)", line, curLine)
}
