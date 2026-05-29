package publish

import (
	"context"
	"fmt"
	"time"

	"github.com/albertocavalcante/canopy/internal/publish"
)

// finalizeDryRun runs the publisher's materialize step against the
// scratch dir (no forge / no git side effects) and emits a dry-run result.
func finalizeDryRun(ctx context.Context, pub publish.Publisher, req publish.PublishRequest, o *publishOutput, cfg publishConfig, module, ver string, start time.Time) error {
	if _, err := pub.Publish(ctx, req); err != nil {
		return fmt.Errorf("canopy publish [dry-run]: %w", err)
	}
	branch := publish.BranchName("add", module, ver)
	o.dryRunSummary(cfg.commitMode, cfg.baseBranch, branch, module, ver)
	strat := "dry-run-pr"
	if cfg.commitMode {
		strat = "dry-run-commit"
	}
	return o.emitResult(publishResult{
		Module:     module,
		Version:    ver,
		HeadBranch: branchForMode(cfg.commitMode, cfg.baseBranch, branch),
		BaseBranch: cfg.baseBranch,
		Strategy:   strat,
		DurationMs: time.Since(start).Milliseconds(),
		DryRun:     true,
	})
}

// finalizePublish runs the real publish side effects (commit + push or
// commit + push + PR) and emits the receipt.
func finalizePublish(ctx context.Context, pub publish.Publisher, req publish.PublishRequest, o *publishOutput, cfg publishConfig, module, ver string, start time.Time) error {
	if cfg.commitMode {
		o.step("committing + pushing to %s", cfg.baseBranch)
	} else {
		o.step("pushing branch + opening PR")
	}
	receipt, err := pub.Publish(ctx, req)
	if err != nil {
		return fmt.Errorf("canopy publish: %w", err)
	}
	return o.emitResult(publishResult{
		Module:     module,
		Version:    ver,
		PRURL:      receipt.PRURL,
		PRNumber:   receipt.PRNumber,
		Commit:     receipt.Commit,
		HeadBranch: branchForMode(cfg.commitMode, cfg.baseBranch, publish.BranchName("add", module, ver)),
		BaseBranch: cfg.baseBranch,
		Strategy:   receipt.Strategy,
		DurationMs: time.Since(start).Milliseconds(),
	})
}

// branchForMode returns the branch the result advertises as "head":
//   - PR mode: the feature branch (e.g., canopy/add-foo-1.0.0).
//   - commit mode: the base branch itself (the commit landed there).
func branchForMode(commitMode bool, base, feature string) string {
	if commitMode {
		return base
	}
	return feature
}
