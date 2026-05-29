// publish.go — canopy publish command. See docs/plans/11-forge-library.md
// for the design context and the flag-tree rationale.
package publish

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

// NewCmd builds the `canopy publish` subcommand.
func NewCmd() *cobra.Command {
	var f publishFlags
	cmd := &cobra.Command{
		Use:   "publish <module>@<version>",
		Short: "Publish a module version to a git-backed registry via PR (default) or direct commit",
		Long: `Publish a module version to the registry.

The 80% case is curator-mirroring an upstream BCR-shape registry:

  canopy publish bazel_skylib@1.8.0 --from https://bcr.bazel.build

For first-publication of an internal module without an upstream
registry, point at the tarball directly:

  canopy publish mylib@2.0.0 \
    --source-url https://artifacts.example.com/mylib-2.0.0.tar.gz \
    --source-integrity sha256-deadbeef...

For pre-made source.json from disk:

  canopy publish mylib@2.0.0 --source-json ./source.json

Deployment configuration (worktree, forge, repo, token) is read from
env vars by default. Use --dry-run to validate config without pushing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPublish(cmd.Context(), args[0], f)
		},
	}

	// Source flags (mutually exclusive; exactly one required).
	cmd.Flags().StringVar(&f.from, "from", "", "upstream BCR-shape registry URL")
	cmd.Flags().StringVar(&f.sourceURL, "source-url", "", "direct tarball URL (requires --source-integrity)")
	cmd.Flags().StringVar(&f.sourceIntegrity, "source-integrity", "", "SRI integrity for --source-url (sha256-...)")
	cmd.Flags().StringVar(&f.sourceJSON, "source-json", "", "path to a pre-made source.json")
	cmd.MarkFlagsMutuallyExclusive("from", "source-url", "source-json")
	cmd.MarkFlagsOneRequired("from", "source-url", "source-json")
	cmd.MarkFlagsRequiredTogether("source-url", "source-integrity")

	// Deployment config (each flag has a paired env var resolved by
	// resolvePublishConfig).
	cmd.Flags().StringVar(&f.worktree, "worktree", "", "git working clone of the registry repo (env CANOPY_REGISTRY_WORKTREE)")
	cmd.Flags().StringVar(&f.forge, "forge", "", "forge kind: github (default) | gitlab | bitbucketdc | forgejo (env CANOPY_FORGE_KIND)")
	cmd.Flags().StringVar(&f.repo, "repo", "", "forge repo identifier; <owner>/<name> for github (env CANOPY_FORGE_REPO)")
	cmd.Flags().StringVar(&f.baseURL, "base-url", "", "forge API base URL (env CANOPY_FORGE_BASE_URL; defaults to api.github.com)")
	cmd.Flags().StringVar(&f.tokenEnv, "token-env", "", "env var holding the PAT (default per-forge: CANOPY_GITHUB_TOKEN / CANOPY_GITLAB_TOKEN / CANOPY_BITBUCKET_TOKEN / CANOPY_FORGEJO_TOKEN)")
	cmd.Flags().StringVar(&f.baseBranch, "base-branch", "", "base branch (env CANOPY_REGISTRY_BASE_BRANCH; default main)")
	cmd.Flags().StringVar(&f.botName, "bot-name", "", "committer name (env CANOPY_BOT_NAME; default canopy)")
	cmd.Flags().StringVar(&f.botEmail, "bot-email", "", "committer email (env CANOPY_BOT_EMAIL; default canopy@<hostname>)")

	// Mode.
	cmd.Flags().BoolVar(&f.commit, "commit", false, "push directly to base branch instead of opening a PR (requires --allow-direct)")
	cmd.Flags().BoolVar(&f.allowDirect, "allow-direct", false, "gate to acknowledge direct-push mode (with --commit)")

	// Identity overrides.
	cmd.Flags().StringVar(&f.requesterName, "requester-name", "", "commit Author name (default: git config user.name)")
	cmd.Flags().StringVar(&f.requesterEmail, "requester-email", "", "commit Author email (default: git config user.email)")

	// Other.
	cmd.Flags().StringSliceVar(&f.labels, "label", nil, "extra forge label (repeatable; ignored on Bitbucket DC)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "validate config and materialize files in a scratch dir; skip push/PR")
	cmd.Flags().BoolVar(&f.jsonOut, "json", false, "emit one-line JSON result on stdout (default: human output on stderr)")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "log each network call and git command")

	return cmd
}

// runPublish is the command's RunE. Kept thin: orchestrates the
// resolve → materialize → publish flow against helpers.
func runPublish(ctx context.Context, modAtVer string, f publishFlags) error {
	start := time.Now()

	module, ver, err := splitModuleAtVersion(modAtVer)
	if err != nil {
		return err
	}

	cfg, err := resolvePublishConfig(f)
	if err != nil {
		return err
	}
	src := publishSource{
		from:            f.from,
		directURL:       f.sourceURL,
		directIntegrity: f.sourceIntegrity,
		sourceJSONPath:  f.sourceJSON,
	}

	o := newPublishOutput(cfg.jsonOut, cfg.verbose)
	if cfg.dryRun {
		o.showConfig(cfg, src, module, ver)
	}

	if err := preflightPublish(ctx, &cfg, o); err != nil {
		return err
	}

	pub, cleanup, err := buildPublisher(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	// Resolve the source through the publisher's BlobSink so bytes are
	// SRI-verified AND captured to a content-addressed blob in a
	// single stream.
	req, err := resolveAndStage(ctx, o, src, module, ver, pub)
	if err != nil {
		return err
	}
	req.Requester = cfg.requester

	if cfg.dryRun {
		return finalizeDryRun(ctx, pub, req, o, cfg, module, ver, start)
	}
	return finalizePublish(ctx, pub, req, o, cfg, module, ver, start)
}
