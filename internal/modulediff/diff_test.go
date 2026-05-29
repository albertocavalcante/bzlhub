package modulediff

import (
	"sort"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// helpers for terse fixtures

func rule(name string, attrs ...report.AttrSpec) report.RuleSpec {
	return report.RuleSpec{Name: name, Attrs: attrs}
}
func attr(name, ty, def string, mandatory bool) report.AttrSpec {
	return report.AttrSpec{Name: name, Type: ty, Default: def, Mandatory: mandatory}
}
func provider(name string, fields ...string) report.ProviderSpec {
	return report.ProviderSpec{Name: name, Fields: fields}
}

func TestDiffDeps(t *testing.T) {
	a := &report.ModuleReport{
		Name: "x", Version: "1.0.0",
		BazelDeps: []report.ModuleKey{{Name: "a", Version: "1.0.0"}, {Name: "b", Version: "1.0.0"}},
	}
	b := &report.ModuleReport{
		Name: "x", Version: "2.0.0",
		BazelDeps: []report.ModuleKey{{Name: "b", Version: "1.5.0"}, {Name: "c", Version: "0.1.0"}},
	}
	d := Compute(a, b)
	if len(d.BazelDeps.Added) != 1 || d.BazelDeps.Added[0].Name != "c" {
		t.Errorf("added: %+v", d.BazelDeps.Added)
	}
	if len(d.BazelDeps.Removed) != 1 || d.BazelDeps.Removed[0].Name != "a" {
		t.Errorf("removed: %+v", d.BazelDeps.Removed)
	}
	if len(d.BazelDeps.Changed) != 1 || d.BazelDeps.Changed[0].Name != "b" {
		t.Errorf("changed: %+v", d.BazelDeps.Changed)
	}
}

func TestDiffRulesAddedRemovedChanged(t *testing.T) {
	a := &report.ModuleReport{
		Rules: []report.RuleSpec{
			rule("keep_stable"),
			rule("will_change", attr("name", "string", "", true)),
			rule("will_disappear"),
		},
	}
	b := &report.ModuleReport{
		Rules: []report.RuleSpec{
			rule("keep_stable"),
			rule("will_change", attr("name", "string", "", true), attr("new_attr", "label", "//foo", false)),
			rule("freshly_added"),
		},
	}
	d := Compute(a, b)
	if len(d.Rules.Added) != 1 || d.Rules.Added[0] != "freshly_added" {
		t.Errorf("added: %+v", d.Rules.Added)
	}
	if len(d.Rules.Removed) != 1 || d.Rules.Removed[0] != "will_disappear" {
		t.Errorf("removed: %+v", d.Rules.Removed)
	}
	if len(d.Rules.Changed) != 1 || d.Rules.Changed[0].Name != "will_change" {
		t.Fatalf("changed: %+v", d.Rules.Changed)
	}
	c := d.Rules.Changed[0]
	if len(c.AttrsAdd) != 1 || c.AttrsAdd[0].Name != "new_attr" {
		t.Errorf("attr-add: %+v", c.AttrsAdd)
	}
}

func TestAttrChangeFlags(t *testing.T) {
	a := &report.ModuleReport{Rules: []report.RuleSpec{rule("r", attr("x", "string", "", false))}}
	b := &report.ModuleReport{Rules: []report.RuleSpec{rule("r", attr("x", "label", `"//foo"`, true))}}
	d := Compute(a, b)
	if len(d.Rules.Changed) != 1 || len(d.Rules.Changed[0].AttrsChg) != 1 {
		t.Fatalf("expected one attr-change: %+v", d.Rules)
	}
	c := d.Rules.Changed[0].AttrsChg[0]
	if c.FromType != "string" || c.ToType != "label" {
		t.Errorf("type: %+v", c)
	}
	if !c.MandatoryFlip || c.FromMandatory || !c.ToMandatory {
		t.Errorf("mandatory flip: %+v", c)
	}
	if c.FromDefault != "" || c.ToDefault != `"//foo"` {
		t.Errorf("default: %+v", c)
	}
}

func TestProviderFieldsDiff(t *testing.T) {
	a := &report.ModuleReport{Providers: []report.ProviderSpec{provider("Info", "kind", "src")}}
	b := &report.ModuleReport{Providers: []report.ProviderSpec{provider("Info", "kind", "src", "label")}}
	d := Compute(a, b)
	if len(d.Providers.Changed) != 1 || d.Providers.Changed[0].FieldsAdded[0] != "label" {
		t.Fatalf("provider fields diff: %+v", d.Providers)
	}
}

func TestHermDiffSymmetric(t *testing.T) {
	a := &report.ModuleReport{
		Hermeticity: report.HermeticityProfile{Classes: []report.HermeticityClass{"pure-starlark"}},
	}
	b := &report.ModuleReport{
		Hermeticity: report.HermeticityProfile{Classes: []report.HermeticityClass{"network-fetch-pinned", "repository-rule-arbitrary-code"}},
	}
	d := Compute(a, b)
	if d.Hermeticity == nil {
		t.Fatal("expected hermeticity diff")
	}
	if len(d.Hermeticity.Added) != 2 || len(d.Hermeticity.Removed) != 1 {
		t.Fatalf("herm diff: %+v", d.Hermeticity)
	}
}

func TestCompatibilityLevelChange(t *testing.T) {
	a := &report.ModuleReport{CompatibilityLevel: 1}
	b := &report.ModuleReport{CompatibilityLevel: 2}
	d := Compute(a, b)
	if d.CompatibilityLevel == nil || d.CompatibilityLevel.From != 1 || d.CompatibilityLevel.To != 2 {
		t.Errorf("compat change: %+v", d.CompatibilityLevel)
	}
}

func TestPrivateRulesFiltered(t *testing.T) {
	a := &report.ModuleReport{
		Rules: []report.RuleSpec{
			rule("public_a"),
			{Name: "_private_a", Private: true},
		},
	}
	b := &report.ModuleReport{
		Rules: []report.RuleSpec{
			rule("public_a"),
			rule("public_b"),
			{Name: "_private_a", Private: true},
			{Name: "_private_b", Private: true},
		},
	}
	d := Compute(a, b)
	if len(d.Rules.Added) != 1 || d.Rules.Added[0] != "public_b" {
		t.Errorf("private rules leaked: %+v", d.Rules)
	}
}

func TestAspectsAndToolchainsNamesDiff(t *testing.T) {
	a := &report.ModuleReport{
		Aspects:    []report.AspectSpec{{Name: "old_aspect"}, {Name: "stable_aspect"}, {Name: "_hidden", Private: true}},
		Toolchains: []report.ToolchainSpec{{Name: "old_tc"}},
	}
	b := &report.ModuleReport{
		Aspects:    []report.AspectSpec{{Name: "stable_aspect"}, {Name: "new_aspect"}},
		Toolchains: []report.ToolchainSpec{{Name: "new_tc"}},
	}
	d := Compute(a, b)
	if len(d.Aspects.Added) != 1 || d.Aspects.Added[0] != "new_aspect" {
		t.Errorf("aspect added: %+v", d.Aspects.Added)
	}
	if len(d.Aspects.Removed) != 1 || d.Aspects.Removed[0] != "old_aspect" {
		t.Errorf("aspect removed: %+v", d.Aspects.Removed)
	}
	if len(d.Toolchains.Added) != 1 || len(d.Toolchains.Removed) != 1 {
		t.Errorf("toolchain diff: %+v", d.Toolchains)
	}
}

func TestRepositoryRulesDiff(t *testing.T) {
	a := &report.ModuleReport{
		RepositoryRules: []report.RepoRuleSpec{
			{Name: "old_repo_rule"},
			{Name: "kept_stable"},
			{Name: "kept_with_changed_attrs", Attrs: []report.AttrSpec{attr("url", "string", "", true)}},
			{Name: "_hidden", Private: true},
		},
	}
	b := &report.ModuleReport{
		RepositoryRules: []report.RepoRuleSpec{
			{Name: "kept_stable"},
			{Name: "kept_with_changed_attrs", Attrs: []report.AttrSpec{attr("url", "string", "", true), attr("sha256", "string", "", false)}},
			{Name: "new_repo_rule"},
		},
	}
	d := Compute(a, b)
	if len(d.RepositoryRules.Added) != 1 || d.RepositoryRules.Added[0] != "new_repo_rule" {
		t.Errorf("repo rule added: %+v", d.RepositoryRules.Added)
	}
	if len(d.RepositoryRules.Removed) != 1 || d.RepositoryRules.Removed[0] != "old_repo_rule" {
		t.Errorf("repo rule removed: %+v", d.RepositoryRules.Removed)
	}
	if len(d.RepositoryRules.Changed) != 1 || d.RepositoryRules.Changed[0].Name != "kept_with_changed_attrs" {
		t.Fatalf("repo rule changed: %+v", d.RepositoryRules.Changed)
	}
	ch := d.RepositoryRules.Changed[0]
	if len(ch.AttrsAdd) != 1 || ch.AttrsAdd[0].Name != "sha256" {
		t.Errorf("repo rule attr added: %+v", ch.AttrsAdd)
	}
}

func TestModuleExtensionsTagClassDiff(t *testing.T) {
	a := &report.ModuleReport{
		ModuleExtensions: []report.ModuleExtSpec{
			{Name: "pip", TagClasses: []string{"parse"}},
			{Name: "removed_ext"},
		},
	}
	b := &report.ModuleReport{
		ModuleExtensions: []report.ModuleExtSpec{
			{Name: "pip", TagClasses: []string{"parse", "override"}},
			{Name: "fresh_ext"},
		},
	}
	d := Compute(a, b)
	if len(d.ModuleExtensions.Added) != 1 || d.ModuleExtensions.Added[0] != "fresh_ext" {
		t.Errorf("modext added: %+v", d.ModuleExtensions.Added)
	}
	if len(d.ModuleExtensions.Removed) != 1 || d.ModuleExtensions.Removed[0] != "removed_ext" {
		t.Errorf("modext removed: %+v", d.ModuleExtensions.Removed)
	}
	if len(d.ModuleExtensions.Changed) != 1 || d.ModuleExtensions.Changed[0].Name != "pip" {
		t.Fatalf("modext changed: %+v", d.ModuleExtensions.Changed)
	}
	pip := d.ModuleExtensions.Changed[0]
	if len(pip.TagClassesAdded) != 1 || pip.TagClassesAdded[0] != "override" {
		t.Errorf("pip tag_classes added: %+v", pip.TagClassesAdded)
	}
	if len(pip.TagClassesRemoved) != 0 {
		t.Errorf("pip tag_classes removed: %+v", pip.TagClassesRemoved)
	}
}

func TestClassifyBreaking(t *testing.T) {
	a := &report.ModuleReport{
		Name:               "x",
		CompatibilityLevel: 1,
		Rules: []report.RuleSpec{
			rule("kept_stable"),
			rule("will_lose_attr", attr("removed_attr", "string", "", false)),
			rule("will_become_strict", attr("now_required", "string", "", false)),
			rule("will_be_removed"),
		},
		Providers: []report.ProviderSpec{
			provider("KeptInfo", "f1"),
			provider("WillLoseField", "f1", "doomed"),
			provider("WillBeRemoved"),
		},
		Macros: []report.MacroSpec{{Name: "old_macro"}},
		ModuleExtensions: []report.ModuleExtSpec{
			{Name: "kept_ext", TagClasses: []string{"keep", "doomed_tag"}},
			{Name: "will_be_removed_ext"},
		},
		RepositoryRules: []report.RepoRuleSpec{
			{Name: "kept_repo_rule"},
			{Name: "will_lose_attr_rr", Attrs: []report.AttrSpec{attr("doomed", "string", "", false)}},
			{Name: "will_become_strict_rr", Attrs: []report.AttrSpec{attr("now_required_rr", "string", "", false)}},
			{Name: "will_be_removed_rr"},
		},
	}
	b := &report.ModuleReport{
		Name:               "x",
		CompatibilityLevel: 2, // shift
		Rules: []report.RuleSpec{
			rule("kept_stable"),
			rule("will_lose_attr"),                                                          // attr removed
			rule("will_become_strict", attr("now_required", "string", "", true)),            // mandatory flip false→true
			// "will_be_removed" gone
		},
		Providers: []report.ProviderSpec{
			provider("KeptInfo", "f1"),
			provider("WillLoseField", "f1"), // doomed field removed
			// WillBeRemoved gone
		},
		Macros: []report.MacroSpec{{Name: "new_macro"}}, // macro removed isn't classified breaking — intentional
		ModuleExtensions: []report.ModuleExtSpec{
			{Name: "kept_ext", TagClasses: []string{"keep"}}, // doomed_tag removed
			// will_be_removed_ext gone
		},
		RepositoryRules: []report.RepoRuleSpec{
			{Name: "kept_repo_rule"},
			{Name: "will_lose_attr_rr"},                                                              // attr removed
			{Name: "will_become_strict_rr", Attrs: []report.AttrSpec{attr("now_required_rr", "string", "", true)}}, // mandatory flip
			// will_be_removed_rr gone
		},
	}
	d := Compute(a, b)

	// Index findings by (Kind, Symbol, Detail) for assertions.
	got := map[string]bool{}
	for _, f := range d.Breaking {
		got[string(f.Kind)+"/"+f.Symbol+"/"+f.Detail] = true
	}

	want := []string{
		"compat_level_shift/x/",
		"rule_removed/will_be_removed/",
		"rule_attr_removed/will_lose_attr/removed_attr",
		"rule_attr_now_mandatory/will_become_strict/now_required",
		"provider_removed/WillBeRemoved/",
		"provider_field_removed/WillLoseField/doomed",
		"module_extension_removed/will_be_removed_ext/",
		"module_extension_tag_class_removed/kept_ext/doomed_tag",
		"repo_rule_removed/will_be_removed_rr/",
		"repo_rule_attr_removed/will_lose_attr_rr/doomed",
		"repo_rule_attr_now_mandatory/will_become_strict_rr/now_required_rr",
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("missing breaking finding %q (have: %v)", k, keys(got))
		}
	}
	// Macro removal is intentionally NOT classified as breaking (too noisy
	// across the BCR — many internal-but-public macros come and go).
	for k := range got {
		if k == "macro_removed/old_macro/" {
			t.Errorf("macro removal should NOT be classified breaking (got %q)", k)
		}
	}
}

