// Package forgewatch is the read-side counterpart to internal/publish.
// It polls a bigorna.Forge for new commits on a branch and dispatches
// them to a user-supplied OnCommit callback — the callback's job is
// the actual re-index work (canopy's wiring calls git pull + ingest).
//
// The package is forge-agnostic by design: it does not touch local
// git, the SQLite store, or any canopy-specific state. Only the
// callback knows about canopy. This keeps the watcher reusable and
// the boundary between "API polling" and "registry sync" sharp.
//
// Semantics
//
//   - At-least-once: state advances only after OnCommit returns nil.
//     If the callback fails, the next poll re-discovers the same
//     commits. Callbacks must be idempotent (canopy's re-ingest is).
//
//   - Backpressure: the next poll timer starts AFTER OnCommit returns.
//     A slow callback delays polling; we never queue overlapping work.
//
//   - Adaptive backoff: on consecutive notModified responses, the
//     poll interval grows up to MaxInterval (default 5× base). On any
//     activity (new commits or callback success after activity), it
//     resets to the base interval.
//
//   - Clock injection: tests run in milliseconds via bigorna.Clock
//     with bigornatest.ManualClock.
package forgewatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/albertocavalcante/bigorna"
)

// State persists across polls so a restart picks up where it left off.
// Empty State means "cold start": ListNewCommits is invoked with empty
// sinceSHA / etag, and the response defines the new baseline.
type State struct {
	// LastSHA is the most recent commit successfully processed by
	// OnCommit. Subsequent polls pass this as sinceSHA so the forge
	// returns only commits newer than it.
	LastSHA string `json:"last_sha"`

	// LastETag is the forge-supplied ETag from the previous successful
	// GET. GitHub uses it for true 304-style conditional polling;
	// Bitbucket DC echoes it back unchanged. Treat as opaque.
	LastETag string `json:"last_etag"`

	// LastPolledAt is the last time the poll loop completed a cycle
	// (regardless of whether new commits were found). Surfaced for
	// observability / debugging.
	LastPolledAt time.Time `json:"last_polled_at,omitzero"`
}

// Store persists Watcher state. Implementations must be safe for
// concurrent calls from a single Watcher (Load → Save sequencing is
// enforced by the watcher's poll loop; no parallel writes).
type Store interface {
	Load(ctx context.Context, repo bigorna.Repo, branch string) (State, error)
	Save(ctx context.Context, repo bigorna.Repo, branch string, s State) error
}

// OnCommitFunc receives the new commits on every successful poll that
// surfaced fresh activity. Commits are forge-native order (newest-
// first on both GitHub and Bitbucket DC). Returning a non-nil error
// keeps the Watcher's state pinned to the previous LastSHA — the same
// commits will be redelivered on the next poll. Idempotent callbacks
// are required.
//
// The context is the Watcher's Run ctx; honor cancellation if the
// callback is long-running.
type OnCommitFunc func(ctx context.Context, commits []bigorna.Commit) error

// Config configures a Watcher.
type Config struct {
	// Forge is the forge API client. Required.
	Forge bigorna.Forge

	// Repo is the registry repository on the forge. Required.
	Repo bigorna.Repo

	// Branch is the branch to watch (default "main"). New commits on
	// this branch trigger OnCommit.
	Branch string

	// Store persists watcher state across restarts. Required.
	Store Store

	// OnCommit receives new commits. Required.
	OnCommit OnCommitFunc

	// Interval is the base poll interval. Default 60s.
	Interval time.Duration

	// MaxInterval is the upper bound on the adaptive backoff. Default
	// 5 × Interval. The watcher grows the interval geometrically on
	// consecutive notModified responses and resets on activity.
	MaxInterval time.Duration

	// Jitter is the ± fractional perturbation applied to each sleep
	// (0..1; e.g. 0.1 = ±10%). Default 0 (no jitter) so ManualClock
	// tests stay deterministic. Production callers set this to
	// ~0.1 to break the thundering-herd pattern when many canopy
	// instances boot together and would otherwise poll a shared
	// forge in lockstep.
	Jitter float64

	// Clock is injected for testability (use bigornatest.ManualClock
	// in tests). Default bigorna.RealClock{}.
	Clock bigorna.Clock

	// Logger receives structured warnings. Default slog.Default().
	Logger *slog.Logger
}

// Watcher is the poll-loop state. Construct via New, drive via Run.
type Watcher struct {
	cfg Config

	// interval is the current adaptive interval (grows on notModified,
	// resets on activity). Always in [cfg.Interval, cfg.MaxInterval].
	interval time.Duration
}

