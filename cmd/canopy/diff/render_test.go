package diff

import (
	"bytes"
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/canopy/internal/closurediff"
	"github.com/albertocavalcante/canopy/internal/modulediff"
)

// fixtureReport builds a representative *modulediff.Report exercising
// every code path in the renderers: hermeticity ±, deps changed/added/
// removed, rules with attr-level changes, providers with field deltas,
// macros/aspects/toolchains added/removed, module_extensions with
// tag_class changes, repository_rules + a compat_level shift, plus a
// breaking-findings entry.
func fixtureReport() *modulediff.Report {
	return &modulediff.Report{
		Module: "rules_x",
		From:   "1.0.0",
		To:     "2.0.0",
		CompatibilityLevel: &modulediff.CompatChange{From: 1, To: 2},
		Hermeticity: &modulediff.HermDiff{
			Added:   []report.HermeticityClass{"pure-starlark"},
			Removed: []report.HermeticityClass{"network-fetch-unpinned"},
		},
		BazelDeps: modulediff.DepsDiff{
			Added:   []report.ModuleKey{{Name: "newdep", Version: "1.0"}},
			Removed: []report.ModuleKey{{Name: "olddep", Version: "0.1"}},
			Changed: []modulediff.ChangedDep{{Name: "bumped", FromVersion: "1.0", ToVersion: "2.0"}},
		},
		Rules: modulediff.RulesDiff{
			Added:   []string{"new_rule"},
			Removed: []string{"old_rule"},
			Changed: []modulediff.ChangedRule{{
				Name:     "x_library",
				AttrsAdd: []report.AttrSpec{{Name: "newattr", Type: "string", Mandatory: true}},
				AttrsRem: []report.AttrSpec{{Name: "oldattr"}},
				AttrsChg: []modulediff.AttrChange{{
					Name:          "deps",
					FromType:      "label_list",
					ToType:        "label_list",
					FromMandatory: false,
					ToMandatory:   true,
					MandatoryFlip: true,
				}},
			}},
		},
		Providers: modulediff.ProvidersDiff{
			Added: []string{"NewInfo"},
			Changed: []modulediff.ChangedProvider{{
				Name:          "XInfo",
				FieldsAdded:   []string{"src"},
				FieldsRemoved: []string{"out"},
			}},
		},
		Macros:     modulediff.NamesDiff{Added: []string{"new_macro"}, Removed: []string{"old_macro"}},
		Aspects:    modulediff.NamesDiff{Added: []string{"new_aspect"}},
		Toolchains: modulediff.NamesDiff{Removed: []string{"old_toolchain"}},
		RepositoryRules: modulediff.RulesDiff{
			Added: []string{"new_repo_rule"},
		},
		ModuleExtensions: modulediff.ModExtsDiff{
			Added: []string{"new_ext"},
			Changed: []modulediff.ChangedModExt{{
				Name:              "x_ext",
				TagClassesAdded:   []string{"new_tag"},
				TagClassesRemoved: []string{"old_tag"},
			}},
		},
		Breaking: []modulediff.BreakingFinding{{
			Kind:   modulediff.BreakingRuleRemoved,
			Symbol: "old_rule",
			Reason: "rule removed; consumers must migrate",
		}},
	}
}

func TestRenderDiffText_HitsAllSections(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDiffText(&buf, fixtureReport()); err != nil {
		t.Fatalf("renderDiffText: %v", err)
	}
	out := buf.String()
	// Every section header that the fixture should trigger:
	wants := []string{
		"rules_x · 1.0.0 → 2.0.0",
		"BREAKING CHANGES (1)",
		"compat_level  L1 → L2",
		"hermeticity",
		"+ pure-starlark",
		"− network-fetch-unpinned",
		"bazel_deps",
		"~ bumped",
		"+ newdep@1.0",
		"− olddep@0.1",
		"rules",
		"~ x_library",
		"providers",
		"~ XInfo",
		"macros",
		"module_extensions",
		"~ x_ext",
		"aspects",
		"toolchains",
		"repository_rules",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("text output missing %q\n--- output ---\n%s", w, out)
		}
	}
}

func TestRenderDiffText_WhatIfBanner(t *testing.T) {
	r := fixtureReport()
	r.FromSource = "upstream"
	r.ToSource = "local"
	var buf bytes.Buffer
	_ = renderDiffText(&buf, r)
	if !strings.Contains(buf.String(), "what-if") {
		t.Errorf("upstream-source side should trigger what-if banner; got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "1.0.0") {
		t.Errorf("what-if banner should name the upstream side; got:\n%s", buf.String())
	}
}

func TestRenderDiffMarkdown_HitsAllSections(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDiffMarkdown(&buf, fixtureReport()); err != nil {
		t.Fatalf("renderDiffMarkdown: %v", err)
	}
	out := buf.String()
	wants := []string{
		"## rules_x · `1.0.0` → `2.0.0`",
		"### ⚠ Breaking changes (1)",
		"### compatibility_level",
		"### hermeticity",
		"### bazel_deps",
		"### rules",
		"### providers",
		"### macros",
		"### module_extensions",
		"### aspects",
		"### toolchains",
		"### repository_rules",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("markdown output missing %q", w)
		}
	}
}

func TestRenderDiffMarkdown_WhatIfBlockquote(t *testing.T) {
	r := fixtureReport()
	r.FromSource = "upstream"
	r.ToSource = "upstream"
	var buf bytes.Buffer
	_ = renderDiffMarkdown(&buf, r)
	if !strings.Contains(buf.String(), "> _What-if diff:") {
		t.Errorf("dual-upstream report should render blockquote; got:\n%s", buf.String())
	}
	// Pluralization: two sides → "versions"
	if !strings.Contains(buf.String(), "versions `1.0.0, 2.0.0`") {
		t.Errorf("dual-upstream should pluralize and list both sides; got:\n%s", buf.String())
	}
}

