package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// newReindexCmd regenerates stored SCIP indexes from mirrored sources.
//
// Needed because an index is a snapshot of what the indexer knew when it ran,
// and two changes since then alter its contents: the symbol grammar fix (older
// symbols are rejected by the reference parser, so nothing resolves them) and
// the transitive closure (a reference to a dependency-of-a-dependency used to
// resolve to nothing). Neither can be applied to bytes already on disk.
//
// Offline: sources come from the mirror, dependency coordinates from the store
// and mirror. No outbound traffic.
func newReindexCmd() *cobra.Command {
	var (
		dbPath   string
		mirrorTo string
		cacheDir string
		only     []string
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Regenerate stored SCIP indexes from mirrored sources.",
		Long: "Regenerate stored SCIP indexes from mirrored sources.\n\n" +
			"An index records what the indexer knew when it ran. Symbols emitted before\n" +
			"the grammar fix are rejected by the reference parser, and references to a\n" +
			"dependency-of-a-dependency did not resolve before the transitive closure\n" +
			"landed. Regenerating is the only way to pick either up.\n\n" +
			"Reads sources from the mirror; makes no network calls. Run with --dry-run\n" +
			"first to see the scope.",
		Args: cobra.NoArgs,
		// No SilenceErrors here. The root command sets it false so cobra prints
		// "Error: ...", and overriding it on a child swallows the message: the
		// command exited 1 with nothing on stderr, which is the failure mode
		// this whole command exists to clean up.
		RunE: func(cmd *cobra.Command, _ []string) error {
			targets, err := parseCoordinates(only)
			if err != nil {
				return err
			}

			db, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			svc := bzlhub.New(db)
			svc.MirrorRoot = mirrorTo
			svc.SourcesCacheDir = cacheDir
			if svc.SourcesCacheDir == "" {
				svc.SourcesCacheDir = defaultSourcesCacheDir()
			}

			res, err := svc.ReindexScip(cmd.Context(), bzlhub.ReindexOptions{
				Only:   targets,
				DryRun: dryRun,
			})
			if err != nil {
				return err
			}

			verb := "reindexed"
			if dryRun {
				verb = "would reindex"
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s %d of %d stored indexes\n", verb, res.Reindexed, res.Considered)

			// Print every skip with its reason. A count alone cannot be acted
			// on, and a silent skip is the failure this command exists to undo.
			for _, sk := range res.Skipped {
				fmt.Fprintf(out, "  skipped %s@%s: %s\n", sk.Module, sk.Version, sk.Reason)
			}
			if len(res.Skipped) > 0 {
				fmt.Fprintf(out, "%d skipped; each is listed above with a reason\n", len(res.Skipped))
			}

			// An index with unresolved references parses and navigates within
			// itself, so without this an incompletely mirrored deployment reads
			// as finished. Each entry names a dependency to mirror.
			for _, u := range res.Unresolved {
				fmt.Fprintf(out, "  %s@%s: %d unresolved load target(s)\n",
					u.Module, u.Version, len(u.Repos))
				for _, raw := range u.Repos {
					fmt.Fprintf(out, "      %s\n", raw)
				}
			}
			if len(res.Unresolved) > 0 {
				fmt.Fprintf(out,
					"%d module(s) indexed with unresolved cross-module references; "+
						"mirror the dependencies above and reindex to resolve them\n",
					len(res.Unresolved))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "bzlhub.db", "Path to the bzlhub SQLite store.")
	cmd.Flags().StringVar(&mirrorTo, "mirror", "", "Mirror root to read module sources from. Required.")
	cmd.Flags().StringVar(&cacheDir, "sources-cache", "",
		"Where to unpack module sources (default: the same cache serve uses).")
	cmd.Flags().StringSliceVar(&only, "only", nil,
		"Restrict to these coordinates, as <module>@<version>. Repeatable. Default: every stored index.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change and write nothing.")
	return cmd
}

// parseCoordinates turns "<module>@<version>" arguments into store coordinates.
//
// A malformed coordinate is an error rather than a skipped entry: the operator
// named it explicitly, so silently reindexing fewer modules than asked for is
// the wrong answer.
func parseCoordinates(args []string) ([]store.ModuleVersion, error) {
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]store.ModuleVersion, 0, len(args))
	for _, a := range args {
		module, version, ok := strings.Cut(a, "@")
		if !ok || module == "" || version == "" {
			return nil, fmt.Errorf("--only %q: want <module>@<version>", a)
		}
		out = append(out, store.ModuleVersion{Module: module, Version: version})
	}
	return out, nil
}
