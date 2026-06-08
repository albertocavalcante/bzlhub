package compat

import (
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/modulediff"
)

// renderShell produces nothing when no finding carries a codemod —
// emitting a "migration" script with zero actionable rows would be
// misleading. (Discovery-only findings DO trigger script emission;
// they're useful to walk through.)
func TestRenderShell_NoFindings_ReturnsEmpty(t *testing.T) {
	r := &Result{
		Self: SelfInfo{Name: "myproj", Version: "0.1.0"},
		Deps: []DepEntry{
			{
				Name: "rules_go", FromVersion: "0.50.0", ToVersion: "0.51.0",
				InCorpus: true, BreakingCount: 0,
				// No findings → no codemods → no script.
			},
		},
	}
	if got := renderShell(r); got != "" {
		t.Errorf("expected empty script, got:\n%s", got)
	}
}

// renderShell wraps each clean codemod in run() so the dry-run /
// --apply dispatch happens uniformly. Discovery comments (lines
// starting with "#") pass through verbatim + echo a [manual] hint.
func TestRenderShell_GoldenScript(t *testing.T) {
	r := &Result{
		Self: SelfInfo{Name: "myproj", Version: "0.1.0"},
		Deps: []DepEntry{
			{
				Name: "rules_cc", FromVersion: "0.0.9", ToVersion: "0.0.10",
				InCorpus: true, BreakingCount: 2,
				Findings: []modulediff.BreakingFinding{
					{
						Kind:    modulediff.BreakingRuleAttrRemoved,
						Symbol:  "cc_binary",
						Detail:  "linkstatic_legacy",
						Hint:    "Remove `linkstatic_legacy = ...` from every call to `cc_binary(...)`.",
						Codemod: "buildozer 'remove linkstatic_legacy' '//...:%cc_binary'",
					},
					{
						Kind:    modulediff.BreakingRuleRemoved,
						Symbol:  "cc_legacy_setup",
						Hint:    "Remove all calls to `cc_legacy_setup(...)`.",
						Codemod: "# review: grep -rn 'cc_legacy_setup(' . --include=BUILD",
					},
				},
			},
			{
				Name: "rules_python", FromVersion: "0.30.0", ToVersion: "0.31.0",
				InCorpus: true, BreakingCount: 1,
				Findings: []modulediff.BreakingFinding{
					{
						Kind:   modulediff.BreakingRuleAttrNowMandatory,
						Symbol: "py_binary",
						Detail: "main",
						Hint:   "Add `main = ...` to every call to `py_binary(...)`; the attribute is no longer optional.",
						// No codemod — needs a value the analyzer
						// doesn't have. Should render as [manual].
					},
				},
			},
		},
	}
	got := renderShell(r)
	if got == "" {
		t.Fatal("expected non-empty script")
	}

	// Structural asserts: each row's presence + ordering.
	requiredSubstrings := []string{
		"#!/usr/bin/env bash",
		"# Module: myproj@0.1.0",
		"SAFETY:",
		`MODE="${1:-}"`,
		`BUILDOZER="${BUILDOZER:-buildozer}"`,
		// Per-dep headers
		"# --- rules_cc: 0.0.9 → 0.0.10 ---",
		"# --- rules_python: 0.30.0 → 0.31.0 ---",
		// Clean codemod gets wrapped in run()
		"run buildozer 'remove linkstatic_legacy' '//...:%cc_binary'",
		// Discovery comment passes through + echo [manual]
		"# review: grep -rn 'cc_legacy_setup(' . --include=BUILD",
		`echo "[manual] review: grep`,
		// Empty-codemod finding gets [manual] with the hint
		`[manual] rule_attr_now_mandatory`,
		// Closing summary
		`Dry-run complete. Re-run with --apply to mutate files.`,
	}
	for _, want := range requiredSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q\n---script---\n%s", want, got)
		}
	}

	// Per-dep ordering: rules_cc header must appear before rules_python.
	cc := strings.Index(got, "# --- rules_cc")
	py := strings.Index(got, "# --- rules_python")
	if cc < 0 || py < 0 || cc > py {
		t.Errorf("dep ordering broken: cc=%d py=%d", cc, py)
	}
}

// Sanity check that double-quote / backtick / $ in hints don't
// produce malformed bash. Canopy controls all hint strings, so this
// is correctness-of-rendering, not shell-injection defense.
func TestRenderShell_EscapesSpecialCharsInHint(t *testing.T) {
	r := &Result{
		Deps: []DepEntry{
			{
				Name: "x", FromVersion: "1", ToVersion: "2",
				Findings: []modulediff.BreakingFinding{
					{
						// One real codemod so the script is emitted.
						Kind:    modulediff.BreakingRuleAttrRemoved,
						Symbol:  "trigger",
						Detail:  "x",
						Codemod: "buildozer 'remove x' '//...:%trigger'",
					},
					{
						// Empty codemod → renderFinding echoes the
						// Hint string verbatim; that's the path where
						// escaping matters.
						Kind:   modulediff.BreakingRuleAttrNowMandatory,
						Symbol: "weird",
						Detail: "x",
						Hint:   `Replace "foo" with $bar via "buildozer"`,
					},
				},
			},
		},
	}
	got := renderShell(r)
	// The hint's literal " and $ characters must appear escaped in
	// the bash echo command — otherwise the dollar would trigger
	// variable expansion (or unset-var error under `set -u`).
	if !strings.Contains(got, `\"foo\"`) {
		t.Errorf("expected escaped quotes in echo hint:\n%s", got)
	}
	if !strings.Contains(got, `\$bar`) {
		t.Errorf("expected escaped dollar in echo hint:\n%s", got)
	}
}
