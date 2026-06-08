package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/drift"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func newDriftCmd() *cobra.Command {
	var (
		mirrorRoot string
		upstream   string
		module     string
		format     string
		workers    int
	)
	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Compare a local canopy mirror against an upstream BCR-shape registry",
		Long: "Read-only HTTP probe against an upstream BCR registry. Renders a one-shot " +
			"drift report; does NOT touch bzlhub.db.\n\n" +
			"For the cache-writing companion that operates against a git-aware mirror, " +
			"see `bzlhub drift refresh`. The two verbs take different flags " +
			"(--root + --upstream vs --mirror + --db) because they hit different data " +
			"sources.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mirrorRoot == "" {
				return errors.New("--root is required")
			}
			if upstream == "" {
				return errors.New("--upstream is required (e.g. https://bcr.bazel.build)")
			}
			rep, err := drift.Compute(cmd.Context(), mirrorRoot, upstream, drift.Options{
				Module:  module,
				Workers: workers,
			})
			if err != nil {
				return err
			}
			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			case "text", "":
				renderDriftText(rep)
				return nil
			default:
				return fmt.Errorf("unknown --format %q (want text|json)", format)
			}
		},
	}
	cmd.Flags().StringVar(&mirrorRoot, "root", "", "local mirror root (BCR-shape directory)")
	cmd.Flags().StringVar(&upstream, "upstream", "https://bcr.bazel.build", "upstream BCR-shape registry URL")
	cmd.Flags().StringVar(&module, "module", "", "only check this single module (optional)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	cmd.Flags().IntVar(&workers, "workers", 4, "concurrent upstream fetches")
	cmd.AddCommand(newDriftRefreshCmd())
	return cmd
}

func newDriftRefreshCmd() *cobra.Command {
	var (
		dbPath     string
		mirrorPath string
	)
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Recompute drift verdicts from the local git-aware mirror",
		Long:  "Overwrites all rows. Boot-time backfill preserves populated rows; this verb does not.",
		Example: `  # Recompute drift after `+"`bzlhub sync bootstrap`"+` so chips populate without restarting serve
  bzlhub drift refresh --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mirrorPath == "" {
				return errors.New("--mirror is required (path to the .git-rooted BCR clone)")
			}
			if dbPath == "" {
				return errors.New("--db is required (path to bzlhub.db)")
			}

			svc, cleanup, err := openServiceForMirror(cmd.Context(), dbPath, mirrorPath)
			if err != nil {
				return err
			}
			defer cleanup()

			n, err := svc.RefreshDriftSummary(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("drift refresh: %d rows rewritten\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to bzlhub.db")
	cmd.Flags().StringVar(&mirrorPath, "mirror", "", "path to the .git-rooted BCR clone")
	return cmd
}

// renderDriftText prints a compact human-readable drift report.
func renderDriftText(r *drift.Report) {
	statusSym := map[drift.Status]string{
		drift.InSync:         "✓",
		drift.Behind:         "↑",
		drift.YankedUpstream: "⚠",
		drift.LocalOnly:      "•",
		drift.UpstreamError:  "✗",
	}
	fmt.Printf("drift: %s vs %s\n", r.MirrorRoot, r.UpstreamURL)
	fmt.Printf("        %d in-sync · %d behind · %d yanked · %d local-only · %d error\n\n",
		r.Summary.InSync, r.Summary.Behind, r.Summary.YankedUpstream,
		r.Summary.LocalOnly, r.Summary.UpstreamError)
	for _, m := range r.Modules {
		sym := statusSym[m.Status]
		fmt.Printf("  %s %-30s  local=%s  upstream=%s  %s\n",
			sym, m.Name, m.LocalLatest, m.UpstreamLatest, m.Status)
		if len(m.NewerUpstream) > 0 {
			fmt.Printf("       ↑ newer: %v\n", m.NewerUpstream)
		}
		if len(m.YankedAtUpstream) > 0 {
			fmt.Printf("       ⚠ yanked upstream: %v\n", m.YankedAtUpstream)
		}
		if m.Error != "" {
			fmt.Printf("       ✗ %s\n", m.Error)
		}
	}
}

// openServiceForMirror opens the store and a git-aware Mirror,
// returns a Service with both wired. Errors when mirrorPath isn't
// a git clone.
func openServiceForMirror(ctx context.Context, dbPath, mirrorPath string) (*bzlhub.Service, func(), error) {
	dbAbs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve --db %q: %w", dbPath, err)
	}
	s, err := store.Open(ctx, dbAbs)
	if err != nil {
		return nil, nil, fmt.Errorf("open store %q: %w", dbAbs, err)
	}
	cleanup := func() { _ = s.Close() }

	bk, err := backend.NewFromRoot(ctx, mirrorPath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("open mirror %q: %w", mirrorPath, err)
	}
	if _, ok := bk.(*backend.BCRMirror); !ok {
		cleanup()
		return nil, nil, fmt.Errorf("--mirror %q is not a git clone; drift refresh requires a .git-rooted BCR mirror (run `bzlhub sync bootstrap` first)", mirrorPath)
	}
	cs := bzlhub.New(s)
	attachMirror(cs, bk, slog.Default())
	return cs, cleanup, nil
}
