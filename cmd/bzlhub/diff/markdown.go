package diff

import (
	"fmt"
	"io"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/closurediff"
	"github.com/albertocavalcante/bzlhub/internal/modulediff"
)

// renderDiffMarkdown is the CLI mirror of ui/src/lib/diff-markdown.ts.
// Keep the two in sync — the same PR-body shape regardless of which surface
// produced the diff.
func renderDiffMarkdown(w io.Writer, r *modulediff.Report) error {
	fmt.Fprintf(w, "## %s · `%s` → `%s`\n\n", r.Module, r.From, r.To)
	if r.FromSource == "upstream" || r.ToSource == "upstream" {
		var sides []string
		if r.FromSource == "upstream" {
			sides = append(sides, r.From)
		}
		if r.ToSource == "upstream" {
			sides = append(sides, r.To)
		}
		noun := "version"
		if len(sides) > 1 {
			noun = "versions"
		}
		fmt.Fprintf(w, "> _What-if diff: %s `%s` fetched from upstream; not in the local index._\n\n",
			noun, strings.Join(sides, ", "))
	}

	if len(r.Breaking) > 0 {
		fmt.Fprintf(w, "### ⚠ Breaking changes (%d)\n", len(r.Breaking))
		fmt.Fprintln(w, "_Consumers exercising these surfaces will need code changes to migrate._")
		fmt.Fprintln(w)
		for _, f := range r.Breaking {
			sym := "`" + f.Symbol + "`"
			if f.Detail != "" {
				sym = "`" + f.Symbol + "." + f.Detail + "`"
			}
			fmt.Fprintf(w, "- **%s** %s — %s\n", f.Kind, sym, f.Reason)
		}
		fmt.Fprintln(w)
	}

	if r.CompatibilityLevel != nil {
		fmt.Fprintf(w, "### compatibility_level\n**`L%d` → `L%d`** — different compatibility_levels are incompatible in Bazel; expect a hard migration.\n\n",
			r.CompatibilityLevel.From, r.CompatibilityLevel.To)
	}
	if r.Hermeticity != nil {
		fmt.Fprintln(w, "### hermeticity")
		if len(r.Hermeticity.Added) > 0 {
			fmt.Fprintf(w, "- **+** %s\n", backtickJoin(asStrings(r.Hermeticity.Added)))
		}
		if len(r.Hermeticity.Removed) > 0 {
			fmt.Fprintf(w, "- **−** %s\n", backtickJoin(asStrings(r.Hermeticity.Removed)))
		}
		fmt.Fprintln(w)
	}

	deps := r.BazelDeps
	if len(deps.Added)+len(deps.Removed)+len(deps.Changed) > 0 {
		fmt.Fprintln(w, "### bazel_deps")
		for _, d := range deps.Changed {
			fmt.Fprintf(w, "- ~ `%s` `%s` → `%s`\n", d.Name, d.FromVersion, d.ToVersion)
		}
		for _, d := range deps.Added {
			fmt.Fprintf(w, "- **+** `%s@%s`\n", d.Name, d.Version)
		}
		for _, d := range deps.Removed {
			fmt.Fprintf(w, "- **−** `%s@%s`\n", d.Name, d.Version)
		}
		fmt.Fprintln(w)
	}

	renderRulesMarkdown(w, "rules", r.Rules)
	renderRulesMarkdown(w, "repository_rules", r.RepositoryRules)
	renderProvidersMarkdown(w, r.Providers)
	renderNamesMarkdown(w, "macros", r.Macros.Added, r.Macros.Removed)
	renderModExtsMarkdown(w, r.ModuleExtensions)
	renderNamesMarkdown(w, "aspects", r.Aspects.Added, r.Aspects.Removed)
	renderNamesMarkdown(w, "toolchains", r.Toolchains.Added, r.Toolchains.Removed)
	return nil
}

func renderRulesMarkdown(w io.Writer, title string, d modulediff.RulesDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintf(w, "### %s\n", title)
	if len(d.Added) > 0 {
		fmt.Fprintf(w, "**Added (%d):** %s\n", len(d.Added), backtickJoin(d.Added))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "**Removed (%d):** %s\n", len(d.Removed), backtickJoin(d.Removed))
	}
	if len(d.Changed) > 0 {
		fmt.Fprintf(w, "\n**Changed (%d):**\n", len(d.Changed))
		for _, ch := range d.Changed {
			var parts []string
			if n := len(ch.AttrsAdd); n > 0 {
				var bits []string
				for _, a := range ch.AttrsAdd {
					ty := a.Type
					if ty == "" {
						ty = "any"
					}
					req := ""
					if a.Mandatory {
						req = " [required]"
					}
					bits = append(bits, fmt.Sprintf("`%s: %s%s`", a.Name, ty, req))
				}
				parts = append(parts, fmt.Sprintf("+%d attr%s (%s)", n, plural(n), strings.Join(bits, ", ")))
			}
			if n := len(ch.AttrsRem); n > 0 {
				var bits []string
				for _, a := range ch.AttrsRem {
					bits = append(bits, fmt.Sprintf("`%s`", a.Name))
				}
				parts = append(parts, fmt.Sprintf("−%d attr%s (%s)", n, plural(n), strings.Join(bits, ", ")))
			}
			if n := len(ch.AttrsChg); n > 0 {
				var bits []string
				for _, a := range ch.AttrsChg {
					var sub []string
					if a.FromType != "" || a.ToType != "" {
						sub = append(sub, fmt.Sprintf("type `%s`→`%s`", orDash(a.FromType), orDash(a.ToType)))
					}
					if a.FromDefault != "" || a.ToDefault != "" {
						sub = append(sub, fmt.Sprintf("default `%s`→`%s`", orDash(a.FromDefault), orDash(a.ToDefault)))
					}
					if a.MandatoryFlip {
						sub = append(sub, fmt.Sprintf("mandatory `%s`→`%s`", yesNo(a.FromMandatory), yesNo(a.ToMandatory)))
					}
					bits = append(bits, fmt.Sprintf("`%s` (%s)", a.Name, strings.Join(sub, "; ")))
				}
				parts = append(parts, fmt.Sprintf("~%d attr%s: %s", n, plural(n), strings.Join(bits, ", ")))
			}
			fmt.Fprintf(w, "- `%s` — %s\n", ch.Name, strings.Join(parts, " · "))
		}
	}
	fmt.Fprintln(w)
}

