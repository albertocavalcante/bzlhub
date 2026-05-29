package modulediff

import "sort"

// BreakingKind enumerates the kinds of changes that mechanically break
// consumers of a Bazel module. Severity here is *structural*: each kind
// names something that will cause a load/analysis failure in a consumer
// that exercised that surface, not a stylistic regression or a soft
// behavior change.
//
// Soft signals (macro deletion, bazel_dep version bumps, hermeticity
// downgrades) are intentionally excluded — they're worth surfacing in the
// diff itself, but not for a binary "fail the PR" decision.
type BreakingKind string

const (
	BreakingCompatLevelShift          BreakingKind = "compat_level_shift"
	BreakingRuleRemoved               BreakingKind = "rule_removed"
	BreakingRuleAttrRemoved           BreakingKind = "rule_attr_removed"
	BreakingRuleAttrNowMandatory      BreakingKind = "rule_attr_now_mandatory"
	BreakingProviderRemoved           BreakingKind = "provider_removed"
	BreakingProviderFieldRemoved      BreakingKind = "provider_field_removed"
	BreakingModuleExtensionRemoved    BreakingKind = "module_extension_removed"
	BreakingModuleExtensionTagRemoved BreakingKind = "module_extension_tag_class_removed"
	BreakingRepoRuleRemoved           BreakingKind = "repo_rule_removed"
	BreakingRepoRuleAttrRemoved       BreakingKind = "repo_rule_attr_removed"
	BreakingRepoRuleAttrNowMandatory  BreakingKind = "repo_rule_attr_now_mandatory"
)

// BreakingFinding is one structural-break signal extracted from a Report.
type BreakingFinding struct {
	Kind   BreakingKind `json:"kind"`
	// Symbol is the primary identifier the finding is about (rule name,
	// provider name, extension name, etc.).
	Symbol string `json:"symbol"`
	// Detail is a sub-identifier when applicable: attr name for rule
	// changes, field name for provider changes, tag_class name for
	// module_extension changes. Empty for finding kinds that don't
	// reference a sub-symbol (e.g. RuleRemoved).
	Detail string `json:"detail,omitempty"`
	// Reason is a one-sentence migration-impact note suitable for showing
	// to a human (PR reviewer, drift dashboard user, CI failure log).
	Reason string `json:"reason"`
	// Hint is an actionable one-liner: what the consumer should DO to
	// migrate past this break. Reason explains the symptom; Hint
	// prescribes the fix. Both are author-controlled prose; Hint is
	// derived from Kind+Symbol+Detail at classification time.
	Hint string `json:"hint,omitempty"`
	// Codemod is a ready-to-pipe Buildozer command (or commented
	// discovery command) that mechanically applies the migration
	// when a clean syntactic edit exists. Empty when the kind isn't
	// codemod-able (e.g. semantic edits like provider field reads).
	//
	// Codemods are SUGGESTIONS, not guarantees. Buildozer edits are
	// pattern-based and may match more sites than intended — UI
	// surfaces this with "review before running" and --dry-run.
	// See docs/plans/06-buildozer-codemods.md for the per-kind
	// mapping rationale.
	Codemod string `json:"codemod,omitempty"`
}