// New constructs a Watcher.
func New(cfg Config) (*Watcher, error) {
	if cfg.Forge == nil {
		return nil, errors.New("forgewatch: Forge is required")
	}
	if cfg.Repo.Owner == "" || cfg.Repo.Name == "" {
		return nil, errors.New("forgewatch: Repo is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("forgewatch: Store is required")
	}
	if cfg.OnCommit == nil {
		return nil, errors.New("forgewatch: OnCommit is required")
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.Interval == 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.MaxInterval == 0 {
		cfg.MaxInterval = 5 * cfg.Interval
	}
	if cfg.MaxInterval < cfg.Interval {
		// A misconfigured MaxInterval below the base would force the
		// watcher into a degenerate "always sleep the maximum" loop.
		// Pin to Interval and log; the alternative (return an error)
		// is needlessly destructive — Interval is the sane fallback.
		cfg.MaxInterval = cfg.Interval
	}
	if cfg.Clock == nil {
		cfg.Clock = bigorna.RealClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Watcher{cfg: cfg, interval: cfg.Interval}, nil
}

// Run polls until ctx is canceled. Each poll cycle:
//
//  1. Load State from the Store.
//  2. Call Forge.ListNewCommits with the persisted sinceSHA + etag.
//  3. If commits arrived, call OnCommit. On success, advance State.
//     On failure, log + retry next cycle without advancing.
//  4. Sleep for the current adaptive interval.
//
// The first iteration polls immediately (no initial sleep) so a
// freshly-started watcher reflects current state quickly.
func (w *Watcher) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := w.pollOnce(ctx); err != nil {
			// Transport errors are non-fatal — log and back off so a
			// transient forge outage doesn't kill the daemon.
			w.cfg.Logger.Warn("forgewatch: poll failed; will retry",
				"repo", w.cfg.Repo, "branch", w.cfg.Branch, "err", err)
		}

		if err := w.cfg.Clock.Sleep(ctx, w.jitteredInterval()); err != nil {
			return err
		}
	}
}

// pollOnce executes a single poll cycle. Returns nil on a clean poll
// (whether or not new commits arrived). Returns a non-nil error only
// for transport / store failures that should be logged at the caller.
func (w *Watcher) pollOnce(ctx context.Context) error {
	state, err := w.cfg.Store.Load(ctx, w.cfg.Repo, w.cfg.Branch)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	commits, newETag, notModified, err := w.cfg.Forge.ListNewCommits(
		ctx, w.cfg.Repo, w.cfg.Branch, state.LastSHA, state.LastETag)
	if err != nil {
		return fmt.Errorf("ListNewCommits: %w", err)
	}

	now := w.cfg.Clock.Now()

	if notModified {
		// Nothing new. Backoff for the next cycle. Save state to
		// update LastPolledAt and preserve the (potentially refreshed)
		// etag, but leave LastSHA untouched.
		w.applyBackoff()
		state.LastETag = newETag
		state.LastPolledAt = now
		if err := w.cfg.Store.Save(ctx, w.cfg.Repo, w.cfg.Branch, state); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		return nil
	}

	if len(commits) == 0 {
		// Defensive: forge reported "modified" but returned zero
		// commits. Treat as a soft no-op — advance LastPolledAt and
		// keep the previous LastSHA. Don't fire OnCommit with nothing.
		w.applyBackoff()
		state.LastETag = newETag
		state.LastPolledAt = now
		return w.cfg.Store.Save(ctx, w.cfg.Repo, w.cfg.Branch, state)
	}

	// New commits arrived. Reset the adaptive interval so the next
	// poll happens at base cadence — activity often clusters.
	w.interval = w.cfg.Interval

	// Dispatch to callback. State only advances on callback success
	// (at-least-once semantics).
	if err := w.cfg.OnCommit(ctx, commits); err != nil {
		w.cfg.Logger.Warn("forgewatch: OnCommit failed; state not advanced",
			"repo", w.cfg.Repo, "branch", w.cfg.Branch,
			"commit_count", len(commits), "err", err)
		// Persistent callback failure (wedged store, bad worktree)
		// would otherwise re-deliver the same commits at base cadence
		// forever, log-spamming the operator. Apply geometric backoff
		// so the next attempt waits — same treatment as the
		// notModified + zero-commits paths above. A subsequent
		// successful poll resets w.interval at line ~231.
		w.applyBackoff()
		// Still touch LastPolledAt so observers can tell the loop
		// is alive even when the callback is broken.
		state.LastPolledAt = now
		_ = w.cfg.Store.Save(ctx, w.cfg.Repo, w.cfg.Branch, state)
		return nil
	}

	// Forge.ListNewCommits returns commits newest-first; the freshly
	// processed top-of-branch SHA is commits[0].
	state.LastSHA = commits[0].SHA
	state.LastETag = newETag
	state.LastPolledAt = now
	if err := w.cfg.Store.Save(ctx, w.cfg.Repo, w.cfg.Branch, state); err != nil {
		// State advance failed AFTER callback succeeded. This is the
		// at-least-once corner: we'll re-deliver the same commits on
		// the next poll. The callback being idempotent makes this
		// safe; log loudly so the operator can investigate the store.
		return fmt.Errorf("save state after OnCommit succeeded (commits will replay): %w", err)
	}
	return nil
}

// jitteredInterval returns the current adaptive interval optionally
// perturbed by ± cfg.Jitter (0..1). Zero Jitter is a no-op pass-through
// so tests using ManualClock advance by exactly w.interval and stay
// deterministic.
func (w *Watcher) jitteredInterval() time.Duration {
	if w.cfg.Jitter <= 0 {
		return w.interval
	}
	// rand/v2 is auto-seeded — no global state to manage.
	delta := (rand.Float64()*2 - 1) * w.cfg.Jitter // in [-Jitter, +Jitter]
	d := time.Duration(float64(w.interval) * (1 + delta))
	if d <= 0 {
		return w.interval
	}
	return d
}

// applyBackoff doubles the interval up to MaxInterval. Called after
// every notModified / empty-commits cycle.
func (w *Watcher) applyBackoff() {
	w.interval = min(w.interval*2, w.cfg.MaxInterval)
}
