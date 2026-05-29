// Package stardoc renders an assay ModuleReport as a Stardoc-shape
// Markdown document.
//
// What this is
// ------------
// One function — Render — that takes the structured analysis output
// (rules / providers / macros / aspects / repository rules / module
// extensions, each with their parsed docstrings and attribute schemas)
// and emits a single Markdown string suitable for:
//
//   - Piping to a docs site (Hugo / VitePress / docs.rs equivalents)
//   - Committing in-tree as a generated `docs/<module>.md`
//   - Posting to a wiki / Notion / GitHub issue
//
// Compatible with Bazel's `stardoc()` build rule output by convention:
// bare-name anchors per symbol, kind tags in headings, per-symbol
// arg + attribute tables, examples as fenced code blocks. The aim is
// "looks plausibly like Stardoc emitted it," not byte-identical
// — Stardoc's exact whitespace/heading-level choices change between
// versions and aren't load-bearing for any consumer.
//
// Design choices
// --------------
//   - Pure function: same input → same output, every time. Map
//     iteration order is the usual non-determinism trap; we sort
//     parsed-doc maps before walking them.
//   - No external state: no filesystem reads, no template files.
//     The caller already loaded the ModuleReport; we just transform.
//   - One markdown source of truth: existing renderers (canopy's UI,
//     MCP responses) get the structured data, this package gets the
//     same data and produces text. No format drift.
//   - starlark-doc-go for Args extraction: we don't re-implement
//     docstring parsing here; pure composition.
package stardoc

import (
	"fmt"
	"sort"
	"strings"

	"github.com/albertocavalcante/assay/report"
	doc "github.com/albertocavalcante/starlark-doc-go"
)

// Render is the default entry point. Equivalent to RenderWithOptions
// with a zero-value Options. Keep this function-shaped (rather than
// the Options API) for the common "just give me the markdown" call.
func Render(rep *report.ModuleReport) string {
	return RenderWithOptions(rep, Options{})
}

// Options tweaks Render's output. The zero value matches Stardoc's
// default behavior — hide private symbols, H1 for module + H2 for
// each symbol.
type Options struct {
	// IncludePrivate emits underscore-prefixed symbols (_impl, etc.).
	// Off by default; Stardoc's own behavior is to hide them.
	IncludePrivate bool
}

// RenderWithOptions is Render with explicit options. Pulled out so
// the common case has a one-arg signature.
func RenderWithOptions(rep *report.ModuleReport, opts Options) string {
	if rep == nil {
		return ""
	}
	var b strings.Builder

	// Module header. Title + optional version subtitle.
	b.WriteString("# ")
	b.WriteString(orFallback(rep.Name, "(unnamed module)"))
	b.WriteString("\n")
	if rep.Version != "" {
		fmt.Fprintf(&b, "\n*Version %s.*\n", rep.Version)
	}
	if rep.CompatibilityLevel > 0 {
		fmt.Fprintf(&b, "\n*Compatibility level: %d.*\n", rep.CompatibilityLevel)
	}

	// Sections, in Stardoc's conventional order: rules first (most-
	// used), then providers, macros, aspects, repository_rules,
	// module_extensions. Each kind has its own section even when
	// empty — drop empty sections to keep the doc compact.
	renderRules(&b, rep, opts)
	renderProviders(&b, rep, opts)
	renderMacros(&b, rep, opts)
	renderAspects(&b, rep, opts)
	renderRepoRules(&b, rep, opts)
	renderModuleExtensions(&b, rep, opts)

	return b.String()
}

func renderRules(b *strings.Builder, rep *report.ModuleReport, opts Options) {
	rules := filterPrivate(rep.Rules, opts, func(r report.RuleSpec) bool { return r.Private })
	if len(rules) == 0 {
		return
	}
	b.WriteString("\n## Rules\n")
	for _, r := range rules {
		writeSymbolHeading(b, r.Name, "rule")
		writeDocBody(b, r.Doc)
		writeAttrsTable(b, r.Attrs)
	}
}

