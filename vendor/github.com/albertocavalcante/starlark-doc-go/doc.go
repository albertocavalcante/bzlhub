// Package doc parses Stardoc-style (Google-Python-flavored) docstrings
// commonly used by Bazel rules, macros, providers, and aspects.
//
// What this is
// ------------
// A small, focused parser that turns:
//
//   """Compile a binary.
//
//   This produces a wrapper script.
//
//   Args:
//       name: name of the rule.
//       srcs: list of source files.
//
//   Returns:
//       A struct with runfiles.
//   """
//
// into a structured Docstring with separate Summary / Description /
// Args / Returns / Examples / Deprecated / Note fields. Downstream
// renderers (canopy's UI, future docs generators, IDE plugins) can
// then display a real parameter table instead of a plain prose blob.
//
// What this is NOT
// ----------------
// - Not a Markdown renderer. Summary/Description text and arg/return
//   doc strings are returned verbatim — pass them through your
//   Markdown renderer of choice on the output side.
// - Not Bazel-aware. Cross-references like `[name](#name)`, label
//   conventions, and attribute-vs-rule disambiguation are intentionally
//   out of scope for this package — they belong in a `bazel-doc-go`
//   overlay that consumes a *Docstring and resolves further.
// - Not Sphinx- or NumPy-style. Stardoc only accepts Google style.
//   Other formats would need a different parser.
//
// Section detection
// -----------------
// A line of the form `<Label>:` at the LEFT MARGIN (no leading
// indentation) starts a new section. The body of the section is
// every subsequent line until the next left-margin section header
// or EOF, with the section's base indent stripped. Recognized
// labels (case-sensitive, like Stardoc itself): Args, Arguments,
// Parameters, Returns, Yields, Raises, Example, Examples,
// Deprecated, Note. Synonyms are normalized:
//
//   - Arguments / Parameters → Args
//   - Example / Examples     → Examples
package doc

import (
	"strings"
)

// Docstring is the structured form of a parsed Stardoc-style
// docstring. Fields are populated only when the corresponding
// section appears in the source — callers must check zero values
// rather than length to distinguish "section absent" from "section
// present but empty."
type Docstring struct {
	// Summary is the first paragraph: the one-line description.
	// Convention is a single sentence ending in a period, but the
	// parser doesn't enforce that — whatever the first paragraph
	// contains lands here.
	Summary string

	// Description is the prose between Summary and the first
	// structured section. Paragraph breaks are preserved as `\n\n`.
	Description string

	// Args lists the per-parameter entries from `Args:` /
	// `Arguments:` / `Parameters:`. Nil when no such section was
	// present. Length 0 vs nil is treated as equivalent.
	Args []Param

	// Returns holds the body of the `Returns:` section. Nil when
	// absent. When the section's first line looks type-shaped
	// (single identifier, optionally with brackets, followed by
	// `:`), it's split into Type + Doc.
	Returns *Return

	// Yields is the same shape as Returns, for generator-style fns.
	Yields *Return

	// Raises lists exceptions a function may raise. Rare in
	// Starlark but supported because Stardoc accepts the section.
	Raises []Raise

	// Examples lists `Example:` / `Examples:` bodies. Each example
	// is a single code block; multiple sections produce multiple
	// entries (we don't merge — preserving the author's grouping).
	Examples []Example

	// Deprecated is the body of the `Deprecated:` section. Empty
	// when absent. Common-case rendering: a banner at the top of
	// the symbol's card.
	Deprecated string

	// Note is the body of the `Note:` section. Same shape as
	// Deprecated; rendered as a callout / aside.
	Note string
}

// Param is one entry in an Args section.
type Param struct {
	Name string // parameter name (LHS of "name: doc" or "name (type): doc")
	Type string // optional inline type annotation; empty when not declared
	Doc  string // description text, with continuation lines joined by \n
}

// Return is the body of a Returns or Yields section.
type Return struct {
	Type string // optional declared type (before a leading ":" inside the body)
	Doc  string // description text
}

// Raise is one entry under Raises.
type Raise struct {
	Type string // exception type (LHS of "Type: doc")
	Doc  string // description
}

// Example is one Example/Examples section.
type Example struct {
	// Lang is the optional fenced-code-block language. Always empty
	// for indented blocks (the common Stardoc shape). Future
	// versions may recognize ```starlark fences and lift the lang.
	Lang string

	// Code is the example body with leading indentation stripped.
	Code string
}

