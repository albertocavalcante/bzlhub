package publish

import (
	"fmt"
	"os"

	"github.com/albertocavalcante/canopy/internal/publish"
)

// buildPublisher selects the publisher impl from cfg:
//   - dry-run: FilesystemPublisher in a scratch dir (no git ops).
//   - --commit: GitDirectPublisher against the real worktree.
//   - default: GitPRPublisher against the real worktree + forge.
//
// Returns a cleanup func that callers must defer; it's a no-op outside
// dry-run mode (where it removes the scratch dir).
func buildPublisher(cfg publishConfig) (publish.Publisher, func(), error) {
	noop := func() {}
	switch {
	case cfg.dryRun:
		scratch, err := os.MkdirTemp("", "canopy-publish-dryrun-*")
		if err != nil {
			return nil, noop, fmt.Errorf("canopy publish: alloc dry-run scratch: %w", err)
		}
		cleanup := func() { _ = os.RemoveAll(scratch) }
		fp, err := publish.NewFilesystem(scratch)
		if err != nil {
			cleanup()
			return nil, noop, fmt.Errorf("canopy publish: init scratch filesystem: %w", err)
		}
		return fp, cleanup, nil
	case cfg.commitMode:
		gd, err := publish.NewGitDirect(publish.GitDirectConfig{
			WorkTree:    cfg.worktree,
			Branch:      cfg.baseBranch,
			BotIdentity: cfg.bot,
		})
		if err != nil {
			return nil, noop, fmt.Errorf("canopy publish: init git-direct publisher: %w", err)
		}
		return gd, noop, nil
	default:
		gp, err := publish.NewGitPR(publish.GitPRConfig{
			WorkTree:    cfg.worktree,
			BaseBranch:  cfg.baseBranch,
			BotIdentity: cfg.bot,
			Repo:        cfg.repo,
			Forge:       cfg.forgeClient,
			Labels:      cfg.labels,
		})
		if err != nil {
			return nil, noop, fmt.Errorf("canopy publish: init git-pr publisher: %w", err)
		}
		return gp, noop, nil
	}
}
