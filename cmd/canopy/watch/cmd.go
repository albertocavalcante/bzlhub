// Package watch implements the `canopy watch` subcommand: a foreground
// daemon that polls a forge for new commits on the registry branch,
// keeps a local worktree in sync via git fetch + reset --hard, and
// (when --db is supplied) re-ingests changed modules/<m>/<v>/ paths
// into the canopy SQLite index.
//
// File layout:
//   - cmd.go      Cobra command + watchFlags
//   - config.go   watchConfig + resolveWatchConfig (env + flag layering)
//   - run.go      runWatch entry + forge preflight + logger
//   - handler.go  syncHandler (git fetch/reset + per-version re-ingest)
//   - helpers.go  small string helpers (firstNonEmpty, short)
package watch

import (
	"time"

	"github.com/spf13/cobra"
)

// watchFlags mirrors publishFlags for the forge-config subset. The
// per-publish bits (source, requester, labels, etc.) don't apply to
// watching.
type watchFlags struct {
	worktree   string
	forge      string
	repo       string
	baseURL    string
	tokenEnv   string
	baseBranch string
	remote     string

	dbPath    string
	stateFile string

	interval    time.Duration
	maxInterval time.Duration

	verbose bool
}

// NewCmd builds the `canopy watch` subcommand.
func NewCmd() *cobra.Command {
	var f watchFlags
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Poll the registry forge for new commits and keep the local worktree in sync",
		Long: `Run a foreground daemon that polls the configured forge for new
commits on the registry branch. On each new commit, the local worktree
is sync'd via 'git fetch' + 'git reset --hard <remote>/<branch>'.
Changed modules/<name>/<version>/ paths are then re-ingested into
canopy's SQLite index when --db is supplied. Without --db the daemon
runs in sync-only mode: worktree stays current but no index updates
happen (useful for staging deploys before standing up the database).

Deployment configuration (worktree, forge, repo, token) follows the
same env + flag layering as 'canopy publish'.

Stop the daemon with SIGINT / SIGTERM.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd.Context(), f)
		},
	}

	cmd.Flags().StringVar(&f.worktree, "worktree", "", "git working clone of the registry repo (env CANOPY_REGISTRY_WORKTREE)")
	cmd.Flags().StringVar(&f.forge, "forge", "", "forge kind: github (default) | gitlab | bitbucketdc | forgejo (env CANOPY_FORGE_KIND)")
	cmd.Flags().StringVar(&f.repo, "repo", "", "forge repo identifier (env CANOPY_FORGE_REPO)")
	cmd.Flags().StringVar(&f.baseURL, "base-url", "", "forge API base URL (env CANOPY_FORGE_BASE_URL; required for bitbucketdc and forgejo)")
	cmd.Flags().StringVar(&f.tokenEnv, "token-env", "", "env var holding the PAT (default per-forge: CANOPY_GITHUB_TOKEN / CANOPY_GITLAB_TOKEN / CANOPY_BITBUCKET_TOKEN / CANOPY_FORGEJO_TOKEN)")
	cmd.Flags().StringVar(&f.baseBranch, "base-branch", "", "branch to watch (env CANOPY_REGISTRY_BASE_BRANCH; default main)")
	cmd.Flags().StringVar(&f.remote, "remote", "origin", "git remote name to fetch from")

	cmd.Flags().StringVar(&f.dbPath, "db", "", "SQLite index path for re-ingest (omit for sync-only mode)")
	cmd.Flags().StringVar(&f.stateFile, "state-file", "", "path to JSON state file (env CANOPY_WATCH_STATE_FILE; default $HOME/.canopy/watch-state.json)")

	cmd.Flags().DurationVar(&f.interval, "interval", 0, "base poll interval (env CANOPY_WATCH_INTERVAL; default 60s)")
	cmd.Flags().DurationVar(&f.maxInterval, "max-interval", 0, "upper bound on adaptive backoff (default 5× --interval)")

	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "log each poll cycle + git command")

	return cmd
}
