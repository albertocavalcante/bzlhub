package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Clone and refresh a BCR-shape upstream into a local mirror",
	}
	cmd.AddCommand(newSyncBootstrapCmd())
	cmd.AddCommand(newSyncRunCmd())
	cmd.AddCommand(newSyncHistoryCmd())
	return cmd
}

func newSyncHistoryCmd() *cobra.Command {
	var (
		dbPath   string
		limit    int
		format   string
		sinceStr string
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent sync-runner activity (bootstrap + run events)",
		Example: `  # Last 25 events as a text table
  bzlhub sync history --db=/var/bzlhub/bzlhub.db

  # JSON for monitoring scripts
  bzlhub sync history --db=/var/bzlhub/bzlhub.db --format=json

  # Events from the last hour or last 7 days
  bzlhub sync history --db=/var/bzlhub/bzlhub.db --since=1h
  bzlhub sync history --db=/var/bzlhub/bzlhub.db --since=7d

  # Alert when the most recent run failed
  bzlhub sync history --db=/var/bzlhub/bzlhub.db --format=json --limit=1 | jq -e '.[0].ok'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dbPath == "" {
				return errors.New("--db is required (path to bzlhub.db)")
			}
			var since time.Time
			if sinceStr != "" {
				d, err := parseSince(sinceStr)
				if err != nil {
					return err
				}
				since = time.Now().UTC().Add(-d)
			}
			svc, cleanup, err := openServiceForSync(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer cleanup()

			entries, err := svc.SyncHistory(cmd.Context(), limit, since)
			if err != nil {
				return err
			}
			switch format {
			case "text", "":
				renderSyncHistory(entries)
				return nil
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			default:
				return fmt.Errorf("unknown --format %q (want text|json)", format)
			}
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to bzlhub.db")
	cmd.Flags().IntVar(&limit, "limit", 25, "maximum events to return")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	cmd.Flags().StringVar(&sinceStr, "since", "", "filter to events within this duration (e.g. 30m, 1h, 7d)")
	return cmd
}

// renderSyncHistory prints one line per event, newest first.
func renderSyncHistory(entries []bzlhub.SyncHistoryEntry) {
	if len(entries) == 0 {
		fmt.Println("(no sync events recorded — run `bzlhub sync bootstrap` to start)")
		return
	}
	for _, e := range entries {
		status := "OK"
		if !e.OK {
			status = "FAIL"
		}
		fmt.Printf("%s  %-25s  %-4s  %dms",
			e.Timestamp.Format("2006-01-02 15:04:05"), e.Kind, status, e.DurationMs)
		switch {
		case e.UpToDate && e.ToSHA != "":
			fmt.Printf("  HEAD=%s", short(e.ToSHA))
		case e.FromSHA != "" && e.ToSHA != "" && e.FromSHA != e.ToSHA:
			fmt.Printf("  %s→%s (%d commits)", short(e.FromSHA), short(e.ToSHA), e.Commits)
		case e.ToSHA != "":
			fmt.Printf("  HEAD=%s", short(e.ToSHA))
		}
		if e.Error != "" {
			fmt.Printf("  err=%q", e.Error)
		}
		fmt.Println()
	}
}

// short renders a hex SHA as its 7-char prefix.
func short(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func newSyncRunCmd() *cobra.Command {
	var (
		dbPath      string
		mirrorPath  string
		force       bool
		skipRefresh bool
		interval    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Fetch upstream updates and recompute drift",
		Long: "One-shot by default — invoke from an external scheduler (systemd, " +
			"cron, GH Actions). With --interval=DURATION, runs as a daemon that " +
			"syncs immediately then every DURATION until SIGINT/SIGTERM. Refuses " +
			"on a divergent local mirror unless --force is passed.",
		Example: `  # One-shot from an external scheduler
  bzlhub sync run --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db

  # Daemon mode — runs immediately then every 15 minutes until killed
  bzlhub sync run --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db --interval=15m

  # Fetch only — inspect upstream changes before drift verdicts get rewritten
  bzlhub sync run --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db --no-refresh

  # Recover from a divergent local mirror (destructive — discards local commits)
  bzlhub sync run --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db --force`,
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

			opts := bzlhub.SyncRunOptions{
				Force:       force,
				SkipRefresh: skipRefresh,
			}

			if interval > 0 {
				fmt.Printf("sync run: daemon mode, interval=%s (Ctrl-C to stop)\n", interval)
				err := svc.SyncRunLoop(cmd.Context(), opts, interval, func(rec bzlhub.SyncRunReceipt, iterErr error) {
					switch {
					case iterErr != nil:
						fmt.Fprintf(os.Stderr, "sync run: iteration failed: %v\n", iterErr)
					case rec.UpToDate:
						fmt.Printf("sync run: up-to-date (HEAD=%s)\n", rec.ToSHA)
					default:
						fmt.Printf("sync run: %s → %s (%d commits, %d drift rows, %s)\n",
							rec.FromSHA, rec.ToSHA, rec.Commits, rec.DriftRowsRewritten, rec.Duration)
					}
				})
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}

			rec, err := svc.SyncRun(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if rec.UpToDate {
				fmt.Printf("sync run: %s already at remote HEAD (%s)\n", mirrorPath, rec.ToSHA)
				return nil
			}
			fmt.Printf("sync run: %s → %s (%d commits, %d drift rows rewritten, %s)\n",
				rec.FromSHA, rec.ToSHA, rec.Commits, rec.DriftRowsRewritten, rec.Duration)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to bzlhub.db")
	cmd.Flags().StringVar(&mirrorPath, "mirror", "", "path to the .git-rooted BCR clone")
	cmd.Flags().BoolVar(&force, "force", false, "hard-reset to remote tip on divergent local mirror (destructive)")
	cmd.Flags().BoolVar(&skipRefresh, "no-refresh", false, "fetch only — skip the post-Sync drift recompute")
	cmd.Flags().DurationVar(&interval, "interval", 0, "daemon mode — sync every DURATION (e.g. 15m). 0 = one-shot")
	return cmd
}

func newSyncBootstrapCmd() *cobra.Command {
	var (
		dbPath     string
		mirrorPath string
		remote     string
		branch     string
		reinit     bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Clone the upstream BCR mirror for the first time",
		Long: "Idempotent — re-running on an existing clone returns a clear message " +
			"unless --reinit is passed. After bootstrap, `bzlhub serve --root <path>` " +
			"auto-detects the .git and switches to the drift-aware backend.",
		Example: `  # Clone upstream BCR into a local mirror
  bzlhub sync bootstrap --remote=https://github.com/bazelbuild/bazel-central-registry --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db

  # Clone an internal fork on a non-default branch
  bzlhub sync bootstrap --remote=https://gitea.internal/bcr-fork --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db --branch=trusted

  # Wipe and reclone (destructive — recovers from a hand-edited mirror)
  bzlhub sync bootstrap --remote=https://github.com/bazelbuild/bazel-central-registry --mirror=/var/bzlhub/bcr --db=/var/bzlhub/bzlhub.db --reinit`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if remote == "" {
				return errors.New("--remote is required (e.g. https://github.com/bazelbuild/bazel-central-registry)")
			}
			if mirrorPath == "" {
				return errors.New("--mirror is required (on-disk path the clone lands in)")
			}
			if dbPath == "" {
				return errors.New("--db is required (audit_events row writes there)")
			}

			svc, cleanup, err := openServiceForSync(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer cleanup()

			rec, err := svc.SyncBootstrap(cmd.Context(), bzlhub.SyncBootstrapOptions{
				Remote:     remote,
				MirrorPath: mirrorPath,
				Branch:     branch,
				Reinit:     reinit,
			})
			if errors.Is(err, bzlhub.ErrAlreadyBootstrapped) {
				fmt.Fprintf(os.Stderr, "%s already contains a clone (HEAD=%s); pass --reinit to wipe + reclone\n",
					mirrorPath, rec.SHA)
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Printf("bootstrapped %s at HEAD=%s (%s, %d bytes)\n",
				mirrorPath, rec.SHA, rec.Duration, rec.Bytes)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to bzlhub.db (where the audit_events row lands)")
	cmd.Flags().StringVar(&mirrorPath, "mirror", "", "on-disk path the BCR clone lands in")
	cmd.Flags().StringVar(&remote, "remote", "", "upstream BCR git URL")
	cmd.Flags().StringVar(&branch, "branch", "main", "upstream branch to clone")
	cmd.Flags().BoolVar(&reinit, "reinit", false, "destructively wipe and reclone an existing mirror")
	return cmd
}

// openServiceForSync opens the store and returns a Service with
// nothing else wired. Used by verbs that only touch audit_events.
func openServiceForSync(ctx context.Context, dbPath string) (*bzlhub.Service, func(), error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve --db %q: %w", dbPath, err)
	}
	s, err := store.Open(ctx, abs)
	if err != nil {
		return nil, nil, fmt.Errorf("open store %q: %w", abs, err)
	}
	cleanup := func() { _ = s.Close() }
	return bzlhub.New(s), cleanup, nil
}
