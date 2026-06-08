package publish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// preflightPublish runs both pre-flight checks before any work happens:
//   - forge health (PR mode only — commit mode skips the forge API entirely)
//   - worktree is a real git checkout
//
// On success, sets cfg.forgeClient for downstream use by buildPublisher.
func preflightPublish(ctx context.Context, cfg *publishConfig, o *publishOutput) error {
	if !cfg.commitMode {
		forgeClient, err := buildForge(*cfg)
		if err != nil {
			return err
		}
		o.step("forge: connecting to %s/%s", cfg.repo.Owner, cfg.repo.Name)
		if err := forgeClient.Health(ctx); err != nil {
			return fmt.Errorf("bzlhub publish: forge health check failed: %w", err)
		}
		cfg.forgeClient = forgeClient
	}
	if st, err := os.Stat(filepath.Join(cfg.worktree, ".git")); err != nil || !st.IsDir() {
		return fmt.Errorf("bzlhub publish: --worktree %s is not a git working tree", cfg.worktree)
	}
	return nil
}
