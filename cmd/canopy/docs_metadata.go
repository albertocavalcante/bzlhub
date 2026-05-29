package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/store"
	"github.com/albertocavalcante/stardoc-go"
)

// newExportDocsCmd renders a stored module's ModuleReport as Stardoc-
// shape Markdown via stardoc-go and writes it to stdout. The
// "publish-grade docs without running canopy as a server" workflow:
// pipe to a file, commit it to a docs site, post to a wiki.
//
// `canopy export-docs rules_go@0.50.1 > rules_go.md` is the canonical
// invocation. --include-private flips Stardoc's default (hidden) to
// surface underscore-prefixed symbols for internal-API docs.
func newExportDocsCmd() *cobra.Command {
	var (
		dbPath         string
		includePrivate bool
	)
	cmd := &cobra.Command{
		Use:   "export-docs <module>@<version>",
		Short: "Render a stored module as Stardoc-shape Markdown to stdout",
		Long: `Render a stored module-version's analysis as Stardoc-shape Markdown.

Uses stardoc-go (~/dev/ws/stardoc-go) to turn the assay ModuleReport
into a single Markdown document with per-symbol anchors, kind tags,
arg + attribute tables, and parsed-docstring sections.

The output is deterministic — same input produces byte-identical
output across runs — so it's safe to commit alongside source and let
CI verify there's no drift.

  canopy export-docs rules_go@0.50.1 > docs/rules_go.md
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, version, ok := splitModuleVersion(args[0])
			if !ok {
				return fmt.Errorf("expected <module>@<version>, got %q", args[0])
			}
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			rep, err := s.GetReport(cmd.Context(), name, version)
			if err != nil {
				return fmt.Errorf("get %s@%s: %w", name, version, err)
			}
			md := stardoc.RenderWithOptions(rep, stardoc.Options{IncludePrivate: includePrivate})
			_, err = os.Stdout.WriteString(md)
			return err
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path")
	cmd.Flags().BoolVar(&includePrivate, "include-private", false, "include symbols whose names start with underscore")
	return cmd
}

// newRefreshMetadataCmd backfills the upstream metadata.json fields
// (homepage / maintainers / repository / yanked_versions) into the
// local mirror for every already-indexed module. Designed for
// recovering from "I bumped a bunch of modules BEFORE the mirror
// enrichment feature landed" — one run brings the corpus up to date.
//
// Fast: one HTTP fetch + one file write per module. No source
// extraction, no SCIP generation. Errors per module are collected,
// not propagated — a single flaky upstream shouldn't abort the rest.
func newRefreshMetadataCmd() *cobra.Command {
	var (
		dbPath   string
		rootDir  string
		upstream string
	)
	cmd := &cobra.Command{
		Use:   "refresh-metadata",
		Short: "Re-pull upstream metadata.json for every indexed module",
		Long: `Walk every module in the index and re-fetch its upstream
metadata.json, merging registry-level fields (homepage, maintainers,
repository, yanked_versions) into the local mirror.

Useful after the canopy update that added upstream metadata
persistence — modules bumped earlier only have a thin local
metadata.json with no registry-level fields. One refresh-metadata
run brings the whole corpus up to date.
`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rootDir == "" {
				return errors.New("--root is required (path to the mirror tree)")
			}
			s, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer s.Close()
			svc := canopy.New(s)
			svc.MirrorRoot = rootDir
			if upstream != "" {
				svc.DefaultUpstream = upstream
			}
			res, err := svc.RefreshMetadata(cmd.Context(), upstream)
			if err != nil {
				return err
			}
			fmt.Printf("refreshed: %d\nfailed:    %d\n", res.Refreshed, res.Failed)
			for _, e := range res.Errors {
				fmt.Fprintln(os.Stderr, "  - "+e)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath, "SQLite index path")
	cmd.Flags().StringVar(&rootDir, "root", "", "mirror root (BCR-shape tree)")
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream registry (default https://bcr.bazel.build)")
	return cmd
}