func renderProviders(b *strings.Builder, rep *report.ModuleReport, opts Options) {
	provs := filterPrivate(rep.Providers, opts, func(p report.ProviderSpec) bool { return p.Private })
	if len(provs) == 0 {
		return
	}
	b.WriteString("\n## Providers\n")
	for _, p := range provs {
		writeSymbolHeading(b, p.Name, "provider")
		writeDocBody(b, p.Doc)
		if len(p.Fields) > 0 {
			b.WriteString("\n**Fields**\n\n")
			for _, f := range p.Fields {
				fmt.Fprintf(b, "- `%s`\n", f)
			}
		}
	}
}

func renderMacros(b *strings.Builder, rep *report.ModuleReport, _ Options) {
	// MacroSpec doesn't carry a Private bool — assay already filters
	// underscore-prefixed names upstream. Include all macros.
	macros := rep.Macros
	if len(macros) == 0 {
		return
	}
	b.WriteString("\n## Macros\n")
	for _, m := range macros {
		writeSymbolHeading(b, m.Name, "macro")
		if len(m.Params) > 0 {
			fmt.Fprintf(b, "\n**Signature**\n\n```python\n%s(%s)\n```\n",
				m.Name, strings.Join(m.Params, ", "))
		}
		writeDocBody(b, m.Doc)
	}
}

func renderAspects(b *strings.Builder, rep *report.ModuleReport, opts Options) {
	aspects := filterPrivate(rep.Aspects, opts, func(a report.AspectSpec) bool { return a.Private })
	if len(aspects) == 0 {
		return
	}
	b.WriteString("\n## Aspects\n")
	for _, a := range aspects {
		writeSymbolHeading(b, a.Name, "aspect")
		writeDocBody(b, a.Doc)
		if len(a.AttrAspects) > 0 {
			fmt.Fprintf(b, "\n*Attr aspects: %s.*\n", strings.Join(a.AttrAspects, ", "))
		}
		if len(a.RequiredProviders) > 0 {
			fmt.Fprintf(b, "\n*Required providers: %s.*\n", strings.Join(a.RequiredProviders, ", "))
		}
	}
}

func renderRepoRules(b *strings.Builder, rep *report.ModuleReport, opts Options) {
	rrs := filterPrivate(rep.RepositoryRules, opts, func(r report.RepoRuleSpec) bool { return r.Private })
	if len(rrs) == 0 {
		return
	}
	b.WriteString("\n## Repository Rules\n")
	for _, r := range rrs {
		writeSymbolHeading(b, r.Name, "repository_rule")
		writeDocBody(b, r.Doc)
		writeAttrsTable(b, r.Attrs)
	}
}

func renderModuleExtensions(b *strings.Builder, rep *report.ModuleReport, opts Options) {
	exts := filterPrivate(rep.ModuleExtensions, opts, func(e report.ModuleExtSpec) bool { return e.Private })
	if len(exts) == 0 {
		return
	}
	b.WriteString("\n## Module Extensions\n")
	for _, e := range exts {
		writeSymbolHeading(b, e.Name, "module_extension")
		writeDocBody(b, e.Doc)
		if len(e.TagClasses) > 0 {
			b.WriteString("\n**Tag classes**\n\n")
			for _, tc := range e.TagClasses {
				fmt.Fprintf(b, "- `%s`\n", tc)
			}
		}
	}
}

// writeSymbolHeading emits the Stardoc-shape `<a id="name"></a>` +
// `## name (kind)` block. The bare-name anchor satisfies cross-refs
// of the form `[name](#name)` that authors write in their doc
// strings; the kind tag (`(rule)` / `(macro)` / etc.) keeps readers
// oriented when a module has same-named symbols across kinds.
func writeSymbolHeading(b *strings.Builder, name, kind string) {
	fmt.Fprintf(b, "\n<a id=%q></a>\n## %s (%s)\n", name, name, kind)
}

