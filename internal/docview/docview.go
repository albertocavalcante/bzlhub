// Package docview builds presentation-ready doc-string views from
// bazel-doc-go's *Enriched output. Frontends should consume the
// Doc value directly — every URL, every dedup'd chip, every "is
// this navigable?" decision is made here so the UI iterates and
// renders without computation.
//
// The split mirrors the project rule that compute lives server-
// side: backend ships hrefs / labels / titles already resolved,
// the SvelteKit layer is presentation-only.
package docview

import (
	"strings"

	bazeldoc "github.com/albertocavalcante/bazel-doc-go"
	doc "github.com/albertocavalcante/starlark-doc-go"
)

// LinkResolver builds canopy URLs from Bazel labels. Implementations
// know which paths are routable at this canopy install (only
// `/modules/<name>` for the module landing, plus per-(module,
// version) code-nav for same-repo file labels).
type LinkResolver interface {
	// ModuleHref returns the URL for the module landing page or "".
	ModuleHref(name string) string

	// CodeNavFileHref returns the URL for a file in the given
	// module's code-nav viewer, or "" when not navigable.
	CodeNavFileHref(module, version, file string) string
}

// Owner is the (module, version) of the docstring being viewed.
// Same-repo //pkg:file labels resolve against this coordinate.
// An empty Module signals "no owner context" (e.g. the doc was
// rendered out-of-page), in which case same-repo labels won't
// resolve and stay un-linked.
type Owner struct {
	Module  string
	Version string
}

// Doc is the presentation-ready shape. JSON-serializable, mirrors
// what the UI iterates over. Fields use the same names as
// starlark-doc-go's Docstring + bazeldoc.Enriched so the frontend
// type can stay close to the existing ParsedDoc interface.
type Doc struct {
	Summary     string         `json:"Summary,omitempty"`
	Description string         `json:"Description,omitempty"`
	Args        []doc.Param    `json:"Args,omitempty"`
	Returns     *doc.Return    `json:"Returns,omitempty"`
	Yields      *doc.Return    `json:"Yields,omitempty"`
	Raises      []doc.Raise    `json:"Raises,omitempty"`
	Examples    []doc.Example  `json:"Examples,omitempty"`
	Deprecated  string         `json:"Deprecated,omitempty"`
	Note        string         `json:"Note,omitempty"`

	// Refs are the in-prose label references, each carrying the
	// resolved Href when one exists (empty when the label has no
	// navigable destination, or when its position inside the
	// source text would corrupt a splice). The frontend splices
	// these into the Markdown source verbatim.
	Refs []Ref `json:"Refs,omitempty"`

	// Chips is the deduplicated, pre-filtered list of footer
	// chips for this docstring. Already filtered to only those
	// with a resolved Href, so the UI just iterates and renders.
	Chips []Chip `json:"Chips,omitempty"`
}

// Ref mirrors bazeldoc.Ref but adds Href (the resolved destination
// or "" when not linkable) and Splice (whether the frontend should
// substitute [Text](Href) at Offset when rendering the source
// field's Markdown). Splice is false even for resolvable refs when
// the offset sits inside a code fence / inline code span / existing
// link — splicing there would corrupt the rendered output.
type Ref struct {
	Kind     int             `json:"Kind"`
	Text     string          `json:"Text"`
	Label    *bazeldoc.Label `json:"Label,omitempty"`
	XrefName string          `json:"XrefName,omitempty"`
	Field    string          `json:"Field"`
	Offset   int             `json:"Offset"`
	Href     string          `json:"Href,omitempty"`
	Splice   bool            `json:"Splice,omitempty"`
}

// Chip is one entry in the footer "referenced" row. All three
// fields are display-ready: Label is the visible chip text, Href
// is the destination, Title is the hover tooltip.
type Chip struct {
	Label string `json:"label"`
	Href  string `json:"href"`
	Title string `json:"title"`
}

// fileTargetSuffixes lists target-name shapes we'll deep-link to
// code-nav. Anything else (a rule target like ":my_test") has no
// useful navigation destination from a doc context.
var fileTargetSuffixes = []string{".bzl", ".bazel", ".star", ".sky"}

// fileTargetExact lists target names that are themselves files
// rather than file suffixes.
var fileTargetExact = []string{"BUILD", "BUILD.bazel", "MODULE.bazel", "WORKSPACE", "WORKSPACE.bazel", "WORKSPACE.bzlmod"}

// Build converts a *bazeldoc.Enriched into the presentation-ready
// Doc. owner names the module being viewed (used to resolve same-
// repo //pkg labels); resolver builds canopy URLs. Either nil
// input or a Docstring with no fields yields nil so the caller's
// "omit when empty" logic still works.
func Build(e *bazeldoc.Enriched, owner Owner, resolver LinkResolver) *Doc {
	if e == nil || e.Docstring == nil {
		return nil
	}
	d := e.Docstring
	out := &Doc{
		Summary:     d.Summary,
		Description: d.Description,
		Args:        d.Args,
		Returns:     d.Returns,
		Yields:      d.Yields,
		Raises:      d.Raises,
		Examples:    d.Examples,
		Deprecated:  d.Deprecated,
		Note:        d.Note,
	}

	// Field-name → source text, so the splice-safety check can
	// look at the bytes immediately around each ref's offset.
	fieldSrc := buildFieldSrc(d)

	chipSeen := map[string]struct{}{}
	for _, r := range e.Refs {
		href := ""
		if r.Kind == bazeldoc.RefLabel {
			href = resolveLabelHref(r, owner, resolver)
		}
		// Splice-eligibility is a separate decision from "does
		// this ref have a destination?" A label cited inside a
		// fenced code block still navigates somewhere useful
		// (chip footer) — we just can't splice it inline without
		// corrupting the Markdown around it.
		splice := href != "" && canSplice(fieldSrc[r.Field], r)
		out.Refs = append(out.Refs, Ref{
			Kind:     int(r.Kind),
			Text:     r.Text,
			Label:    r.Label,
			XrefName: r.XrefName,
			Field:    r.Field,
			Offset:   r.Offset,
			Href:     href,
			Splice:   splice,
		})
		if href == "" {
			continue
		}
		if _, ok := chipSeen[href]; ok {
			continue
		}
		chipSeen[href] = struct{}{}
		out.Chips = append(out.Chips, chipFor(r, href))
	}

	return out
}

