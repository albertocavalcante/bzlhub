package compat

import (
	"fmt"
	"strings"

	"github.com/albertocavalcante/canopy/internal/modulediff"
)

// renderShell emits a `migrate.sh` bash script that applies every
// Buildozer codemod the analyzer was able to derive from the result's
// BreakingFindings, plus commented "[manual]" rows for findings that
// only have discovery aids.
//
// Conventions (see docs/plans/06-buildozer-codemods.md):
//
//   - The script is a SUGGESTION, not a guarantee. Buildozer edits
//     are pattern-based and may match more sites than intended.
//   - Default to --dry-run when no arg is passed. Operator must
//     pass --apply to actually mutate files.
//   - Each dep's findings are grouped under a header so the
//     operator can navigate the script section-by-section.
//   - BUILDOZER env var overrides the binary name (for vendored
//     buildozer installs).
//
// Returns "" when the result has zero deps with non-empty Codemod
// fields — emitting a script with no actionable rows would be
// misleading. (Discovery-only "[manual]" rows DO count; they're
// useful for the operator to walk through.)
func renderShell(r *Result) string {
	// Decide up front whether any finding carries a non-empty
	// Codemod field; if not, skip script generation.
	any := false
	for _, dep := range r.Deps {
		for _, f := range dep.Findings {
			if f.Codemod != "" {
				any = true
				break
			}
		}
		if any {
			break
		}
	}
	if !any {
		return ""
	}

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("#\n")
	b.WriteString("# canopy-generated migration script.\n")
	b.WriteString("# Source: canopy compat-check analyzer\n")
	if r.Self.Name != "" {
		fmt.Fprintf(&b, "# Module: %s@%s\n", r.Self.Name, r.Self.Version)
	}
	b.WriteString("#\n")
	b.WriteString("# SAFETY: This script is a SUGGESTION. Buildozer edits are\n")
	b.WriteString("# pattern-based and may match more sites than intended.\n")
	b.WriteString("# Review each command before running --apply.\n")
	b.WriteString("#\n")
	b.WriteString("# Usage:\n")
	b.WriteString("#   bash migrate.sh           # dry-run (default): print commands\n")
	b.WriteString("#   bash migrate.sh --apply   # actually mutate files\n")
	b.WriteString("#\n")
	b.WriteString("# The BUILDOZER env var overrides the binary name when buildozer\n")
	b.WriteString("# isn't on $PATH (e.g. BUILDOZER=./tools/buildozer bash migrate.sh).\n")
	b.WriteString("\n")
	b.WriteString("set -eu\n")
	b.WriteString("\n")
	b.WriteString(`MODE="${1:-}"` + "\n")
	b.WriteString(`BUILDOZER="${BUILDOZER:-buildozer}"` + "\n")
	b.WriteString("\n")
	b.WriteString("run() {\n")
	b.WriteString(`  if [[ "$MODE" == "--apply" ]]; then` + "\n")
	b.WriteString("    echo \"+ $*\"\n")
	b.WriteString(`    "$@"` + "\n")
	b.WriteString("  else\n")
	b.WriteString(`    echo "would run: $*"` + "\n")
	b.WriteString("  fi\n")
	b.WriteString("}\n")
	b.WriteString("\n")

	// Per-dep sections. Skip deps with no findings; group findings
	// under one header per dep.
	for _, dep := range r.Deps {
		if len(dep.Findings) == 0 {
			continue
		}
		// Header
		if dep.SameVersion {
			fmt.Fprintf(&b, "# --- %s @ %s (unchanged) ---\n", dep.Name, dep.FromVersion)
		} else if dep.ToVersion != "" {
			fmt.Fprintf(&b, "# --- %s: %s → %s ---\n", dep.Name, dep.FromVersion, dep.ToVersion)
		} else {
			fmt.Fprintf(&b, "# --- %s @ %s ---\n", dep.Name, dep.FromVersion)
		}
		for _, f := range dep.Findings {
			renderFinding(&b, f)
		}
		b.WriteString("\n")
	}

	b.WriteString("echo \"\"\n")
	b.WriteString(`if [[ "$MODE" == "--apply" ]]; then` + "\n")
	b.WriteString("  echo \"Migration applied. Run 'bazel build //...' to verify.\"\n")
	b.WriteString("else\n")
	b.WriteString("  echo \"Dry-run complete. Re-run with --apply to mutate files.\"\n")
	b.WriteString("fi\n")
	return b.String()
}

// renderFinding writes one finding's section to the script buffer.
// Clean codemods become `run buildozer ...` lines; commented
// discovery commands stay as-is (prefixed with "#"); empty codemods
// drop to a "[manual]" placeholder noting the finding kind.
func renderFinding(b *strings.Builder, f modulediff.BreakingFinding) {
	// Kind label + symbol for context — keeps the script
	// self-documenting when an operator audits row-by-row.
	label := string(f.Kind)
	id := f.Symbol
	if f.Detail != "" {
		id += "." + f.Detail
	}
	fmt.Fprintf(b, "# %s: %s\n", label, id)

	switch {
	case f.Codemod == "":
		// No syntactic edit available. Surface as a manual step so
		// the operator doesn't miss it.
		fmt.Fprintf(b, "echo \"[manual] %s — %s\"\n", label, escapeForDoubleQuote(f.Hint))
	case strings.HasPrefix(f.Codemod, "#"):
		// Discovery / review-style comment from buildozerCodemod.
		// Echo it (without the leading "# ") so it appears in
		// dry-run output, and write the comment verbatim above for
		// audit readability.
		b.WriteString(f.Codemod + "\n")
		comment := strings.TrimPrefix(strings.TrimPrefix(f.Codemod, "#"), " ")
		fmt.Fprintf(b, "echo \"[manual] %s\"\n", escapeForDoubleQuote(comment))
	default:
		// Clean codemod — emit via run() so dry-run / --apply
		// dispatch works uniformly.
		fmt.Fprintf(b, "run %s\n", f.Codemod)
	}
}

// escapeForDoubleQuote escapes the characters that have meaning
// inside a bash double-quoted string: backslash, double-quote, and
// the unescaped `$` and backtick that would re-trigger expansion.
// Hints come from canopy's classification (no user input), so the
// failure mode of bad escaping is a misformatted message, not a
// shell injection; the escape is for legibility.
func escapeForDoubleQuote(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"`", "\\`",
		`$`, `\$`,
	)
	return r.Replace(s)
}