// Parse interprets s as a Stardoc-style docstring and returns the
// structured form. Always non-nil; an empty docstring yields a
// zero-value Docstring.
func Parse(s string) *Docstring {
	d := &Docstring{}
	if s == "" {
		return d
	}

	// Normalize line endings.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Trim(s, "\n")

	lines := strings.Split(s, "\n")

	// Python `"""docstring"""` literals leave the FIRST line flush
	// with the opening quote but indent every subsequent line at the
	// source's `def`-level. The author wrote:
	//
	//     def foo():
	//         """Summary.
	//
	//         Args:
	//             name: doc.
	//         """
	//
	// which arrives as:
	//
	//     "Summary.\n\n    Args:\n        name: doc."
	//
	// Without dedent, section headers (`Args:` etc.) would never
	// match because they're at column 4, not 0. Compute the common
	// leading indent across lines 2+ and strip it so headers + body
	// align at the left margin the parser expects.
	if len(lines) > 1 {
		min := -1
		for _, line := range lines[1:] {
			if strings.TrimSpace(line) == "" {
				continue
			}
			n := 0
			for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
				n++
			}
			if min == -1 || n < min {
				min = n
			}
		}
		if min > 0 {
			for i := 1; i < len(lines); i++ {
				if len(lines[i]) >= min {
					lines[i] = lines[i][min:]
				}
			}
		}
	}

	// Phase 1: split into (preamble, sections). The preamble is
	// everything before the first left-margin section header.
	var preamble []string
	type rawSection struct {
		label string   // normalized label (Args/Returns/Examples/Deprecated/Note/Raises/Yields)
		body  []string // raw body lines, with leading common indent NOT yet stripped
	}
	var sections []rawSection
	var cur *rawSection

	for _, line := range lines {
		label, isHeader := sectionHeader(line)
		if isHeader {
			sections = append(sections, rawSection{label: label})
			cur = &sections[len(sections)-1]
			continue
		}
		if cur == nil {
			preamble = append(preamble, line)
		} else {
			cur.body = append(cur.body, line)
		}
	}

	// Phase 2: derive Summary + Description from preamble.
	d.Summary, d.Description = splitSummaryDescription(preamble)

	// Phase 3: per-section parsing.
	for _, sec := range sections {
		body := dedent(sec.body)
		switch sec.label {
		case "Args":
			d.Args = parseArgs(body)
		case "Returns":
			d.Returns = parseReturn(body)
		case "Yields":
			d.Yields = parseReturn(body)
		case "Raises":
			d.Raises = parseRaises(body)
		case "Examples":
			d.Examples = append(d.Examples, Example{Code: strings.Join(body, "\n")})
		case "Deprecated":
			d.Deprecated = joinTrimmed(body)
		case "Note":
			d.Note = joinTrimmed(body)
		}
	}

	return d
}

// sectionHeader checks whether line is a section header like
// "Args:" at the LEFT MARGIN. Returns the NORMALIZED label and
// true on match. Aliases (Arguments/Parameters → Args, Example →
// Examples) are folded so downstream switch cases stay simple.
func sectionHeader(line string) (string, bool) {
	// Header must have no leading whitespace and end with exactly ":"
	// (and nothing else — "Args: foo" is body, not a header).
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return "", false
	}
	if !strings.HasSuffix(line, ":") {
		return "", false
	}
	label := strings.TrimSuffix(line, ":")
	if label == "" {
		return "", false
	}
	switch label {
	case "Args", "Arguments", "Parameters":
		return "Args", true
	case "Returns":
		return "Returns", true
	case "Yields":
		return "Yields", true
	case "Raises":
		return "Raises", true
	case "Example", "Examples":
		return "Examples", true
	case "Deprecated":
		return "Deprecated", true
	case "Note":
		return "Note", true
	}
	return "", false
}

// splitSummaryDescription turns the preamble lines into a one-liner
// summary plus the remaining description. Both are trimmed of
// surrounding blank lines.
//
// Summary policy: the FIRST non-empty contiguous block of lines.
// One paragraph, joined with spaces if it spans multiple lines (a
// hand-wrapped summary should read as one sentence). Description
// is everything after that, with original paragraph breaks (`\n\n`)
// preserved.
func splitSummaryDescription(lines []string) (summary, description string) {
	// Skip leading blanks.
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return "", ""
	}
	// Collect summary lines until first blank line.
	var summaryLines []string
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
		summaryLines = append(summaryLines, strings.TrimSpace(lines[i]))
		i++
	}
	summary = strings.Join(summaryLines, " ")

	// Rest is description; trim trailing blanks.
	rest := lines[i:]
	for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
		rest = rest[1:]
	}
	for len(rest) > 0 && strings.TrimSpace(rest[len(rest)-1]) == "" {
		rest = rest[:len(rest)-1]
	}
	description = strings.Join(rest, "\n")
	return summary, description
}

// dedent strips the largest leading whitespace common to every
// non-blank line in body. Required because section bodies are
// indented relative to the section header in the source, but
// downstream renderers want the indent gone.
func dedent(body []string) []string {
	min := -1
	for _, line := range body {
		if strings.TrimSpace(line) == "" {
			continue
		}
		count := 0
		for count < len(line) && (line[count] == ' ' || line[count] == '\t') {
			count++
		}
		if min == -1 || count < min {
			min = count
		}
	}
	if min <= 0 {
		// Either body is all blank, or no common indent to strip.
		return body
	}
	out := make([]string, len(body))
	for i, line := range body {
		if len(line) >= min {
			out[i] = line[min:]
		} else {
			out[i] = line
		}
	}
	return out
}