// resolveLabelHref maps a label ref to a canopy URL or "".
//   - @repo prefix → /modules/<repo> (we don't know the latest
//     version of an arbitrary referenced module at render time)
//   - same-repo //pkg:file.bzl → owning module's code-nav
//   - everything else (bare :target, same-repo //pkg without
//     a file-shaped target) → "" (no useful destination)
func resolveLabelHref(r bazeldoc.Ref, owner Owner, resolver LinkResolver) string {
	if r.Label == nil || resolver == nil {
		return ""
	}
	lbl := r.Label

	if lbl.Repo != "" {
		moduleName := strings.TrimLeft(lbl.Repo, "@")
		if moduleName == "" {
			return ""
		}
		// Self-reference: @aspect_bazel_lib//lib:foo.bzl inside
		// aspect_bazel_lib's own docs. We know our (module, version)
		// AND we know the target file, so deep-link past the module
		// landing straight to code-nav.
		if moduleName == owner.Module && owner.Version != "" && looksLikeFileTarget(lbl.Target) {
			file := lbl.Target
			if lbl.Package != "" {
				file = lbl.Package + "/" + lbl.Target
			}
			return resolver.CodeNavFileHref(owner.Module, owner.Version, file)
		}
		return resolver.ModuleHref(moduleName)
	}

	// Same-repo (no @repo prefix): need our own coordinate + file-shaped target.
	if owner.Module == "" || owner.Version == "" {
		return ""
	}
	if !looksLikeFileTarget(lbl.Target) {
		return ""
	}
	file := lbl.Target
	if lbl.Package != "" {
		file = lbl.Package + "/" + lbl.Target
	}
	return resolver.CodeNavFileHref(owner.Module, owner.Version, file)
}

func looksLikeFileTarget(target string) bool {
	if target == "" {
		return false
	}
	for _, ext := range fileTargetSuffixes {
		if strings.HasSuffix(target, ext) {
			return true
		}
	}
	for _, exact := range fileTargetExact {
		if target == exact {
			return true
		}
	}
	return false
}

// chipFor builds the display fields for one chip. The label is
// short and contextual: @repo for repo-prefixed labels, pkg/file
// for same-repo file references.
func chipFor(r bazeldoc.Ref, href string) Chip {
	label := r.Text
	if r.Label != nil {
		switch {
		case r.Label.Repo != "":
			label = "@" + strings.TrimLeft(r.Label.Repo, "@")
		case r.Label.Package != "":
			label = r.Label.Package + "/" + r.Label.Target
		default:
			label = r.Label.Target
		}
	}
	return Chip{
		Label: label,
		Href:  href,
		Title: "cited as " + r.Text,
	}
}

// buildFieldSrc reproduces the Field-name → source-text mapping
// that bazel-doc-go uses when scanning. Same naming convention as
// Enriched's walk so Ref.Field looks up correctly here.
func buildFieldSrc(d *doc.Docstring) map[string]string {
	src := map[string]string{
		"Summary":     d.Summary,
		"Description": d.Description,
		"Deprecated":  d.Deprecated,
		"Note":        d.Note,
	}
	for _, p := range d.Args {
		src["Args["+p.Name+"].Doc"] = p.Doc
	}
	if d.Returns != nil {
		src["Returns.Doc"] = d.Returns.Doc
	}
	if d.Yields != nil {
		src["Yields.Doc"] = d.Yields.Doc
	}
	for _, ra := range d.Raises {
		src["Raises["+ra.Type+"].Doc"] = ra.Doc
	}
	for i, ex := range d.Examples {
		src["Examples["+itoa(i)+"].Code"] = ex.Code
	}
	return src
}

// canSplice returns false when splicing at r.Offset would corrupt
// surrounding Markdown. Cases:
//
//  1. Adjacent backticks: `label` (inline code).
//  2. Inside a fenced code block: ```starlark ... ``` (count ``` runs
//     before offset — odd means we're inside an open fence).
//  3. Inside the text portion of an existing Markdown link: the last
//     '[' before the offset has no matching ']' yet.
func canSplice(src string, r bazeldoc.Ref) bool {
	if src == "" || r.Offset < 0 || r.Offset >= len(src) {
		return false
	}
	before := src[:r.Offset]
	end := r.Offset + len(r.Text)

	// (1) backtick-adjacency check
	prev := byte(0)
	if r.Offset > 0 {
		prev = src[r.Offset-1]
	}
	next := byte(0)
	if end < len(src) {
		next = src[end]
	}
	if prev == '`' || next == '`' {
		return false
	}

	// (2) fenced code block — odd count of "```" before offset
	// means we're inside an open fence.
	if strings.Count(before, "```")%2 == 1 {
		return false
	}

	// (3) existing Markdown link text — last unclosed '[' before
	// the offset (covers both "[ref...]" and "[...ref...]" cases,
	// not just the immediate-prev '[' the earlier check caught).
	if strings.LastIndex(before, "[") > strings.LastIndex(before, "]") {
		return false
	}

	return true
}

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
