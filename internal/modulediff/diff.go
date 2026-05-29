// Package modulediff produces a structured diff between two
// assay.ModuleReport snapshots so the UI can show "what changed
// between X@A and X@B" — the actionable companion to the drift
// dashboard's "REVIEW" tier.
//
// The diff is intentionally a value-oriented data structure (no
// interfaces, no methods): producers in this package, consumers in
// REST/MCP/UI, JSON-serializable as-is.
//
// File layout:
//   - types.go     wire types (Report + sub-diffs)
//   - diff.go      Compute orchestrator + small per-domain diffs
//                  (hermeticity, deps, names) + filter/extract helpers
//   - rules.go     rules domain (RulesDiff + attr-level comparison)
//   - providers.go providers domain (ProvidersDiff + field comparison)
//   - modexts.go   module-extension domain (tag_class comparison)
//   - breaking.go  Breaking classification (separate concern)
package modulediff

import (
	"sort"

	"github.com/albertocavalcante/assay/report"
)

// Compute produces a diff. Filters private (underscore-prefixed) names
// so the diff is meaningful for module consumers, not author internals.
func Compute(from, to *report.ModuleReport) *Report {
	d := &Report{Module: to.Name, From: from.Version, To: to.Version}

	if from.CompatibilityLevel != to.CompatibilityLevel {
		d.CompatibilityLevel = &CompatChange{From: from.CompatibilityLevel, To: to.CompatibilityLevel}
	}

	if hd := hermDiff(from.Hermeticity.Classes, to.Hermeticity.Classes); hd != nil {
		d.Hermeticity = hd
	}

	d.BazelDeps = depsDiff(from.BazelDeps, to.BazelDeps)
	d.Rules = rulesDiff(filterPublicRules(from.Rules), filterPublicRules(to.Rules))
	d.Providers = providersDiff(filterPublicProviders(from.Providers), filterPublicProviders(to.Providers))
	d.Macros = namesDiff(macroNames(from.Macros), macroNames(to.Macros))
	d.Aspects = namesDiff(aspectNames(from.Aspects), aspectNames(to.Aspects))
	d.Toolchains = namesDiff(toolchainNames(from.Toolchains), toolchainNames(to.Toolchains))
	d.RepositoryRules = rulesDiff(repoRulesAsRules(from.RepositoryRules), repoRulesAsRules(to.RepositoryRules))
	d.ModuleExtensions = modExtsDiff(filterPublicModExts(from.ModuleExtensions), filterPublicModExts(to.ModuleExtensions))
	d.Breaking = ClassifyBreaking(d)
	return d
}

// --- hermeticity ---------------------------------------------------

func hermDiff(a, b []report.HermeticityClass) *HermDiff {
	setA := map[report.HermeticityClass]bool{}
	setB := map[report.HermeticityClass]bool{}
	for _, c := range a {
		setA[c] = true
	}
	for _, c := range b {
		setB[c] = true
	}
	out := &HermDiff{}
	for c := range setB {
		if !setA[c] {
			out.Added = append(out.Added, c)
		}
	}
	for c := range setA {
		if !setB[c] {
			out.Removed = append(out.Removed, c)
		}
	}
	sortHerm(out.Added)
	sortHerm(out.Removed)
	if len(out.Added) == 0 && len(out.Removed) == 0 {
		return nil
	}
	return out
}

func sortHerm(s []report.HermeticityClass) {
	sort.Slice(s, func(i, j int) bool { return string(s[i]) < string(s[j]) })
}

// --- deps ----------------------------------------------------------

func depsDiff(a, b []report.ModuleKey) DepsDiff {
	aByName := map[string]string{}
	for _, d := range a {
		aByName[d.Name] = d.Version
	}
	bByName := map[string]string{}
	for _, d := range b {
		bByName[d.Name] = d.Version
	}
	var out DepsDiff
	for name, bver := range bByName {
		if aver, ok := aByName[name]; ok {
			if aver != bver {
				out.Changed = append(out.Changed, ChangedDep{Name: name, FromVersion: aver, ToVersion: bver})
			}
		} else {
			out.Added = append(out.Added, report.ModuleKey{Name: name, Version: bver})
		}
	}
	for name, aver := range aByName {
		if _, ok := bByName[name]; !ok {
			out.Removed = append(out.Removed, report.ModuleKey{Name: name, Version: aver})
		}
	}
	sort.Slice(out.Added, func(i, j int) bool { return out.Added[i].Name < out.Added[j].Name })
	sort.Slice(out.Removed, func(i, j int) bool { return out.Removed[i].Name < out.Removed[j].Name })
	sort.Slice(out.Changed, func(i, j int) bool { return out.Changed[i].Name < out.Changed[j].Name })
	return out
}

// --- names (macros / aspects / toolchains) -------------------------

func namesDiff(a, b []string) NamesDiff {
	aSet := map[string]bool{}
	for _, s := range a {
		aSet[s] = true
	}
	bSet := map[string]bool{}
	for _, s := range b {
		bSet[s] = true
	}
	var out NamesDiff
	for s := range bSet {
		if !aSet[s] {
			out.Added = append(out.Added, s)
		}
	}
	for s := range aSet {
		if !bSet[s] {
			out.Removed = append(out.Removed, s)
		}
	}
	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	return out
}

// --- filter / extract helpers --------------------------------------

// filterPublicRules drops underscore-prefixed entries so the diff reflects
// what module consumers actually see. Authors moving internal rules around
// shouldn't pollute the migration report.
func filterPublicRules(rs []report.RuleSpec) []report.RuleSpec {
	out := make([]report.RuleSpec, 0, len(rs))
	for _, r := range rs {
		if !r.Private && len(r.Name) > 0 && r.Name[0] != '_' {
			out = append(out, r)
		}
	}
	return out
}

func filterPublicProviders(ps []report.ProviderSpec) []report.ProviderSpec {
	out := make([]report.ProviderSpec, 0, len(ps))
	for _, p := range ps {
		if !p.Private && len(p.Name) > 0 && p.Name[0] != '_' {
			out = append(out, p)
		}
	}
	return out
}

func macroNames(ms []report.MacroSpec) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		if len(m.Name) > 0 && m.Name[0] != '_' {
			out = append(out, m.Name)
		}
	}
	return out
}

func aspectNames(as []report.AspectSpec) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		if !a.Private && len(a.Name) > 0 && a.Name[0] != '_' {
			out = append(out, a.Name)
		}
	}
	return out
}

func toolchainNames(ts []report.ToolchainSpec) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		if len(t.Name) > 0 && t.Name[0] != '_' {
			out = append(out, t.Name)
		}
	}
	return out
}

// repoRulesAsRules adapts public RepoRuleSpec entries into the RuleSpec
// shape so rulesDiff can do the heavy lifting. Drops underscore-prefixed
// names and entries marked Private.
func repoRulesAsRules(rs []report.RepoRuleSpec) []report.RuleSpec {
	out := make([]report.RuleSpec, 0, len(rs))
	for _, r := range rs {
		if !r.Private && len(r.Name) > 0 && r.Name[0] != '_' {
			out = append(out, report.RuleSpec{Name: r.Name, Attrs: r.Attrs})
		}
	}
	return out
}

func filterPublicModExts(es []report.ModuleExtSpec) []report.ModuleExtSpec {
	out := make([]report.ModuleExtSpec, 0, len(es))
	for _, e := range es {
		if !e.Private && len(e.Name) > 0 && e.Name[0] != '_' {
			out = append(out, e)
		}
	}
	return out
}