func TestRenderDiff_EmptyReportNoPanic(t *testing.T) {
	r := &modulediff.Report{Module: "m", From: "1", To: "2"}
	var buf bytes.Buffer
	if err := renderDiffText(&buf, r); err != nil {
		t.Errorf("text: %v", err)
	}
	if err := renderDiffMarkdown(&buf, r); err != nil {
		t.Errorf("markdown: %v", err)
	}
}

// Closure renderers.

func fixtureClosure() *closurediff.Report {
	return &closurediff.Report{
		Module:               "root",
		From:                 "1.0.0",
		To:                   "2.0.0",
		FromClosureSize:      3,
		ToClosureSize:        4,
		ClosureBreakingTotal: 2,
		ClosureBreakingByModule: map[string]int{
			"root":     1,
			"transdep": 1,
		},
		ClosureDeps: closurediff.ClosureDepsDiff{
			Added:   []report.ModuleKey{{Name: "newdep", Version: "1.0"}},
			Changed: []closurediff.ChangedClosureDep{{Name: "bumped", FromVersion: "0.1", ToVersion: "0.2"}},
		},
		ModuleDiffs: map[string]*modulediff.Report{
			"root": {
				Module: "root", From: "1.0.0", To: "2.0.0",
				Rules:    modulediff.RulesDiff{Added: []string{"new_rule"}},
				Breaking: []modulediff.BreakingFinding{{Kind: modulediff.BreakingRuleRemoved, Symbol: "x"}},
			},
		},
		ErrorByModule: map[string]string{"weird_archive_mod": "unsupported archive format"},
	}
}

func TestRenderClosureText_HitsAllSections(t *testing.T) {
	var buf bytes.Buffer
	if err := renderClosureText(&buf, fixtureClosure()); err != nil {
		t.Fatalf("renderClosureText: %v", err)
	}
	out := buf.String()
	wants := []string{
		"root · 1.0.0 → 2.0.0",
		"closure: 3 → 4 modules",
		"BREAKING (closure-wide): 2",
		"closure shape",
		"per-module impact",
		"errors",
		"weird_archive_mod",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("closure text missing %q\n--- output ---\n%s", w, out)
		}
	}
}

func TestRenderClosureMarkdown_HitsAllSections(t *testing.T) {
	var buf bytes.Buffer
	if err := renderClosureMarkdown(&buf, fixtureClosure()); err != nil {
		t.Fatalf("renderClosureMarkdown: %v", err)
	}
	out := buf.String()
	wants := []string{
		"## root · `1.0.0` → `2.0.0` _(closure diff)_",
		"### ⚠ Closure-wide breaking:",
		"### Closure shape",
		"### Per-module impact",
		"### Errors",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("closure markdown missing %q", w)
		}
	}
}

// Helpers.

func TestPlural(t *testing.T) {
	cases := map[int]string{0: "s", 1: "", 2: "s", 100: "s"}
	for in, want := range cases {
		if got := plural(in); got != want {
			t.Errorf("plural(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestOrDash(t *testing.T) {
	if orDash("") != "—" {
		t.Error("empty string should render as em-dash")
	}
	if orDash("x") != "x" {
		t.Error("non-empty should pass through")
	}
}

func TestYesNo(t *testing.T) {
	if yesNo(true) != "yes" || yesNo(false) != "no" {
		t.Error("yesNo mapping wrong")
	}
}

func TestBacktickJoin_SortsAndWraps(t *testing.T) {
	got := backtickJoin([]string{"c", "a", "b"})
	want := "`a`, `b`, `c`"
	if got != want {
		t.Errorf("backtickJoin = %q, want %q", got, want)
	}
	if backtickJoin(nil) != "" {
		t.Errorf("backtickJoin(nil) should be empty string")
	}
}

func TestSimpleDelta(t *testing.T) {
	cases := []struct {
		a, r, c int
		want    string
	}{
		{0, 0, 0, ""},
		{2, 0, 0, "+2"},
		{0, 1, 0, "−1"},
		{0, 0, 3, "~3"},
		{1, 2, 3, "+1 −2 ~3"},
	}
	for _, tc := range cases {
		if got := simpleDelta(tc.a, tc.r, tc.c); got != tc.want {
			t.Errorf("simpleDelta(%d,%d,%d) = %q, want %q", tc.a, tc.r, tc.c, got, tc.want)
		}
	}
}

func TestPerModuleSummary_NoChanges(t *testing.T) {
	md := &modulediffReportLike{}
	if got := perModuleSummary(md); got != "no public-surface changes" {
		t.Errorf("empty diff should report no-changes, got %q", got)
	}
}

func TestPerModuleSummary_AggregatesDomains(t *testing.T) {
	md := &modulediffReportLike{
		RulesAdded: 1, DepsChanged: 1, ProvidersRemoved: 1,
	}
	got := perModuleSummary(md)
	for _, want := range []string{"rules +1", "deps ~1", "providers −1"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q: %s", want, got)
		}
	}
}

func TestAsStrings(t *testing.T) {
	in := []report.HermeticityClass{"a", "b"}
	out := asStrings(in)
	if len(out) != 2 || out[0] != "a" || out[1] != "b" {
		t.Errorf("asStrings round-trip failed: %v", out)
	}
}

func TestSortedKeysGeneric(t *testing.T) {
	m := map[string]*modulediff.Report{"c": nil, "a": nil, "b": nil}
	got := sortedKeysGeneric(m)
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sortedKeysGeneric = %v, want %v", got, want)
	}
}
