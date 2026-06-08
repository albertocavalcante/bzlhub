package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/verify"
)

// Exit codes for `bzlhub verify` — distinct from cobra's generic
// exit-1 ("flag/usage error") so CI scripts can tell "tool crashed"
// apart from "checks found a problem".
const (
	exitCodeVerifyFindings = 2
)

func newVerifyCmd() *cobra.Command {
	var (
		rootDir   string
		dbPath    string
		deep      bool
		format    string
		failOnAny bool
		checks    []string
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Integrity + consistency checks over a local canopy mirror",
		Long: `verify runs five integrity + consistency checks against a local canopy
mirror, in one pass:

  1. blob_integrity         — tarball SHA256 matches source.json SRI
  2. source_json_schema     — source.json has url + well-formed integrity
  3. module_bazel_present   — modules/<m>/<v>/MODULE.bazel exists + parses
  4. index_mirror_agreement — SQLite index and mirror tree align
  5. orphan_blobs           — blobs/ contains nothing unreferenced

  + optional --deep: re-runs assay on each module and diffs the result
  against the stored ModuleReport. Slow on large mirrors; opt-in.

Exit codes:
  0  no errors and no warnings (info-only findings are still 0)
  2  at least one error, or --fail-on-any with any finding
  1  tool itself failed (couldn't open db, missing flag, etc.)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rootDir == "" {
				return fmt.Errorf("--root is required (the BCR-shape mirror directory)")
			}
			opts := verify.Options{
				MirrorRoot: rootDir,
				DBPath:     dbPath,
				Deep:       deep,
			}
			for _, c := range checks {
				opts.Checks = append(opts.Checks, verify.Kind(c))
			}
			r, err := verify.Verify(cmd.Context(), opts)
			if err != nil {
				return err
			}
			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(r); err != nil {
					return err
				}
			case "markdown", "md":
				renderVerifyMarkdown(os.Stdout, r)
			case "text", "":
				renderVerifyText(os.Stdout, r)
			default:
				return fmt.Errorf("unknown --format %q (want text|json|markdown)", format)
			}
			if r.Errors > 0 || (failOnAny && (r.Warnings > 0 || r.Info > 0)) {
				os.Exit(exitCodeVerifyFindings)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&rootDir, "root", "", "BCR-shape mirror directory (required)")
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path (optional; enables index_mirror_agreement)")
	cmd.Flags().BoolVar(&deep, "deep", false, "re-assay each module and diff against the stored report (slow)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json | markdown")
	cmd.Flags().BoolVar(&failOnAny, "fail-on-any", false, "exit 2 on any finding (warnings + info), not just errors")
	cmd.Flags().StringSliceVar(&checks, "check", nil, "restrict to specific checks (repeatable, e.g. --check blob_integrity)")
	return cmd
}

// renderVerifyText prints a compact human-readable summary. Designed
// for fast triage: header lines first, then findings grouped by
// severity (errors → warnings → info), each with a one-line fix hint
// so the operator knows the next step without grepping docs.
func renderVerifyText(w io.Writer, r *verify.Report) {
	fmt.Fprintf(w, "verifying %s", r.MirrorRoot)
	if r.DBPath != "" {
		fmt.Fprintf(w, " against %s", r.DBPath)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  modules examined: %d\n", r.ModulesExamined)
	fmt.Fprintf(w, "  blobs examined:   %d\n", r.BlobsExamined)
	fmt.Fprintf(w, "  findings:         %d (%d error%s, %d warning%s, %d info)\n",
		len(r.Findings), r.Errors, plural(r.Errors), r.Warnings, plural(r.Warnings), r.Info)
	fmt.Fprintf(w, "  elapsed:          %s\n", r.Elapsed)

	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "\n  ✓ no findings — mirror looks healthy")
		return
	}
	fmt.Fprintln(w)
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  %-6s [%s]  %s\n", severityLabel(f.Severity), f.Kind, locator(f))
		fmt.Fprintf(w, "         %s\n", f.Message)
		if len(f.Details) > 0 {
			for _, k := range sortedDetailKeys(f.Details) {
				fmt.Fprintf(w, "         %s: %v\n", k, f.Details[k])
			}
		}
		if f.Fix != "" {
			fmt.Fprintf(w, "         fix: %s\n", f.Fix)
		}
		fmt.Fprintln(w)
	}
}

// locator formats the most-specific identifier we have for a finding:
// module@version when present, otherwise the bare path.
func locator(f verify.Finding) string {
	switch {
	case f.Module != "" && f.Version != "":
		s := f.Module + "@" + f.Version
		if f.Path != "" {
			s += " (" + f.Path + ")"
		}
		return s
	case f.Module != "":
		return f.Module
	default:
		return f.Path
	}
}

func severityLabel(s verify.Severity) string {
	switch s {
	case verify.SevError:
		return "ERROR"
	case verify.SevWarning:
		return "WARN"
	case verify.SevInfo:
		return "INFO"
	}
	return strings.ToUpper(string(s))
}

// renderVerifyMarkdown produces a PR-body-ready summary of the verify
// report. Same data the text renderer surfaces, but with headings +
// code-fenced details so an operator can paste a "mirror health"
// check into an incident issue or runbook.
func renderVerifyMarkdown(w io.Writer, r *verify.Report) {
	fmt.Fprintf(w, "## bzlhub verify — `%s`\n\n", r.MirrorRoot)
	fmt.Fprintf(w, "- modules examined: **%d**\n", r.ModulesExamined)
	fmt.Fprintf(w, "- blobs examined: **%d**\n", r.BlobsExamined)
	fmt.Fprintf(w, "- findings: **%d** (%d error%s, %d warning%s, %d info)\n",
		len(r.Findings), r.Errors, plural(r.Errors), r.Warnings, plural(r.Warnings), r.Info)
	fmt.Fprintf(w, "- elapsed: %s\n\n", r.Elapsed)

	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "✓ **No findings — mirror is healthy.**")
		return
	}

	for _, sev := range []verify.Severity{verify.SevError, verify.SevWarning, verify.SevInfo} {
		bucket := []verify.Finding{}
		for _, f := range r.Findings {
			if f.Severity == sev {
				bucket = append(bucket, f)
			}
		}
		if len(bucket) == 0 {
			continue
		}
		fmt.Fprintf(w, "### %s · %d finding%s\n\n", severityHeader(sev), len(bucket), plural(len(bucket)))
		for _, f := range bucket {
			fmt.Fprintf(w, "- **`%s`** %s\n", f.Kind, locator(f))
			fmt.Fprintf(w, "  - %s\n", f.Message)
			if len(f.Details) > 0 {
				for _, k := range sortedDetailKeys(f.Details) {
					fmt.Fprintf(w, "  - `%s`: `%v`\n", k, f.Details[k])
				}
			}
			if f.Fix != "" {
				fmt.Fprintf(w, "  - fix: %s\n", f.Fix)
			}
		}
		fmt.Fprintln(w)
	}
}

// severityHeader is the markdown-friendly label (with leading emoji
// signal so the section is scannable in a long PR description).
func severityHeader(s verify.Severity) string {
	switch s {
	case verify.SevError:
		return "⚠ Errors"
	case verify.SevWarning:
		return "Warnings"
	case verify.SevInfo:
		return "Info"
	}
	return strings.ToUpper(string(s))
}

// sortedDetailKeys orders Finding.Details keys for deterministic
// text output. Named distinctly from diff_closure.go's sortedKeys
// (which takes map[string]int) — the type difference rules out a
// shared helper without generics gymnastics.
func sortedDetailKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
