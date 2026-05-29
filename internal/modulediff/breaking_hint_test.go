package modulediff

import (
	"strings"
	"testing"
)

func TestMigrationHint_AllKindsCovered(t *testing.T) {
	kinds := []BreakingKind{
		BreakingCompatLevelShift,
		BreakingRuleRemoved,
		BreakingRuleAttrRemoved,
		BreakingRuleAttrNowMandatory,
		BreakingProviderRemoved,
		BreakingProviderFieldRemoved,
		BreakingModuleExtensionRemoved,
		BreakingModuleExtensionTagRemoved,
		BreakingRepoRuleRemoved,
		BreakingRepoRuleAttrRemoved,
		BreakingRepoRuleAttrNowMandatory,
	}
	for _, k := range kinds {
		f := BreakingFinding{Kind: k, Symbol: "foo", Detail: "bar"}
		hint := migrationHint(f)
		if hint == "" {
			t.Errorf("kind %q has no migration hint", k)
		}
	}
}

func TestMigrationHint_RuleAttrRemovedInterpolatesSymbolAndDetail(t *testing.T) {
	f := BreakingFinding{
		Kind:   BreakingRuleAttrRemoved,
		Symbol: "cc_binary",
		Detail: "linkstatic_legacy",
	}
	got := migrationHint(f)
	for _, want := range []string{"linkstatic_legacy", "cc_binary"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint missing %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "Remove") {
		t.Errorf("hint should be imperative: %q", got)
	}
}

func TestMigrationHint_AttrNowMandatoryAddsValue(t *testing.T) {
	f := BreakingFinding{
		Kind:   BreakingRuleAttrNowMandatory,
		Symbol: "py_binary",
		Detail: "interp",
	}
	got := migrationHint(f)
	if !strings.Contains(got, "Add") {
		t.Errorf("hint should be imperative-add: %q", got)
	}
	if !strings.Contains(got, "interp") || !strings.Contains(got, "py_binary") {
		t.Errorf("hint should interpolate symbol+detail: %q", got)
	}
}

func TestMigrationHint_HintsUseBackticksForIdentifiers(t *testing.T) {
	// Convention check: hints with code spans use backticks so the
	// frontend splitter renders them as <code>.
	f := BreakingFinding{Kind: BreakingRuleRemoved, Symbol: "my_rule"}
	got := migrationHint(f)
	if !strings.Contains(got, "`my_rule") {
		t.Errorf("hint should backtick-wrap identifiers: %q", got)
	}
}