// parseArgs interprets a dedented Args block. Each top-level entry
// starts on a line with NO leading whitespace (the dedent pass
// already stripped the section's base indent). Continuation lines
// belonging to a previous entry are recognized by having ANY
// remaining leading whitespace.
func parseArgs(body []string) []Param {
	var out []Param
	var cur *Param
	var contLines []string

	flush := func() {
		if cur == nil {
			return
		}
		if len(contLines) > 0 {
			joined := strings.Join(contLines, "\n")
			if cur.Doc == "" {
				cur.Doc = joined
			} else {
				cur.Doc = cur.Doc + "\n" + joined
			}
			contLines = nil
		}
		out = append(out, *cur)
		cur = nil
	}

	for _, line := range body {
		if strings.TrimSpace(line) == "" {
			// Blank line ends the current arg's continuation block;
			// it doesn't itself start a new arg.
			if cur != nil {
				flush()
			}
			continue
		}
		isContinuation := line[0] == ' ' || line[0] == '\t'
		if isContinuation {
			if cur != nil {
				contLines = append(contLines, strings.TrimSpace(line))
			}
			// Continuation before any arg — skip (Stardoc treats
			// this as malformed; ignoring is the safe choice).
			continue
		}
		// New arg line.
		flush()
		name, typ, docText := splitArgLine(line)
		cur = &Param{Name: name, Type: typ, Doc: docText}
	}
	flush()
	return out
}

// splitArgLine parses one of:
//
//   name: doc
//   name (type): doc
//
// Returns name, type (or empty), and doc. Leading/trailing
// whitespace on doc is trimmed.
func splitArgLine(line string) (name, typ, docText string) {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return strings.TrimSpace(line), "", ""
	}
	lhs := strings.TrimSpace(line[:colon])
	rhs := strings.TrimSpace(line[colon+1:])

	// Type annotation: "name (type)".
	if open := strings.Index(lhs, "("); open >= 0 && strings.HasSuffix(lhs, ")") {
		name = strings.TrimSpace(lhs[:open])
		typ = strings.TrimSpace(lhs[open+1 : len(lhs)-1])
		return name, typ, rhs
	}
	return lhs, "", rhs
}

// parseReturn interprets the body of a Returns / Yields section.
// First non-blank line MAY lead with "Type: doc"; subsequent lines
// extend the doc body.
func parseReturn(body []string) *Return {
	// Collapse to non-blank lines + joined with spaces — Returns
	// is conventionally a single prose paragraph.
	var lines []string
	for _, line := range body {
		t := strings.TrimSpace(line)
		if t == "" {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			continue
		}
		lines = append(lines, t)
	}
	// Trim trailing blank entries the loop may have appended
	// (blank lines between sections sometimes leak into the body).
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	joined := strings.TrimSpace(strings.Join(lines, " "))

	// Type detection: first line is "Type: rest" where Type is one
	// token (identifier with optional [brackets]) — be conservative
	// so we don't capture a real sentence colon.
	first := lines[0]
	if colon := strings.Index(first, ":"); colon > 0 {
		candidate := strings.TrimSpace(first[:colon])
		if looksLikeType(candidate) {
			restOfFirst := strings.TrimSpace(first[colon+1:])
			rest := append([]string{restOfFirst}, lines[1:]...)
			return &Return{Type: candidate, Doc: strings.TrimSpace(strings.Join(rest, " "))}
		}
	}
	return &Return{Doc: joined}
}

// looksLikeType screens for tokens that plausibly name a return
// type: a single identifier optionally with brackets like
// "list[str]" or "dict[str, int]". No spaces (would indicate prose).
func looksLikeType(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	// Allow letters, digits, underscores, dots, brackets, commas.
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '[' || r == ']' || r == ',':
		default:
			return false
		}
	}
	return true
}

// parseRaises interprets a Raises section. Same shape as Args but
// the LHS is an exception type, not a parameter name.
func parseRaises(body []string) []Raise {
	params := parseArgs(body) // identical layout rules
	out := make([]Raise, 0, len(params))
	for _, p := range params {
		out = append(out, Raise{Type: p.Name, Doc: p.Doc})
	}
	return out
}

// joinTrimmed joins lines into a single string, preserving blank
// lines as `\n\n` paragraph breaks. Used for Deprecated/Note,
// which are short prose blocks.
func joinTrimmed(body []string) string {
	// Trim leading/trailing blanks; preserve internal blanks.
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	return strings.Join(body, "\n")
}
