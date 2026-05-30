package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/store"
)

// newSyncCmd registers `canopy sync` and its subcommands.
//
//   - `bootstrap` performs the one-time initial clone (M2 PR8).
//   - `run` does the periodic fetch + drift refresh (M3 v1).
//
// Plan 21 B4's full sync-runner contract — LAST_SYNC heartbeat,
// layered staleness fields, egress audit, signature verification,
// candidate→trusted promotion — lands in subsequent PRs on top of
// these two seams.
func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Clone and refresh a BCR-shape upstream into a local mirror",
	}
	cmd.AddCommand(newSyncBootstrapCmd())
	cmd.AddCommand(newSyncRunCmd())
	return cmd
}

// newSyncRunCmd registers `canopy sync run` — one-shot fetch +
// drift refresh. Designed to be invoked by an external scheduler
// (systemd timer, launchd, cron, GH Actions); the verb itself is
// not a daemon and does not loop.
func newSyncRunCmd() *cobra.Command {
	var (
		dbPath     string
		mirrorPath string
		force      bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Fetch upstream updates into the local mirror and recompute drift",
		Long: "Pulls the latest commits from the local mirror's configured origin (set " +
			"during `canopy sync bootstrap`) and recomputes drift verdicts for every " +
			"(module, version) row in the index.\n\n" +
			"On a divergent local mirror the verb refuses by default and exits with a " +
			"non-fast-forward error so the operator can investigate. Pass --force to " +
			"hard-reset the mirror to the remote tip — destructive against any local " +
			"commits, intended for recovery from a hand-edited mirror.\n\n" +
			"Intended for invocation from an external scheduler (systemd timer, " +
			"launchd, cron, GH Actions). The verb is one-shot; there is no internal " +
			"loop or interval flag.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mirrorPath == "" {
				return errors.New("--mirror is required (path to the .git-rooted BCR clone)")
			}
			if dbPath == "" {
				return errors.New("--db is required (path to canopy.db)")
			}

			svc, cleanup, err := openServiceForMirror(cmd.Context(), dbPath, mirrorPath)
			if err != nil {
				return err
			}
			defer cleanup()

			rec, err := svc.SyncRun(cmd.Context(), canopy.SyncRunOptions{Force: force})
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
	cmd.Flags().StringVar(&dbPath, "db", "", "path to canopy.db")
	cmd.Flags().StringVar(&mirrorPath, "mirror", "", "path to the .git-rooted BCR clone")
	cmd.Flags().BoolVar(&force, "force", false, "hard-reset to remote tip on divergent local mirror (destructive)")
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
		Long: "Performs the initial clone of an upstream BCR-shape registry into a " +
			"local on-disk mirror. Idempotent: re-running on an existing clone returns " +
			"a clear message and exits 0 unless --reinit is passed. With --reinit, the " +
			"target directory is wiped and a fresh clone is created.\n\n" +
			"After bootstrap, point `canopy serve --root <mirror-path>` at the same " +
			"directory; serve auto-detects the .git and switches to the drift-aware " +
			"backend.",
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

			rec, err := svc.SyncBootstrap(cmd.Context(), canopy.SyncBootstrapOptions{
				Remote:     remote,
				MirrorPath: mirrorPath,
				Branch:     branch,
				Reinit:     reinit,
			})
			if errors.Is(err, canopy.ErrAlreadyBootstrapped) {
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
	cmd.Flags().StringVar(&dbPath, "db", "", "path to canopy.db (where the audit_events row lands)")
	cmd.Flags().StringVar(&mirrorPath, "mirror", "", "on-disk path the BCR clone lands in")
	cmd.Flags().StringVar(&remote, "remote", "", "upstream BCR git URL")
	cmd.Flags().StringVar(&branch, "branch", "main", "upstream branch to clone")
	cmd.Flags().BoolVar(&reinit, "reinit", false, "destructively wipe and reclone an existing mirror")
	return cmd
}

// openServiceForSync opens the store at dbPath and returns a minimal
// Service wired with only what SyncBootstrap needs (the store +
// default audit shape). Mirrors the boot wiring in serve.go but
// omits the things sync-bootstrap doesn't touch (MirrorRoot, Bus,
// GitHubMeta, etc.).
func openServiceForSync(ctx context.Context, dbPath string) (*canopy.Service, func(), error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve --db %q: %w", dbPath, err)
	}
	s, err := store.Open(ctx, abs)
	if err != nil {
		return nil, nil, fmt.Errorf("open store %q: %w", abs, err)
	}
	cleanup := func() { _ = s.Close() }
	return canopy.New(s), cleanup, nil
}
