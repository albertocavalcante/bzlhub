package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/albertocavalcante/canopy/internal/modulediff"
)

// backtickJoin sorts then backtick-wraps each entry for markdown output.
func backtickJoin(ss []string) string {
	cp := append([]string{}, ss...)
	sort.Strings(cp)
	for i, s := range cp {
		cp[i] = "`" + s + "`"
	}
	return strings.Join(cp, ", ")
}

// plural returns "s" when n != 1, "" otherwise. Duplicate of
// cmd/canopy/helpers.go's plural — kept local so this subpackage
// doesn't reach back into its parent.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// asStrings widens a []HermeticityClass (or any string-kinded slice) to
// []string so the shared backtickJoin helper can render it.
func asStrings[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, v := range xs {
		out[i] = string(v)
	}
	return out
}

// perModuleSummary produces a terse "rules ±A · deps ±B · ext ±C" style
// summary from a per-module diff. The full per-module surface is in
// --format=json or `canopy diff <module> <from> <to>`; this is just an
// at-a-glance hint of where the action is in this row.
func perModuleSummary(md *modulediffReportLike) string {
	var parts []string
	if n := rulesDelta(md.RulesAdded, md.RulesRemoved, md.RulesChanged); n != "" {
		parts = append(parts, "rules "+n)
	}
	if n := simpleDelta(md.DepsAdded, md.DepsRemoved, md.DepsChanged); n != "" {
		parts = append(parts, "deps "+n)
	}
	if n := simpleDelta(md.ProvidersAdded, md.ProvidersRemoved, md.ProvidersChanged); n != "" {
		parts = append(parts, "providers "+n)
	}
	if n := simpleDelta(md.ExtAdded, md.ExtRemoved, md.ExtChanged); n != "" {
		parts = append(parts, "module_extensions "+n)
	}
	if len(parts) == 0 {
		return "no public-surface changes"
	}
	return strings.Join(parts, " · ")
}

func simpleDelta(a, r, c int) string {
	if a+r+c == 0 {
		return ""
	}
	out := ""
	if a > 0 {
		out += fmt.Sprintf("+%d", a)
	}
	if r > 0 {
		out += fmt.Sprintf(" −%d", r)
	}
	if c > 0 {
		out += fmt.Sprintf(" ~%d", c)
	}
	return strings.TrimSpace(out)
}

func rulesDelta(a, r, c int) string { return simpleDelta(a, r, c) }

// modulediffReportLike is the slim adapter shape this file uses to
// pull headline counts off a *modulediff.Report. Avoids leaking the
// detailed schema into the summary printer.
type modulediffReportLike struct {
	RulesAdded, RulesRemoved, RulesChanged             int
	DepsAdded, DepsRemoved, DepsChanged                int
	ProvidersAdded, ProvidersRemoved, ProvidersChanged int
	ExtAdded, ExtRemoved, ExtChanged                   int
}

func adaptForSummary(md *modulediff.Report) *modulediffReportLike {
	return &modulediffReportLike{
		RulesAdded: len(md.Rules.Added), RulesRemoved: len(md.Rules.Removed), RulesChanged: len(md.Rules.Changed),
		DepsAdded: len(md.BazelDeps.Added), DepsRemoved: len(md.BazelDeps.Removed), DepsChanged: len(md.BazelDeps.Changed),
		ProvidersAdded: len(md.Providers.Added), ProvidersRemoved: len(md.Providers.Removed), ProvidersChanged: len(md.Providers.Changed),
		ExtAdded: len(md.ModuleExtensions.Added), ExtRemoved: len(md.ModuleExtensions.Removed), ExtChanged: len(md.ModuleExtensions.Changed),
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysStr(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedKeysGeneric is parameterized over the value type so we don't
// need one helper per map[string]X we touch.
func sortedKeysGeneric[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
