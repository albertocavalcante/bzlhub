// Package preflight runs the procurement state-machine's automated
// pre-decision phase. Pending requests are picked up by a worker
// pool, run through a Checker, and transitioned to auto_pass,
// needs_review, or denied based on the Verdict.
//
// Concurrency contract:
//   - Workers run in parallel (default 2, env BZLHUB_PREFLIGHT_WORKERS).
//   - Duplicate processing is prevented by the SQL CAS in
//     store.TransitionRequest — the worker that wins the
//     pending → preflighting transition owns the request.
//   - Graceful shutdown: ctx.Done blocks new pulls; in-flight
//     Check() calls finish; workers return.
//
// The Checker interface is the seam for real-check implementations
// (HTTP fetch, license detection, hermeticity classification).

package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// PreflightStore is the slice of *store.Store the Runner touches.
// Interface keeps tests independent of SQLite.
type PreflightStore interface {
	ListRequests(ctx context.Context, q store.RequestQuery) ([]*store.Request, error)
	TransitionRequest(ctx context.Context, id int64, from, to store.RequestState, fields *store.RequestFields) error
	RecordAudit(ctx context.Context, ev store.AuditEvent) error
}

// Options configures a Runner. Store + Checker are required;
// Workers, PollEvery, and Log have sensible defaults.
type Options struct {
	Store     PreflightStore
	Checker   Checker
	Workers   int
	PollEvery time.Duration
	Log       *slog.Logger
}

const (
	defaultWorkers   = 2
	defaultPollEvery = 5 * time.Second
)

// Runner polls for pending requests and dispatches them through
// the Checker.
type Runner struct {
	store     PreflightStore
	checker   Checker
	workers   int
	pollEvery time.Duration
	log       *slog.Logger
}

// New constructs a Runner. Panics if Store or Checker is nil — those
// are misconfigurations, not runtime conditions.
func New(opts Options) *Runner {
	if opts.Store == nil {
		panic("preflight.New: Store is required")
	}
	if opts.Checker == nil {
		panic("preflight.New: Checker is required")
	}
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = defaultPollEvery
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Runner{
		store:     opts.Store,
		checker:   opts.Checker,
		workers:   opts.Workers,
		pollEvery: opts.PollEvery,
		log:       opts.Log,
	}
}

// Run spawns the worker pool and blocks until ctx is cancelled.
// Workers stagger their poll cycles so the SQL query load is even
// across the configured PollEvery interval instead of bursting
// every poll.
func (r *Runner) Run(ctx context.Context) {
	r.log.Info("preflight runner starting",
		"workers", r.workers,
		"poll_every", r.pollEvery)

	var wg sync.WaitGroup
	for i := range r.workers {
		offset := time.Duration(i) * (r.pollEvery / time.Duration(r.workers))
		wg.Go(func() {
			r.workerLoop(ctx, offset)
		})
	}
	wg.Wait()
	r.log.Info("preflight runner stopped")
}

// workerLoop is one goroutine's main loop. The initial sleep
// staggers workers across the poll interval.
func (r *Runner) workerLoop(ctx context.Context, initialOffset time.Duration) {
	if initialOffset > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialOffset):
		}
	}
	tick := time.NewTicker(r.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Process up to one request per tick. If there's more
			// work, the next worker / next tick picks it up — fair
			// share without locking.
			r.tryProcessOne(ctx)
		}
	}
}

// tryProcessOne polls for a pending request, attempts the CAS
// transition to preflighting, and on success runs the check and
// applies the verdict. Returns silently when there's nothing to do
// or the CAS lost to another worker.
func (r *Runner) tryProcessOne(ctx context.Context) {
	pending, err := r.store.ListRequests(ctx, store.RequestQuery{
		States: []store.RequestState{store.RequestStatePending},
		Limit:  r.workers * 2, // small batch — workers race CAS over it
	})
	if err != nil {
		if ctx.Err() != nil {
			return // graceful shutdown, not an error to surface
		}
		r.log.Warn("preflight: list pending failed", "err", err)
		return
	}
	for _, req := range pending {
		if ctx.Err() != nil {
			return
		}
		// CAS: only one worker wins. Losers see ErrStateMismatch and
		// move on to the next candidate.
		if err := r.store.TransitionRequest(ctx, req.ID,
			store.RequestStatePending, store.RequestStatePreflighting, nil); err != nil {
			if errors.Is(err, store.ErrStateMismatch) {
				continue
			}
			r.log.Warn("preflight: pending→preflighting transition failed",
				"err", err, "id", req.ID)
			continue
		}
		// We own this request now.
		r.processOwned(ctx, *req)
		return // one per tick is fair
	}
}

// processOwned runs the checker against req and applies the verdict.
// Called with the request already in state=preflighting.
func (r *Runner) processOwned(ctx context.Context, req store.Request) {
	verdict := r.checker.Check(ctx, req)
	if !validNextStates[verdict.NextState] {
		r.log.Error("preflight: checker returned illegal next state",
			"id", req.ID, "module", req.Module, "version", req.Version,
			"next_state", verdict.NextState)
		// Don't try to transition — leave the request in preflighting
		// for ops to investigate. A broken checker shouldn't corrupt
		// the audit story.
		return
	}

	payload, _ := json.Marshal(verdict)
	fields := &store.RequestFields{
		PreflightJSON: payload,
	}
	if verdict.NextState == store.RequestStateDenied {
		fields.DenialReason = verdict.Reason
	}

	if err := r.store.TransitionRequest(ctx, req.ID,
		store.RequestStatePreflighting, verdict.NextState, fields); err != nil {
		r.log.Error("preflight: verdict transition failed",
			"err", err, "id", req.ID, "next_state", verdict.NextState)
		return
	}

	// Audit row for the verdict. Best-effort — losing it doesn't
	// undo the transition.
	auditKind := "preflight_" + string(verdict.NextState)
	if err := r.store.RecordAudit(ctx, store.AuditEvent{
		Kind:    auditKind,
		Source:  "preflight",
		Module:  req.Module,
		Version: req.Version,
		OK:      verdict.NextState != store.RequestStateDenied,
		Error:   verdict.Reason,
		Payload: payload,
		UserID:  "", // preflight is system-driven; no user identity
	}); err != nil {
		r.log.Warn("preflight: audit write failed (transition still committed)",
			"err", err, "id", req.ID)
	}

	r.log.Info("preflight verdict applied",
		"id", req.ID,
		"module", req.Module, "version", req.Version,
		"next_state", verdict.NextState,
		"reason", verdict.Reason)
}
