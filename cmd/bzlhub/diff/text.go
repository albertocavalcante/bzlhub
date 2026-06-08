package diff

import (
	"fmt"
	"io"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/closurediff"
	"github.com/albertocavalcante/bzlhub/internal/modulediff"
)

// renderDiffText prints a compact terminal-friendly summary. Designed to fit
// on one screen for small diffs; long lists wrap.
func renderDiffText(w io.Writer, r *modulediff.Report) error {
	fmt.Fprintf(w, "%s · %s → %s\n", r.Module, r.From, r.To)
	if r.FromSource == "upstream" || r.ToSource == "upstream" {
		var sides []string
		if r.FromSource == "upstream" {
			sides = append(sides, r.From)
		}
		if r.ToSource == "upstream" {
			sides = append(sides, r.To)
		}
		fmt.Fprintf(w, "  (what-if: %s fetched from upstream, not in local index)\n", strings.Join(sides, ", "))
	}
	fmt.Fprintln(w)

	if len(r.Breaking) > 0 {
		fmt.Fprintf(w, "BREAKING CHANGES (%d) — consumers will need code changes:\n", len(r.Breaking))
		for _, f := range r.Breaking {
			sym := f.Symbol
			if f.Detail != "" {
				sym = f.Symbol + "." + f.Detail
			}
			fmt.Fprintf(w, "  ! [%s] %s — %s\n", f.Kind, sym, f.Reason)
		}
		fmt.Fprintln(w)
	}

	if r.CompatibilityLevel != nil {
		fmt.Fprintf(w, "compat_level  L%d → L%d   (likely breaking — Bazel treats different levels as incompatible)\n\n",
			r.CompatibilityLevel.From, r.CompatibilityLevel.To)
	}
	if r.Hermeticity != nil {
		fmt.Fprintln(w, "hermeticity")
		for _, c := range r.Hermeticity.Added {
			fmt.Fprintf(w, "  + %s\n", c)
		}
		for _, c := range r.Hermeticity.Removed {
			fmt.Fprintf(w, "  − %s\n", c)
		}
		fmt.Fprintln(w)
	}

	if n := len(r.BazelDeps.Added) + len(r.BazelDeps.Removed) + len(r.BazelDeps.Changed); n > 0 {
		fmt.Fprintln(w, "bazel_deps")
		for _, d := range r.BazelDeps.Changed {
			fmt.Fprintf(w, "  ~ %-30s %s → %s\n", d.Name, d.FromVersion, d.ToVersion)
		}
		for _, d := range r.BazelDeps.Added {
			fmt.Fprintf(w, "  + %s@%s\n", d.Name, d.Version)
		}
		for _, d := range r.BazelDeps.Removed {
			fmt.Fprintf(w, "  − %s@%s\n", d.Name, d.Version)
		}
		fmt.Fprintln(w)
	}

	renderRulesText(w, "rules", r.Rules)
	renderRulesText(w, "repository_rules", r.RepositoryRules)
	renderProvidersText(w, r.Providers)
	renderNamesText(w, "macros", r.Macros.Added, r.Macros.Removed)
	renderModExtsText(w, r.ModuleExtensions)
	renderNamesText(w, "aspects", r.Aspects.Added, r.Aspects.Removed)
	renderNamesText(w, "toolchains", r.Toolchains.Added, r.Toolchains.Removed)
	return nil
}

func renderRulesText(w io.Writer, title string, d modulediff.RulesDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintln(w, title)
	for _, ch := range d.Changed {
		var parts []string
		if n := len(ch.AttrsAdd); n > 0 {
			parts = append(parts, fmt.Sprintf("+%d attr%s", n, plural(n)))
		}
		if n := len(ch.AttrsRem); n > 0 {
			parts = append(parts, fmt.Sprintf("−%d attr%s", n, plural(n)))
		}
		if n := len(ch.AttrsChg); n > 0 {
			parts = append(parts, fmt.Sprintf("~%d attr%s", n, plural(n)))
		}
		fmt.Fprintf(w, "  ~ %s  (%s)\n", ch.Name, strings.Join(parts, " · "))
		for _, a := range ch.AttrsAdd {
			req := ""
			if a.Mandatory {
				req = " [required]"
			}
			ty := a.Type
			if ty == "" {
				ty = "any"
			}
			fmt.Fprintf(w, "      + %s: %s%s\n", a.Name, ty, req)
		}
		for _, a := range ch.AttrsRem {
			fmt.Fprintf(w, "      − %s\n", a.Name)
		}
		for _, a := range ch.AttrsChg {
			var sub []string
			if a.FromType != "" || a.ToType != "" {
				sub = append(sub, fmt.Sprintf("type %s→%s", orDash(a.FromType), orDash(a.ToType)))
			}
			if a.FromDefault != "" || a.ToDefault != "" {
				sub = append(sub, fmt.Sprintf("default %s→%s", orDash(a.FromDefault), orDash(a.ToDefault)))
			}
			if a.MandatoryFlip {
				sub = append(sub, fmt.Sprintf("mandatory %s→%s", yesNo(a.FromMandatory), yesNo(a.ToMandatory)))
			}
			fmt.Fprintf(w, "      ~ %s  (%s)\n", a.Name, strings.Join(sub, "; "))
		}
	}
	if len(d.Added) > 0 {
		fmt.Fprintf(w, "  added (%d): %s\n", len(d.Added), strings.Join(d.Added, ", "))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "  removed (%d): %s\n", len(d.Removed), strings.Join(d.Removed, ", "))
	}
	fmt.Fprintln(w)
}

