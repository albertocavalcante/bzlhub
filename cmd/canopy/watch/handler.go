package watch

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/albertocavalcante/bigorna"
	"github.com/albertocavalcante/canopy/internal/ingest"
	"github.com/albertocavalcante/canopy/internal/store"
)

// buildSyncHandler assembles the OnCommit handler. When cfg.dbPath is
// set the handler re-ingests changed modules into the canopy index;
// otherwise it stays sync-only. The returned cleanup closes the db (or
// no-ops if none was opened).
func buildSyncHandler(ctx context.Context, cfg watchConfig, logger *slog.Logger) (*syncHandler, func(), error) {
	h := &syncHandler{
		worktree: cfg.worktree,
		remote:   cfg.remote,
		branch:   cfg.baseBranch,
		logger:   logger,
	}
	noop := func() {}
	if cfg.dbPath == "" {
		logger.Info("sync-only mode (no --db); workspace will stay synced but the index will not be updated")
		return h, noop, nil
	}
	s, err := store.Open(ctx, cfg.dbPath)
	if err != nil {
		return nil, noop, fmt.Errorf("canopy watch: open db %s: %w", cfg.dbPath, err)
	}
	h.canopyStore = s
	logger.Info("re-ingest enabled", "db", cfg.dbPath)
	return h, func() { _ = s.Close() }, nil
}

// syncHandler implements the OnCommit callback. Each fire:
//  1. Records the old worktree HEAD before pulling.
//  2. Runs `git fetch <remote> <branch>` + `git reset --hard <remote>/<branch>`.
//  3. Computes the changed `modules/<name>/<version>/` paths via
//     `git diff --name-only <old>..<new> -- modules/`.
//  4. If canopyStore is set, calls ingest.FromMirroredVersion for
//     each changed (module, version) pair. Per-module failures are
//     logged but don't abort the rest of the batch.
//
// Re-ingest is at-least-once: if the callback returns an error, the
// forgewatch loop won't advance state, so the same commits + same
// (module, version) pairs replay on the next poll. FromMirroredVersion
// is idempotent (WriteReport replaces).
type syncHandler struct {
	worktree    string
	remote      string
	branch      string
	logger      *slog.Logger
	canopyStore *store.Store // nil → sync-only mode, no re-ingest
}

func (h *syncHandler) handle(ctx context.Context, commits []bigorna.Commit) error {
	h.logger.Info("new commits detected",
		"count", len(commits),
		"newest", short(commits[0].SHA))

	// Capture old HEAD before the reset so we can diff afterward.
	oldHead, err := h.gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		// Worktree might be in an uninitialized state (no commits yet).
		// Treat as "no previous HEAD" and proceed; the diff will fall
		// through to listing everything under modules/.
		h.logger.Debug("could not read old HEAD; treating as fresh worktree", "err", err)
		oldHead = ""
	}
	oldHead = strings.TrimSpace(oldHead)

	if err := h.gitRun(ctx, "fetch", "--quiet", h.remote, h.branch); err != nil {
		return fmt.Errorf("git fetch %s %s: %w", h.remote, h.branch, err)
	}
	if err := h.gitRun(ctx, "reset", "--hard", "--quiet", h.remote+"/"+h.branch); err != nil {
		return fmt.Errorf("git reset --hard %s/%s: %w", h.remote, h.branch, err)
	}

	// New HEAD after reset.
	newHead, err := h.gitOutput(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read new HEAD: %w", err)
	}
	newHead = strings.TrimSpace(newHead)
	h.logger.Info("worktree synced", "from", short(oldHead), "to", short(newHead))

	// Diff the changed module versions.
	changed, err := h.changedModuleVersions(ctx, oldHead, newHead)
	if err != nil {
		// Diff failure is non-fatal — we've already synced. Log loud
		// and continue so the next poll cycle has a clean state.
		h.logger.Warn("could not compute module diff", "err", err)
		return nil
	}
	if len(changed) == 0 {
		h.logger.Info("no module versions changed in this batch")
		return nil
	}

	if h.canopyStore == nil {
		// Sync-only mode — log the diff but skip re-ingest.
		for _, mv := range changed {
			h.logger.Info("module changed (sync-only mode; no re-ingest)",
				"module", mv.module, "version", mv.version)
		}
		return nil
	}

	// Re-ingest each (module, version) pair. Per-pair failures are
	// non-fatal: log loudly and continue. Returning an error from
	// the handler would replay the entire batch on the next poll,
	// which can wedge progress when a single module is permanently
	// broken. The at-least-once contract is upheld at the OnCommit
	// boundary — within a successful OnCommit, partial failure is
	// the operator's signal to investigate, not a retry trigger.
	var failed int
	for _, mv := range changed {
		if _, err := ingest.FromMirroredVersion(ctx, h.canopyStore, h.worktree, mv.module, mv.version); err != nil {
			h.logger.Warn("re-ingest failed",
				"module", mv.module, "version", mv.version, "err", err)
			failed++
			continue
		}
		h.logger.Info("re-ingested",
			"module", mv.module, "version", mv.version)
	}
	if failed > 0 {
		h.logger.Warn("batch re-ingest had failures",
			"total", len(changed), "failed", failed)
	}
	return nil
}

type moduleVersion struct {
	module, version string
}

// changedModuleVersions returns the unique (module, version) pairs
// touched between two SHAs. Empty oldHead → list everything currently
// under modules/.
func (h *syncHandler) changedModuleVersions(ctx context.Context, oldHead, newHead string) ([]moduleVersion, error) {
	var paths []string
	if oldHead == "" || oldHead == newHead {
		// Fall back to ls-tree at the new HEAD scoped to modules/.
		out, err := h.gitOutput(ctx, "ls-tree", "-r", "--name-only", newHead, "modules/")
		if err != nil {
			return nil, fmt.Errorf("ls-tree: %w", err)
		}
		paths = strings.Split(strings.TrimSpace(out), "\n")
	} else {
		out, err := h.gitOutput(ctx, "diff", "--name-only", oldHead+".."+newHead, "--", "modules/")
		if err != nil {
			return nil, fmt.Errorf("diff: %w", err)
		}
		paths = strings.Split(strings.TrimSpace(out), "\n")
	}

	seen := map[moduleVersion]struct{}{}
	out := make([]moduleVersion, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// modules/<name>/<version>/<file>
		parts := strings.SplitN(p, "/", 4)
		if len(parts) < 3 || parts[0] != "modules" {
			continue
		}
		mv := moduleVersion{module: parts[1], version: parts[2]}
		if _, dup := seen[mv]; dup {
			continue
		}
		seen[mv] = struct{}{}
		out = append(out, mv)
	}
	return out, nil
}

// gitRun runs `git <args...>` in the worktree, suppressing stdout.
func (h *syncHandler) gitRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = h.worktree
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w (%s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// gitOutput runs `git <args...>` and returns stdout.
func (h *syncHandler) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = h.worktree
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