func TestClassifyBreakingMandatoryFlipReverseNotBreaking(t *testing.T) {
	// mandatory: true→false is a relaxation, not a break. Consumers who
	// were passing it keep working; consumers who weren't will now succeed.
	a := &report.ModuleReport{Rules: []report.RuleSpec{rule("r", attr("x", "string", "", true))}}
	b := &report.ModuleReport{Rules: []report.RuleSpec{rule("r", attr("x", "string", "", false))}}
	d := Compute(a, b)
	for _, f := range d.Breaking {
		if f.Kind == BreakingRuleAttrNowMandatory {
			t.Errorf("mandatory: yes→no should not yield rule_attr_now_mandatory; got %+v", f)
		}
	}
}

func TestClassifyBreakingNoFalsePositivesOnIdentical(t *testing.T) {
	r := &report.ModuleReport{
		Name: "x", Version: "1.0.0",
		Rules: []report.RuleSpec{rule("r1", attr("x", "string", "", false))},
	}
	d := Compute(r, r)
	if len(d.Breaking) != 0 {
		t.Errorf("identical reports should produce no breaking findings, got %+v", d.Breaking)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestIdenticalReportsHaveEmptyDiff(t *testing.T) {
	r := &report.ModuleReport{
		Name: "x", Version: "1.0.0",
		BazelDeps: []report.ModuleKey{{Name: "a", Version: "1.0.0"}},
		Rules:     []report.RuleSpec{rule("r1", attr("x", "string", "", false))},
		Providers: []report.ProviderSpec{provider("P", "f1")},
	}
	d := Compute(r, r)
	if len(d.BazelDeps.Added)+len(d.BazelDeps.Removed)+len(d.BazelDeps.Changed) != 0 {
		t.Errorf("deps not empty: %+v", d.BazelDeps)
	}
	if len(d.Rules.Added)+len(d.Rules.Removed)+len(d.Rules.Changed) != 0 {
		t.Errorf("rules not empty: %+v", d.Rules)
	}
	if d.Hermeticity != nil {
		t.Errorf("herm should be nil for identical")
	}
	if d.CompatibilityLevel != nil {
		t.Errorf("compat should be nil for identical")
	}
}
