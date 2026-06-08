// Package diff implements the `bzlhub diff` + `bzlhub diff-closure`
// subcommands: per-module structural diff and full bazel_dep closure
// rollup. File layout:
//   - cmd.go       both cobra commands + small format helpers
//   - text.go      terminal text renderers
//   - markdown.go  PR-body markdown renderers
//   - helpers.go   closure-summary adapter + sort/format primitives
package diff

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// exitCodeBreaking is what `bzlhub diff --fail-if-breaking` returns when
// breaking findings exist. Distinct from cobra's generic-error exit code 1
// so CI scripts can tell "tool crashed" apart from "diff says breaking".
const exitCodeBreaking = 2

// DefaultDBPath is the SQLite index path the cobra commands default to
// when --db is unset. Match cmd/bzlhub/main.go's defaultDBPath.
const DefaultDBPath = "bzlhub.db"

// NewCmd builds the `bzlhub diff` subcommand.
func NewCmd() *cobra.Command {
	var (
		dbPath         string
		upstream       string
		format         string
		failIfBreaking bool
	)
	cmd := &cobra.Command{
		Use:   "diff <module> <from> <to>",
		Short: "Structured public-surface diff between two versions of a module",
		Long: `Diff compares two ModuleReports of the same Bazel module and prints the
delta: bazel_deps, rules (with per-attr changes), providers, macros, aspects,
toolchains, repository_rules (with per-attr changes), module_extensions (with
tag_class changes), hermeticity classes, and compatibility_level.

Both versions must be in the local index. Pass --upstream to enable what-if
mode: any missing side is fetched + analyzed from that BCR-shape registry on
the fly without persisting (useful for previewing a bump before committing).

The --format=markdown output is shaped for pasting into a PR description.`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			module, from, to := args[0], args[1], args[2]
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			svc := bzlhub.New(s)
			d, err := svc.Diff(cmd.Context(), api.DiffOptions{
				Module:      module,
				FromVersion: from,
				ToVersion:   to,
				Upstream:    upstream,
			})
			if err != nil {
				return err
			}
			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(d); err != nil {
					return err
				}
			case "markdown", "md":
				if err := renderDiffMarkdown(os.Stdout, d); err != nil {
					return err
				}
			case "text", "":
				if err := renderDiffText(os.Stdout, d); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown --format %q (want text|json|markdown)", format)
			}
			if failIfBreaking && len(d.Breaking) > 0 {
				fmt.Fprintf(os.Stderr, "\ncanopy diff: %d breaking change%s detected — exiting %d\n",
					len(d.Breaking), plural(len(d.Breaking)), exitCodeBreaking)
				os.Exit(exitCodeBreaking)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", DefaultDBPath, "SQLite index path")
	cmd.Flags().StringVar(&upstream, "upstream", "", "BCR-shape registry URL for what-if fallback (fetch + analyze any version not in --db)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json | markdown")
	cmd.Flags().BoolVar(&failIfBreaking, "fail-if-breaking", false, "exit 2 if the diff contains any structurally-breaking changes (compat_level shift, removed rule/provider/extension, removed attribute, mandatory flip, etc.)")
	return cmd
}

// NewClosureCmd builds the `bzlhub diff-closure` subcommand.
func NewClosureCmd() *cobra.Command {
	var (
		dbPath         string
		upstream       string
		format         string
		failIfBreaking bool
	)
	cmd := &cobra.Command{
		Use:   "diff-closure <module> <from> <to>",
		Short: "Recursive bazel_dep closure diff with breaking-change rollup",
		Long: `DiffClosure walks the bazel_dep closure on each side via MVS, runs a
per-module diff for every dep whose version moved (including the root),
and rolls up the breaking findings into a closure-wide total.

This is the migration-impact preview: the true blast radius of a bump
isn't just the root module — it's every transitive dep MVS drags along.

Requires --upstream (closure walking needs a registry). Each module
pair is analyzed via the same local-or-upstream fallback as 'bzlhub diff';
modules already in --db are served locally, missing modules are fetched
on the fly from the registry without persistence.

The --format=markdown output drops into a PR description; combine with
--fail-if-breaking in CI to gate on closure-wide structural impact.`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			module, from, to := args[0], args[1], args[2]
			if upstream == "" {
				return fmt.Errorf("--upstream is required (closure walking needs a registry URL)")
			}
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			svc := bzlhub.New(s)
			d, err := svc.DiffClosure(cmd.Context(), api.DiffOptions{
				Module:      module,
				FromVersion: from,
				ToVersion:   to,
				Upstream:    upstream,
			})
			if err != nil {
				return err
			}
			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(d); err != nil {
					return err
				}
			case "markdown", "md":
				if err := renderClosureMarkdown(os.Stdout, d); err != nil {
					return err
				}
			case "text", "":
				if err := renderClosureText(os.Stdout, d); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown --format %q (want text|json|markdown)", format)
			}
			if failIfBreaking && d.ClosureBreakingTotal > 0 {
				fmt.Fprintf(os.Stderr, "\ncanopy diff-closure: %d closure-wide breaking finding%s across %d module%s — exiting %d\n",
					d.ClosureBreakingTotal, plural(d.ClosureBreakingTotal),
					len(d.ClosureBreakingByModule), plural(len(d.ClosureBreakingByModule)),
					exitCodeBreaking)
				os.Exit(exitCodeBreaking)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", DefaultDBPath, "SQLite index path; modules present here are reused locally, others fetched from --upstream")
	cmd.Flags().StringVar(&upstream, "upstream", "", "BCR-shape registry URL (REQUIRED — MVS resolution needs one)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json | markdown")
	cmd.Flags().BoolVar(&failIfBreaking, "fail-if-breaking", false, "exit 2 if ClosureBreakingTotal > 0")
	return cmd
}