func renderProvidersText(w io.Writer, d modulediff.ProvidersDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintln(w, "providers")
	if len(d.Added) > 0 {
		fmt.Fprintf(w, "  added (%d): %s\n", len(d.Added), strings.Join(d.Added, ", "))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "  removed (%d): %s\n", len(d.Removed), strings.Join(d.Removed, ", "))
	}
	for _, ch := range d.Changed {
		var parts []string
		if len(ch.FieldsAdded) > 0 {
			parts = append(parts, "+fields: "+strings.Join(ch.FieldsAdded, ", "))
		}
		if len(ch.FieldsRemoved) > 0 {
			parts = append(parts, "−fields: "+strings.Join(ch.FieldsRemoved, ", "))
		}
		fmt.Fprintf(w, "  ~ %s  (%s)\n", ch.Name, strings.Join(parts, " · "))
	}
	fmt.Fprintln(w)
}

func renderNamesText(w io.Writer, title string, added, removed []string) {
	if len(added)+len(removed) == 0 {
		return
	}
	fmt.Fprintln(w, title)
	if len(added) > 0 {
		fmt.Fprintf(w, "  added (%d): %s\n", len(added), strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		fmt.Fprintf(w, "  removed (%d): %s\n", len(removed), strings.Join(removed, ", "))
	}
	fmt.Fprintln(w)
}

func renderModExtsText(w io.Writer, d modulediff.ModExtsDiff) {
	if len(d.Added)+len(d.Removed)+len(d.Changed) == 0 {
		return
	}
	fmt.Fprintln(w, "module_extensions  (use_extension surface — Bzlmod consumer impact)")
	if len(d.Added) > 0 {
		fmt.Fprintf(w, "  added (%d): %s\n", len(d.Added), strings.Join(d.Added, ", "))
	}
	if len(d.Removed) > 0 {
		fmt.Fprintf(w, "  removed (%d): %s\n", len(d.Removed), strings.Join(d.Removed, ", "))
	}
	for _, ch := range d.Changed {
		var parts []string
		if len(ch.TagClassesAdded) > 0 {
			parts = append(parts, "+tag_classes: "+strings.Join(ch.TagClassesAdded, ", "))
		}
		if len(ch.TagClassesRemoved) > 0 {
			parts = append(parts, "−tag_classes: "+strings.Join(ch.TagClassesRemoved, ", "))
		}
		fmt.Fprintf(w, "  ~ %s  (%s)\n", ch.Name, strings.Join(parts, " · "))
	}
	fmt.Fprintln(w)
}

func renderClosureText(w io.Writer, r *closurediff.Report) error {
	fmt.Fprintf(w, "%s · %s → %s   (closure: %d → %d modules)\n",
		r.Module, r.From, r.To, r.FromClosureSize, r.ToClosureSize)
	fmt.Fprintln(w)

	if r.ClosureBreakingTotal > 0 {
		fmt.Fprintf(w, "BREAKING (closure-wide): %d finding%s across %d module%s\n",
			r.ClosureBreakingTotal, plural(r.ClosureBreakingTotal),
			len(r.ClosureBreakingByModule), plural(len(r.ClosureBreakingByModule)))
		for _, name := range sortedKeys(r.ClosureBreakingByModule) {
			fmt.Fprintf(w, "  ! %-30s  %d\n", name, r.ClosureBreakingByModule[name])
		}
		fmt.Fprintln(w)
	}

	cd := r.ClosureDeps
	if len(cd.Added)+len(cd.Removed)+len(cd.Changed) > 0 {
		fmt.Fprintln(w, "closure shape")
		for _, c := range cd.Changed {
			fmt.Fprintf(w, "  ~ %-30s %s → %s\n", c.Name, c.FromVersion, c.ToVersion)
		}
		for _, d := range cd.Added {
			fmt.Fprintf(w, "  + %s@%s\n", d.Name, d.Version)
		}
		for _, d := range cd.Removed {
			fmt.Fprintf(w, "  − %s@%s\n", d.Name, d.Version)
		}
		fmt.Fprintln(w)
	}

	if len(r.ModuleDiffs) > 0 {
		fmt.Fprintln(w, "per-module impact")
		for _, name := range sortedKeysGeneric(r.ModuleDiffs) {
			md := r.ModuleDiffs[name]
			parts := perModuleSummary(adaptForSummary(md))
			breaking := ""
			if n := len(md.Breaking); n > 0 {
				breaking = fmt.Sprintf("  !! %d breaking", n)
			}
			fmt.Fprintf(w, "  %s · %s → %s   %s%s\n", name, md.From, md.To, parts, breaking)
		}
		fmt.Fprintln(w)
	}

	if len(r.ErrorByModule) > 0 {
		fmt.Fprintln(w, "errors")
		for _, name := range sortedKeysStr(r.ErrorByModule) {
			fmt.Fprintf(w, "  ✗ %s: %s\n", name, r.ErrorByModule[name])
		}
		fmt.Fprintln(w)
	}
	return nil
}