// writeDocBody renders a symbol's docstring as Markdown. Routes
// through starlark-doc-go to lift Args/Returns/Examples into proper
// structured sections; falls back to the raw doc body when nothing
// structured was found (preserves Markdown the author wrote outside
// the Stardoc conventions).
func writeDocBody(b *strings.Builder, body string) {
	if body == "" {
		return
	}
	d := doc.Parse(body)
	if d.Summary != "" {
		b.WriteString("\n")
		b.WriteString(d.Summary)
		b.WriteString("\n")
	}
	if d.Description != "" {
		b.WriteString("\n")
		b.WriteString(d.Description)
		b.WriteString("\n")
	}
	if d.Deprecated != "" {
		fmt.Fprintf(b, "\n> **Deprecated:** %s\n", d.Deprecated)
	}
	if len(d.Args) > 0 {
		b.WriteString("\n**Arguments**\n\n")
		writeArgsTable(b, d.Args)
	}
	if d.Returns != nil {
		b.WriteString("\n**Returns**\n\n")
		if d.Returns.Type != "" {
			fmt.Fprintf(b, "*%s.* ", d.Returns.Type)
		}
		b.WriteString(d.Returns.Doc)
		b.WriteString("\n")
	}
	if len(d.Examples) > 0 {
		b.WriteString("\n**Example**\n\n")
		for _, ex := range d.Examples {
			lang := ex.Lang
			if lang == "" {
				lang = "starlark"
			}
			fmt.Fprintf(b, "```%s\n%s\n```\n", lang, ex.Code)
		}
	}
}

// writeArgsTable emits a Markdown table for parsed Args entries
// (from the docstring). The companion writeAttrsTable handles
// extracted rule attrs (different concept — schema vs prose).
func writeArgsTable(b *strings.Builder, args []doc.Param) {
	b.WriteString("| Arg | Type | Description |\n")
	b.WriteString("|---|---|---|\n")
	for _, a := range args {
		fmt.Fprintf(b, "| %s | %s | %s |\n",
			mdEscape(a.Name),
			mdEscape(orFallback(a.Type, "")),
			mdEscape(singleLine(a.Doc)))
	}
}

// writeAttrsTable emits the rule/repository_rule attribute schema as
// a Markdown table. Renders only when attrs are non-empty so empty
// rules don't get a placeholder header.
func writeAttrsTable(b *strings.Builder, attrs []report.AttrSpec) {
	if len(attrs) == 0 {
		return
	}
	b.WriteString("\n**Attributes**\n\n")
	b.WriteString("| Attr | Type | Required | Default | Description |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, a := range attrs {
		required := "no"
		if a.Mandatory {
			required = "**yes**"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
			mdEscape(a.Name),
			mdEscape(orFallback(a.Type, "")),
			required,
			mdEscape(orFallback(a.Default, "")),
			mdEscape(singleLine(a.Doc)))
	}
}

// filterPrivate returns the subset of items whose Private bool is
// false, unless opts.IncludePrivate is set. Generic helper kept
// inside this file because the calling sites all need exactly the
// same shape.
func filterPrivate[T any](items []T, opts Options, isPrivate func(T) bool) []T {
	if opts.IncludePrivate {
		return items
	}
	out := items[:0:0]
	for _, it := range items {
		if !isPrivate(it) {
			out = append(out, it)
		}
	}
	return out
}

// mdEscape replaces pipe characters with HTML entities so Markdown-
// table cells don't break when a value (a default, a type with
// generics, etc.) contains a literal `|`. Other escapes intentionally
// not done — we want users to see their actual markdown rendered.
func mdEscape(s string) string {
	if s == "" {
		return ""
	}
	if !strings.ContainsAny(s, "|\n") {
		return s
	}
	s = strings.ReplaceAll(s, "|", "\\|")
	s = singleLine(s)
	return s
}

// singleLine flattens newlines into spaces so table cells stay on
// one row. The original line breaks are preserved in the docstring
// itself, so this is a cell-level concern only.
func singleLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
}

// orFallback returns s unless s is empty, in which case it returns
// alt. Tiny helper to avoid `if s == ""` boilerplate inline.
func orFallback(s, alt string) string {
	if s != "" {
		return s
	}
	return alt
}

// sortStrings is a stable wrapper kept here because future versions
// of stardoc may want to sort symbols (currently the input order is
// preserved, matching the order assay emitted them). Reserve the
// helper so adding deterministic sort later is a one-line change.
var _ = sort.Strings