func renderProvidersMarkdown(w io.Writer, d modulediff.ProvidersDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintln(w, "### providers")
	if len(d.Added) > 0 {
		fmt.Fprintf(w, "**Added (%d):** %s\n", len(d.Added), backtickJoin(d.Added))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "**Removed (%d):** %s\n", len(d.Removed), backtickJoin(d.Removed))
	}
	for _, ch := range d.Changed {
		var parts []string
		if len(ch.FieldsAdded) > 0 {
			parts = append(parts, "+fields: "+backtickJoin(ch.FieldsAdded))
		}
		if len(ch.FieldsRemoved) > 0 {
			parts = append(parts, "−fields: "+backtickJoin(ch.FieldsRemoved))
		}
		fmt.Fprintf(w, "- ~ `%s` — %s\n", ch.Name, strings.Join(parts, " · "))
	}
	fmt.Fprintln(w)
}

func renderNamesMarkdown(w io.Writer, title string, added, removed []string) {
	if len(added)+len(removed) == 0 {
		return
	}
	fmt.Fprintf(w, "### %s\n", title)
	if len(added) > 0 {
		fmt.Fprintf(w, "**Added (%d):** %s\n", len(added), backtickJoin(added))
	}
	if len(removed) > 0 {
		fmt.Fprintf(w, "**Removed (%d):** %s\n", len(removed), backtickJoin(removed))
	}
	fmt.Fprintln(w)
}

func renderModExtsMarkdown(w io.Writer, d modulediff.ModExtsDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintln(w, "### module_extensions")
	fmt.Fprintln(w, "_use_extension surface — highest-impact change for Bzlmod consumers._")
	if len(d.Added) > 0 {
		fmt.Fprintf(w, "**Added (%d):** %s\n", len(d.Added), backtickJoin(d.Added))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "**Removed (%d):** %s\n", len(d.Removed), backtickJoin(d.Removed))
	}
	for _, ch := range d.Changed {
		var parts []string
		if len(ch.TagClassesAdded) > 0 {
			parts = append(parts, "+tag_classes: "+backtickJoin(ch.TagClassesAdded))
		}
		if len(ch.TagClassesRemoved) > 0 {
			parts = append(parts, "−tag_classes: "+backtickJoin(ch.TagClassesRemoved))
		}
		fmt.Fprintf(w, "- ~ `%s` — %s\n", ch.Name, strings.Join(parts, " · "))
	}
	fmt.Fprintln(w)
}

func renderClosureMarkdown(w io.Writer, r *closurediff.Report) error {
	fmt.Fprintf(w, "## %s · `%s` → `%s` _(closure diff)_\n", r.Module, r.From, r.To)
	fmt.Fprintf(w, "_Closure: %d → %d modules._\n\n", r.FromClosureSize, r.ToClosureSize)

	if r.ClosureBreakingTotal > 0 {
		fmt.Fprintf(w, "### ⚠ Closure-wide breaking: %d finding%s across %d module%s\n\n",
			r.ClosureBreakingTotal, plural(r.ClosureBreakingTotal),
			len(r.ClosureBreakingByModule), plural(len(r.ClosureBreakingByModule)))
		for _, name := range sortedKeys(r.ClosureBreakingByModule) {
			fmt.Fprintf(w, "- **%s** — %d breaking\n", name, r.ClosureBreakingByModule[name])
		}
		fmt.Fprintln(w)
	}

	cd := r.ClosureDeps
	if len(cd.Added)+len(cd.Removed)+len(cd.Changed) > 0 {
		fmt.Fprintln(w, "### Closure shape")
		for _, c := range cd.Changed {
			fmt.Fprintf(w, "- ~ `%s` `%s` → `%s`\n", c.Name, c.FromVersion, c.ToVersion)
		}
		for _, d := range cd.Added {
			fmt.Fprintf(w, "- **+** `%s@%s`\n", d.Name, d.Version)
		}
		for _, d := range cd.Removed {
			fmt.Fprintf(w, "- **−** `%s@%s`\n", d.Name, d.Version)
		}
		fmt.Fprintln(w)
	}

	if len(r.ModuleDiffs) > 0 {
		fmt.Fprintln(w, "### Per-module impact")
		for _, name := range sortedKeysGeneric(r.ModuleDiffs) {
			md := r.ModuleDiffs[name]
			parts := perModuleSummary(adaptForSummary(md))
			breaking := ""
			if n := len(md.Breaking); n > 0 {
				breaking = fmt.Sprintf(" — **%d breaking**", n)
			}
			fmt.Fprintf(w, "- `%s` `%s` → `%s` — %s%s\n", name, md.From, md.To, parts, breaking)
		}
		fmt.Fprintln(w)
	}

	if len(r.ErrorByModule) > 0 {
		fmt.Fprintln(w, "### Errors")
		for _, name := range sortedKeysStr(r.ErrorByModule) {
			fmt.Fprintf(w, "- `%s`: %s\n", name, r.ErrorByModule[name])
		}
		fmt.Fprintln(w)
	}
	return nil
}
