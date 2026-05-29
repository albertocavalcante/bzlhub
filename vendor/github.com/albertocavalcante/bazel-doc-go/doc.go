// Package bazeldoc enriches Stardoc-parsed docstrings with Bazel-
// specific awareness — label extraction and intra-doc cross-
// references — that the pure starlark-doc-go parser intentionally
// leaves out.
//
// Composition shape
// -----------------
// bazel-doc-go is the doc-string analogue of bazel-highlight-go's
// relationship to starlark-highlight-go: a small overlay that
// consumes the dialect-agnostic output and lifts domain entities.
// Callers can render either the plain *Docstring (no Bazel
// awareness) or the *Enriched form (with hyperlinkable labels and
// cross-refs) depending on what the surface needs.
//
// What's recognized
// -----------------
//   - Bazel labels: @repo//pkg:target, //pkg:target, :target,
//     @@canonical_repo//pkg/sub:target
//   - Stardoc xrefs: [name](#name) — the convention Stardoc uses
//     to link from prose into the args/returns table of the same
//     symbol's docstring
//
// What's NOT recognized
// ---------------------
//   - Bare rule names dropped into prose. "cc_binary depends on..."
//     could be the rule or a sentence fragment; without a symbol
//     resolver the false-positive rate is too high. If you have a
//     resolver, post-process Enriched.Refs yourself.
//   - Markdown links to http(s) URLs — that's the renderer's job.
//   - Sphinx/RST roles (`:ref:`, `:func:`). Stardoc doesn't use
//     them and we don't want to.
package bazeldoc

import (
	"regexp"
	"strings"

	sd "github.com/albertocavalcante/starlark-doc-go"
)

// RefKind classifies what kind of Bazel entity a Ref points to.
type RefKind int

const (
	// RefLabel identifies a Bazel label expression. The Label field
	// is populated with the parsed components.
	RefLabel RefKind = iota
	// RefXref identifies a Stardoc-style [name](#name) intra-doc
	// link. The XrefName field carries the anchor name (without
	// the leading '#').
	RefXref
)

// Ref is one recognized Bazel-aware reference inside a docstring
// field. Field+Offset together tell the renderer where in the
// source text to splice a hyperlink.
type Ref struct {
	Kind RefKind

	// Text is the raw matched substring (e.g. "@bazel_skylib//rules:copy_file.bzl"
	// or "[name](#name)"). The renderer should use Text as the
	// visible token to replace with a link.
	Text string

	// Label is non-nil when Kind == RefLabel.
	Label *Label

	// XrefName is non-empty when Kind == RefXref (the part after '#').
	XrefName string

	// Field names the Docstring field this Ref was extracted from.
	// Stable, machine-parseable form:
	//
	//   "Summary"
	//   "Description"
	//   "Args[<name>].Doc"
	//   "Returns.Doc"
	//   "Yields.Doc"
	//   "Raises[<type>].Doc"
	//   "Examples[<index>].Code"
	//   "Deprecated"
	//   "Note"
	Field string

	// Offset is the byte offset within Field where Text begins.
	Offset int
}

// Label is a structured Bazel label.
//
// Examples and how they parse:
//
//	"@bazel_skylib//rules:common_settings.bzl"
//	  Repo="@bazel_skylib", Package="rules", Target="common_settings.bzl"
//
//	"//foo/bar:baz"
//	  Repo="", Package="foo/bar", Target="baz"
//
//	"//foo/bar"
//	  Repo="", Package="foo/bar", Target="bar"   (implicit target)
//
//	":local"
//	  Repo="", Package="", Target="local"
//
//	"@@canonical_repo//pkg"
//	  Repo="@@canonical_repo", Package="pkg", Target="pkg"
type Label struct {
	// Repo is the repository prefix including its leading '@' or
	// '@@'. Empty for same-repo labels.
	Repo string

	// Package is the slash-separated package path, without the
	// leading "//" or trailing ":".
	Package string

	// Target is the target name after ':'. When the source omits
	// ':' the implicit target name (basename of Package) is filled
	// in here for convenience.
	Target string

	// Raw is the original text exactly as it appeared.
	Raw string
}

// Enriched is a Stardoc Docstring plus extracted Bazel references.
// The embedded *Docstring is the same pointer the caller passed in
// (or that Parse produced); Enrich does not copy or mutate it.
type Enriched struct {
	*sd.Docstring

	// Refs are all Bazel-aware references discovered across the
	// docstring's text fields, in deterministic field-then-offset
	// order. Nil when nothing was found.
	Refs []Ref
}

// Parse parses raw Stardoc text via starlark-doc-go and then
// enriches the result. Equivalent to Enrich(sd.Parse(s)).
func Parse(s string) *Enriched {
	return Enrich(sd.Parse(s))
}

// Enrich walks the fields of an already-parsed Docstring and
// returns the enriched form. d must be non-nil (use sd.Parse for
// empty input — it returns a zero-value Docstring, not nil).
func Enrich(d *sd.Docstring) *Enriched {
	out := &Enriched{Docstring: d}
	if d == nil {
		return out
	}

	scan := func(field, text string) {
		out.Refs = append(out.Refs, scanRefs(field, text)...)
	}

	scan("Summary", d.Summary)
	scan("Description", d.Description)
	for _, p := range d.Args {
		scan("Args["+p.Name+"].Doc", p.Doc)
	}
	if d.Returns != nil {
		scan("Returns.Doc", d.Returns.Doc)
	}
	if d.Yields != nil {
		scan("Yields.Doc", d.Yields.Doc)
	}
	for _, r := range d.Raises {
		scan("Raises["+r.Type+"].Doc", r.Doc)
	}
	for i, ex := range d.Examples {
		scan("Examples["+itoa(i)+"].Code", ex.Code)
	}
	scan("Deprecated", d.Deprecated)
	scan("Note", d.Note)

	return out
}

