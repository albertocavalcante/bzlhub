package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/bzlhub/cmd/bzlhub/forge"
	"github.com/albertocavalcante/bzlhub/internal/forgewatch"
	"github.com/albertocavalcante/bzlhub/internal/version"
)

func runWatch(ctx context.Context, f watchFlags) error {
	cfg, err := resolveWatchConfig(f)
	if err != nil {
		return err
	}

	forgeClient, err := preflightWatchForge(ctx, cfg)
	if err != nil {
		return err
	}

	logger := newWatchLogger(cfg.verbose)

	handler, cleanup, err := buildSyncHandler(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer cleanup()

	w, err := forgewatch.New(forgewatch.Config{
		Forge:       forgeClient,
		Repo:        cfg.repo,
		Branch:      cfg.baseBranch,
		Store:       forgewatch.NewFileStore(cfg.stateFile),
		OnCommit:    handler.handle,
		Interval:    cfg.interval,
		MaxInterval: cfg.maxInterval,
		// ±10% jitter so multiple canopy instances watching the same
		// forge don't poll in lockstep. Library default is 0 (tests).
		Jitter: 0.1,
		Logger: logger,
	})
	if err != nil {
		return err
	}

	logger.Info("bzlhub watch starting",
		"repo", cfg.repo.String(),
		"branch", cfg.baseBranch,
		"interval", cfg.interval,
		"max_interval", cfg.maxInterval,
		"worktree", cfg.worktree,
		"state_file", cfg.stateFile)

	// Wire SIGINT/SIGTERM to ctx cancellation so the watcher exits
	// cleanly on Ctrl-C and on container stop.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("bzlhub watch: %w", err)
	}
	logger.Info("bzlhub watch stopped")
	return nil
}

// preflightWatchForge builds the forge client and runs Health(). Catches
// token / URL / repo misconfiguration before the poll loop starts.
func preflightWatchForge(ctx context.Context, cfg watchConfig) (bigorna.Forge, error) {
	forgeClient, err := forge.New(cfg.forge, cfg.repo, cfg.baseURL, cfg.token, "canopy-watch/"+version.Version)
	if err != nil {
		return nil, err
	}
	if err := forgeClient.Health(ctx); err != nil {
		return nil, fmt.Errorf("bzlhub watch: forge health check failed: %w", err)
	}
	return forgeClient, nil
}

func newWatchLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
