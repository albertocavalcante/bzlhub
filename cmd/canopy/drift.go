package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/canopy/internal/drift"
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