// scanRefs runs both pattern scanners over text and returns refs
// in ascending offset order. Overlapping matches are resolved by
// preferring the earlier-starting one (label vs xref can't really
// overlap, but the merge is cheap and keeps things robust).
func scanRefs(field, text string) []Ref {
	if text == "" {
		return nil
	}
	var refs []Ref
	for _, m := range labelRe.FindAllStringIndex(text, -1) {
		raw := text[m[0]:m[1]]
		lbl := parseLabel(raw)
		if lbl == nil {
			continue
		}
		// The regex captures a leading delimiter (space, quote, paren)
		// to anchor the //pkg and :target branches; parseLabel trims
		// it for Label.Raw. Align Text + Offset with the trimmed
		// view so consumers don't see stray punctuation when they
		// render Text in tooltips or splice at Offset.
		off := m[0] + (len(raw) - len(lbl.Raw))
		refs = append(refs, Ref{
			Kind:   RefLabel,
			Text:   lbl.Raw,
			Label:  lbl,
			Field:  field,
			Offset: off,
		})
	}
	for _, m := range xrefRe.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[0]:m[1]]
		name := text[m[2]:m[3]]
		refs = append(refs, Ref{
			Kind:     RefXref,
			Text:     raw,
			XrefName: name,
			Field:    field,
			Offset:   m[0],
		})
	}
	sortByOffset(refs)
	return refs
}

// labelRe matches three label shapes:
//
//   - @repo//pkg[:target]      (including @@canonical)
//   - //pkg[:target]
//   - :target                  (relative label, only when preceded by
//                              whitespace or string-start so URLs
//                              like "http://" don't match the bare
//                              "//" branch)
//
// Capture groups intentionally absent — we re-parse the matched
// text in parseLabel to keep the regex small and the structured
// fields explicit.
var labelRe = regexp.MustCompile(
	`@@?[A-Za-z_][A-Za-z0-9_.-]*//[A-Za-z0-9_./+-]*(?::[A-Za-z0-9_./+-]+)?` +
		`|(?:\A|[\s(\[\x60'"])//[A-Za-z0-9_./+-]+(?::[A-Za-z0-9_./+-]+)?` +
		`|(?:\A|[\s(\[\x60'"]):[A-Za-z_][A-Za-z0-9_.+-]*`,
)

// xrefRe matches Stardoc's [name](#name) intra-doc link.
var xrefRe = regexp.MustCompile(`\[[^\]\n]+\]\(#([A-Za-z_][A-Za-z0-9_]*)\)`)

// parseLabel turns matched label text into a structured Label.
// Returns nil when the text fails the shape check (e.g. a leading
// whitespace char captured by the "//pkg" alternative).
func parseLabel(raw string) *Label {
	// Strip a leading delimiter the regex may have captured for
	// the "//pkg" / ":target" branches. The label itself starts
	// at '@', '/', or ':'.
	trimmed := strings.TrimLeft(raw, " \t(\x60'\"[")
	if trimmed == "" {
		return nil
	}
	if trimmed != raw {
		// Mismatched leading characters mean the actual label is
		// shorter than the regex match. Update the caller's view
		// by trimming our own copy and using that as Raw.
		raw = trimmed
	}

	lbl := &Label{Raw: raw}

	rest := raw
	switch {
	case strings.HasPrefix(rest, "@"):
		// Repo prefix: @name or @@name.
		end := indexAny(rest, "/:")
		if end < 0 {
			return nil
		}
		lbl.Repo = rest[:end]
		rest = rest[end:]
	}

	switch {
	case strings.HasPrefix(rest, "//"):
		rest = rest[2:]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			lbl.Package = rest[:colon]
			lbl.Target = rest[colon+1:]
		} else {
			lbl.Package = rest
			lbl.Target = basename(rest)
		}
	case strings.HasPrefix(rest, ":"):
		lbl.Target = rest[1:]
	default:
		// Repo-only label like "@io_bazel" with no //pkg — not
		// commonly used in docstrings; reject for now.
		return nil
	}

	if lbl.Target == "" {
		return nil
	}
	return lbl
}

// indexAny returns the index of the first byte in s that is one of
// the chars in chars, or -1.
func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}

// basename returns the final '/'-separated component of pkg.
func basename(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}

// sortByOffset orders refs by ascending Offset. Stable so equal-
// offset entries (unlikely in practice) keep their scan order.
func sortByOffset(refs []Ref) {
	// Insertion sort — N is tiny (single-digit per field).
	for i := 1; i < len(refs); i++ {
		j := i
		for j > 0 && refs[j-1].Offset > refs[j].Offset {
			refs[j-1], refs[j] = refs[j], refs[j-1]
			j--
		}
	}
}

// itoa avoids pulling strconv into the public dependency surface.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
