package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// newStatusCmd registers `bzlhub status` — at-a-glance operator
// health check. Self-contained: opens the store + Mirror in
// read-only mode, renders index + drift counters + Mirror
// freshness to stdout, exits. Doesn't need `bzlhub serve` to be
// running.
func newStatusCmd() *cobra.Command {
	var (
		dbPath     string
		mirrorPath string
		format     string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report mirror + drift cache state at a glance",
		Long: "Read-only health view. --mirror is optional; without it, only index-side " +
			"counters are reported. --format=json for monitoring scripts.",
		Example: `  # Human-readable summary with mirror staleness
  bzlhub status --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db

  # Index-only view (File-backed install, no git-aware mirror)
  bzlhub status --db=/var/bzlhub/bzlhub.db

  # Alert when last sync is older than 1 hour
  bzlhub status --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db --format=json | \
    jq -e --arg now "$(date -u +%s)" '(.last_sync | fromdate) > ($now | tonumber - 3600)'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dbPath == "" {
				return errors.New("--db is required (path to bzlhub.db)")
			}

			svc, cleanup, err := openServiceForStatus(cmd.Context(), dbPath, mirrorPath)
			if err != nil {
				return err
			}
			defer cleanup()

			status, err := svc.MirrorStatus(cmd.Context())
			if err != nil {
				return err
			}
			switch format {
			case "text", "":
				renderStatus(status)
				return nil
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			default:
				return fmt.Errorf("unknown --format %q (want text|json)", format)
			}
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to bzlhub.db")
	cmd.Flags().StringVar(&mirrorPath, "mirror", "", "path to the .git-rooted BCR clone (optional)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	return cmd
}

// openServiceForStatus opens the store and optionally the Mirror.
// Mirror is optional since status is a read-only inspection.
func openServiceForStatus(ctx context.Context, dbPath, mirrorPath string) (*bzlhub.Service, func(), error) {
	dbAbs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve --db %q: %w", dbPath, err)
	}
	s, err := store.Open(ctx, dbAbs)
	if err != nil {
		return nil, nil, fmt.Errorf("open store %q: %w", dbAbs, err)
	}
	cleanup := func() { _ = s.Close() }
	cs := bzlhub.New(s)

	if mirrorPath != "" {
		bk, err := backend.NewFromRoot(ctx, mirrorPath)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("open mirror %q: %w", mirrorPath, err)
		}
		attachMirror(cs, bk, slog.Default())
	}
	return cs, cleanup, nil
}

// renderStatus prints the report as human-readable lines, with
// the drift breakdown alphabetically sorted for diffability.
//
// The header carries the server-derived verdict in brackets so an
// operator scrolling through SSH output can spot trouble at the
// first line — no need to read the whole report to see if the
// install is amber. The verdict + per-signal breakdown are
// computed by health.DeriveLocal from the same thresholds as the
// /api/v1/system/status wire payload.
func renderStatus(r bzlhub.MirrorStatusReport) {
	fmt.Printf("bzlhub status [%s]\n", r.Computed.InstantState)
	fmt.Println("─────────────")
	// Why-breakdown first: when amber or red, the most useful info
	// is which specific check tripped + by how much. Reading order
	// matches a human's triage — verdict, then the contributing
	// signals, then the supporting data.
	if len(r.Computed.Signals) > 0 {
		fmt.Println("  signals:")
		for _, sig := range r.Computed.Signals {
			fmt.Printf("    [%s] %s — %s\n", sig.Level, sig.Kind, sig.Detail)
		}
		fmt.Println()
	}
	if r.MirrorPath != "" {
		fmt.Printf("  mirror:       %s\n", r.MirrorPath)
		fmt.Printf("  HEAD:         %s\n", r.MirrorHEAD)
		if r.LastSync.IsZero() {
			fmt.Printf("  last sync:    (never recorded)\n")
		} else {
			fmt.Printf("  last sync:    %s (%s ago)\n",
				r.LastSync.Format(time.RFC3339), humanDuration(time.Since(r.LastSync)))
		}
	} else {
		fmt.Println("  mirror:       (not configured — pass --mirror to inspect)")
	}
	fmt.Println()
	fmt.Println("  index:")
	fmt.Printf("    modules:    %d\n", r.IndexedModules)
	fmt.Printf("    versions:   %d\n", r.IndexedVersions)
	fmt.Println()
	fmt.Println("  drift:")
	if len(r.DriftByStatus) == 0 && r.PendingCompute == 0 {
		fmt.Println("    (no drift data — run `bzlhub drift refresh`)")
	} else {
		for _, k := range slices.Sorted(maps.Keys(r.DriftByStatus)) {
			fmt.Printf("    %-15s %d\n", k+":", r.DriftByStatus[k])
		}
		if r.PendingCompute > 0 {
			fmt.Printf("    %-15s %d\n", "pending:", r.PendingCompute)
		}
	}
}

// humanDuration renders a Duration as the largest unit (3m, 2h, 5d).
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
