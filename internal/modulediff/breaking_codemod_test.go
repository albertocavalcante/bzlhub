package modulediff

import (
	"strings"
	"testing"

	"github.com/albertocavalcante/assay/report"
)

// Per-kind buildozer codemod golden-output tests. The shape locked
// in by docs/plans/06-buildozer-codemods.md: 4 kinds emit clean
// `buildozer ...` commands, 5 emit commented discovery aids, 2 are
// intentionally empty (compat_level_shift, *_now_mandatory) because
// they need human-decided values, not syntactic edits.

func TestBuildozerCodemod_CleanCodemods(t *testing.T) {
	cases := []struct {
		name string
		in   BreakingFinding
		want string
	}{
		{
			name: "rule_attr_removed → BUILD-file remove",
			in:   BreakingFinding{Kind: BreakingRuleAttrRemoved, Symbol: "cc_binary", Detail: "linkstatic_legacy"},
			want: "buildozer 'remove linkstatic_legacy' '//...:%cc_binary'",
		},
		{
			name: "repo_rule_attr_removed → MODULE.bazel scoped remove",
			in:   BreakingFinding{Kind: BreakingRepoRuleAttrRemoved, Symbol: "http_archive", Detail: "patch_strip"},
			want: "buildozer 'remove patch_strip' '//MODULE.bazel:%http_archive'",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildozerCodemod(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildozerCodemod_DiscoveryCommands(t *testing.T) {
	// These kinds CANNOT be cleanly codemod'd, but emitting a
	// commented grep keeps the operator from staring at a blank
	// "find me what's affected" task. Each must start with "#" so
	// migrate.sh treats them as no-op rows.
	cases := []struct {
		name        string
		in          BreakingFinding
		wantSubstr  string
		wantComment bool
	}{
		{
			name:        "module_extension_removed names use_extension",
			in:          BreakingFinding{Kind: BreakingModuleExtensionRemoved, Symbol: "py_deps"},
			wantSubstr:  "use_extension",
			wantComment: true,
		},
		{
			name:        "module_extension_tag_class_removed greps for the tag call",
			in:          BreakingFinding{Kind: BreakingModuleExtensionTagRemoved, Symbol: "py_deps", Detail: "parse"},
			wantSubstr:  ".parse(",
			wantComment: true,
		},
		{
			name:        "rule_removed greps BUILD files",
			in:          BreakingFinding{Kind: BreakingRuleRemoved, Symbol: "go_legacy_setup"},
			wantSubstr:  "go_legacy_setup(",
			wantComment: true,
		},
		{
			name:        "repo_rule_removed greps MODULE/WORKSPACE",
			in:          BreakingFinding{Kind: BreakingRepoRuleRemoved, Symbol: "go_repository"},
			wantSubstr:  "MODULE.bazel WORKSPACE",
			wantComment: true,
		},
		{
			name:        "provider_removed greps bzl files",
			in:          BreakingFinding{Kind: BreakingProviderRemoved, Symbol: "CcInfo"},
			wantSubstr:  "CcInfo",
			wantComment: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildozerCodemod(c.in)
			if c.wantComment && !strings.HasPrefix(got, "#") {
				t.Errorf("expected commented discovery line, got %q", got)
			}
			if !strings.Contains(got, c.wantSubstr) {
				t.Errorf("expected substring %q in %q", c.wantSubstr, got)
			}
		})
	}
}

func TestBuildozerCodemod_NoCodemodKinds(t *testing.T) {
	// Compat-level-shift and *NowMandatory both need a value the
	// codemod doesn't have. Emitting any pseudo-codemod here would
	// lie about being actionable; better to keep them out of
	// migrate.sh entirely (empty string).
	noCodemod := []BreakingKind{
		BreakingCompatLevelShift,
		BreakingRuleAttrNowMandatory,
		BreakingRepoRuleAttrNowMandatory,
	}
	for _, k := range noCodemod {
		f := BreakingFinding{Kind: k, Symbol: "foo", Detail: "bar"}
		if got := buildozerCodemod(f); got != "" {
			t.Errorf("kind %q should have no codemod, got %q", k, got)
		}
	}
}

// ClassifyBreaking annotates every finding with both Hint and
// Codemod. Verify the round-trip: a real diff produces findings that
// carry both fields populated where applicable.
func TestClassifyBreaking_AnnotatesCodemod(t *testing.T) {
	r := &Report{
		Module: "m",
		Rules: RulesDiff{
			Changed: []ChangedRule{
				{
					Name:     "cc_binary",
					AttrsRem: []report.AttrSpec{{Name: "linkstatic_legacy"}},
				},
			},
		},
	}
	out := ClassifyBreaking(r)
	if len(out) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(out))
	}
	if out[0].Codemod != "buildozer 'remove linkstatic_legacy' '//...:%cc_binary'" {
		t.Errorf("codemod not populated: %q", out[0].Codemod)
	}
	if out[0].Hint == "" {
		t.Errorf("hint should still be populated: %q", out[0].Hint)
	}
}