// ClassifyBreaking walks the diff Report and emits one finding per
// structural break. The result is sorted (by Kind, Symbol, Detail) so
// the output is deterministic and diff-friendly.
//
// The function is pure: it reads from r without writing to it.
func ClassifyBreaking(r *Report) []BreakingFinding {
	var out []BreakingFinding

	if r.CompatibilityLevel != nil {
		out = append(out, BreakingFinding{
			Kind:   BreakingCompatLevelShift,
			Symbol: r.Module,
			Reason: "Bazel treats different compatibility_levels as hard-incompatible; the version is unusable from any project still on the old level.",
		})
	}

	for _, name := range r.Rules.Removed {
		out = append(out, BreakingFinding{
			Kind:   BreakingRuleRemoved,
			Symbol: name,
			Reason: "BUILD files that target this rule will fail at load time.",
		})
	}
	for _, ch := range r.Rules.Changed {
		for _, a := range ch.AttrsRem {
			out = append(out, BreakingFinding{
				Kind:   BreakingRuleAttrRemoved,
				Symbol: ch.Name,
				Detail: a.Name,
				Reason: "Consumers passing this attribute will get an unknown-keyword error.",
			})
		}
		for _, a := range ch.AttrsChg {
			if a.MandatoryFlip && !a.FromMandatory && a.ToMandatory {
				out = append(out, BreakingFinding{
					Kind:   BreakingRuleAttrNowMandatory,
					Symbol: ch.Name,
					Detail: a.Name,
					Reason: "Consumers not passing this attribute will now fail; the attribute was previously optional.",
				})
			}
		}
	}

	for _, name := range r.Providers.Removed {
		out = append(out, BreakingFinding{
			Kind:   BreakingProviderRemoved,
			Symbol: name,
			Reason: "Consumers that read this provider type from a target will lose access to it.",
		})
	}
	for _, ch := range r.Providers.Changed {
		for _, f := range ch.FieldsRemoved {
			out = append(out, BreakingFinding{
				Kind:   BreakingProviderFieldRemoved,
				Symbol: ch.Name,
				Detail: f,
				Reason: "Consumers reading this field on the provider will get an attribute error.",
			})
		}
	}

	for _, name := range r.ModuleExtensions.Removed {
		out = append(out, BreakingFinding{
			Kind:   BreakingModuleExtensionRemoved,
			Symbol: name,
			Reason: "MODULE.bazel files calling use_extension(...) for this extension will fail to resolve.",
		})
	}
	for _, ch := range r.ModuleExtensions.Changed {
		for _, t := range ch.TagClassesRemoved {
			out = append(out, BreakingFinding{
				Kind:   BreakingModuleExtensionTagRemoved,
				Symbol: ch.Name,
				Detail: t,
				Reason: "MODULE.bazel files invoking this tag class on the extension will fail to load.",
			})
		}
	}

	for _, name := range r.RepositoryRules.Removed {
		out = append(out, BreakingFinding{
			Kind:   BreakingRepoRuleRemoved,
			Symbol: name,
			Reason: "Consumers calling this repository rule (typically via load(...) + WORKSPACE-era setup) will fail.",
		})
	}
	for _, ch := range r.RepositoryRules.Changed {
		for _, a := range ch.AttrsRem {
			out = append(out, BreakingFinding{
				Kind:   BreakingRepoRuleAttrRemoved,
				Symbol: ch.Name,
				Detail: a.Name,
				Reason: "Consumers passing this attribute to the repository rule will get an unknown-keyword error.",
			})
		}
		for _, a := range ch.AttrsChg {
			if a.MandatoryFlip && !a.FromMandatory && a.ToMandatory {
				out = append(out, BreakingFinding{
					Kind:   BreakingRepoRuleAttrNowMandatory,
					Symbol: ch.Name,
					Detail: a.Name,
					Reason: "Consumers not passing this attribute to the repository rule will now fail.",
				})
			}
		}
	}

	// Annotate each finding with an actionable migration hint +
	// buildozer codemod (Plan 06). Done in a separate pass so the
	// upstream classification logic stays focused on detection.
	for i := range out {
		out[i].Hint = migrationHint(out[i])
		out[i].Codemod = buildozerCodemod(out[i])
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// migrationHint returns a one-liner telling the consumer exactly
// what to change. Per-kind dispatch on the finding kind; Symbol +
// Detail get interpolated to make the hint contextual (e.g.
// "Remove `deps` from cc_binary(...) calls" rather than a generic
// "Remove the removed attribute").
//
// Convention: hints are imperative, present-tense, second-person,
// and reference identifiers in backticks so a Markdown renderer
// styles them as code. Each hint fits on one line at typical
// terminal width.
func migrationHint(f BreakingFinding) string {
	switch f.Kind {
	case BreakingCompatLevelShift:
		return "Bump every project on the old level to a release that supports the new compatibility_level, or pin to the previous version."
	case BreakingRuleRemoved:
		return "Remove all calls to `" + f.Symbol + "(...)` from your BUILD files (or pick the replacement rule from the module's CHANGELOG)."
	case BreakingRuleAttrRemoved:
		return "Remove `" + f.Detail + " = ...` from every call to `" + f.Symbol + "(...)`."
	case BreakingRuleAttrNowMandatory:
		return "Add `" + f.Detail + " = ...` to every call to `" + f.Symbol + "(...)`; the attribute is no longer optional."
	case BreakingProviderRemoved:
		return "Find every read of the `" + f.Symbol + "` provider (typically `target[" + f.Symbol + "]` in a rule's implementation) and migrate to the replacement, or guard with `" + f.Symbol + " in target`."
	case BreakingProviderFieldRemoved:
		return "Remove reads of `." + f.Detail + "` on the `" + f.Symbol + "` provider; the field no longer exists."
	case BreakingModuleExtensionRemoved:
		return "Remove `use_extension(\"...\", \"" + f.Symbol + "\")` from MODULE.bazel; the extension no longer exists."
	case BreakingModuleExtensionTagRemoved:
		return "Remove every `" + f.Symbol + "." + f.Detail + "(...)` tag call from MODULE.bazel."
	case BreakingRepoRuleRemoved:
		return "Remove uses of `" + f.Symbol + "(...)` from WORKSPACE/repo-rule setup; the repository rule no longer exists."
	case BreakingRepoRuleAttrRemoved:
		return "Remove `" + f.Detail + " = ...` from every `" + f.Symbol + "(...)` invocation."
	case BreakingRepoRuleAttrNowMandatory:
		return "Add `" + f.Detail + " = ...` to every `" + f.Symbol + "(...)` invocation; the attribute is no longer optional."
	}
	return ""
}

// buildozerCodemod returns a ready-to-pipe Buildozer command for the
// finding when a clean syntactic edit exists, or a commented
// grep-style discovery command otherwise. Plan 06: 4 of 11
// BreakingKinds map to mechanical codemods; the rest stay as
// human-review hints (with discovery aids).
//
// Output shape:
//   - codemod-able kinds → bare buildozer command (no shebang/comment)
//   - non-codemod-able kinds → leading "# " comment with a grep
//     discovery command, suitable for migrate.sh "[manual]" rows
//   - unhandled kinds → empty string (no codemod row in UI/script)
//
// Buildozer label conventions used:
//
//	'//...:%<rule>'              — all build-file rule calls
//	'//MODULE.bazel:%<rule>'     — repo-rule calls at module root
//	'//MODULE.bazel:%use_extension' — extension declarations
//
// See docs/plans/06-buildozer-codemods.md for the full mapping table.
func buildozerCodemod(f BreakingFinding) string {
	switch f.Kind {
	case BreakingRuleAttrRemoved:
		// BUILD-file edit: drop the attribute from every call.
		return "buildozer 'remove " + f.Detail + "' '//...:%" + f.Symbol + "'"
	case BreakingRepoRuleAttrRemoved:
		// MODULE.bazel edit: same attr-remove but scoped to the
		// module root where repo rules are typically invoked.
		return "buildozer 'remove " + f.Detail + "' '//MODULE.bazel:%" + f.Symbol + "'"
	case BreakingModuleExtensionRemoved:
		// Drop the use_extension declaration. Buildozer can't
		// reliably name the exact use_extension by Symbol alone
		// because consumers shadow them with arbitrary identifiers
		// (`go_deps = use_extension(...)` vs `_go = ...`). Emit a
		// commented hint with the most-likely buildozer call —
		// operator audits before running.
		return "# review: buildozer 'delete' '//MODULE.bazel:%use_extension' (consumers may rename — find by RHS, not lhs)"
	case BreakingModuleExtensionTagRemoved:
		// Tag calls are <ext_lhs>.<tag>(...). The LHS varies per
		// consumer, so we can't name an exact buildozer target.
		// Provide a discovery grep instead.
		return "# review: grep -rn '\\." + f.Detail + "(' MODULE.bazel  # tag calls of " + f.Symbol + "." + f.Detail
	case BreakingRuleRemoved:
		return "# review: grep -rn '" + f.Symbol + "(' . --include=BUILD --include=BUILD.bazel --include='*.bzl'"
	case BreakingRepoRuleRemoved:
		return "# review: grep -rn '" + f.Symbol + "(' MODULE.bazel WORKSPACE WORKSPACE.bazel"
	case BreakingProviderRemoved, BreakingProviderFieldRemoved:
		// Provider reads happen in rule impls, not BUILD files;
		// buildozer can't touch them. Discovery only.
		return "# review: grep -rn '" + f.Symbol + "' . --include='*.bzl'"
	}
	// CompatLevelShift / *NowMandatory: nothing useful to codemod —
	// these need human-decided values, not a syntactic edit.
	return ""
}
