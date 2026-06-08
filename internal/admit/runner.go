package admit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/publish"
	"github.com/albertocavalcante/bzlhub/internal/purge"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// ErrTransient marks an admit pipeline error that may resolve on a
// later attempt (network blip, upstream 5xx, rate-limit). The runner
// wraps these with retry-with-backoff up to Options.MaxRetries;
// terminal errors (validation, integrity mismatch, malformed source)
// flow through unwrapped and deny immediately.
//
// fetcher.go classifies network + 5xx responses as transient. Other
// admit-pipeline error sources (publisher, extractor) may wrap their
// own transient flavours by joining this sentinel via fmt.Errorf
// "%w: %w".
var ErrTransient = errors.New("admit: transient failure")

// IsTransient reports whether err (or anything it wraps) is an
// ErrTransient. Exposed for callers that want to classify errors
// without depending on the sentinel directly.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }

// AdmitStore is the slice of *store.Store the Runner touches.
// Interface keeps tests independent of SQLite.
type AdmitStore interface {
	ListRequests(ctx context.Context, q store.RequestQuery) ([]*store.Request, error)
	TransitionRequest(ctx context.Context, id int64, from, to store.RequestState, fields *store.RequestFields) error
	RecordAudit(ctx context.Context, ev store.AuditEvent) error
	ReclaimStuckFetching(ctx context.Context, before time.Time) (int, error)
}

// Options configures a Runner. Store + Publisher + Fetcher are
// required; the rest have sensible defaults.
type Options struct {
	Store     AdmitStore
	Publisher publish.Publisher
	Fetcher   Fetcher
	BotIdent  publish.Identity
	Workers   int           // default 1 (publishers self-serialize on the worktree)
	PollEvery time.Duration // default 5s
	Log       *slog.Logger

	// ExtractMaxBytes caps the size of the extracted source tree
	// the pipeline inflates from the blob. 0 = 1 GiB default.
	ExtractMaxBytes int64

	// MaxRetries bounds the number of retry attempts after a
	// transient pipeline error (see ErrTransient). 0 selects the
	// default of 2 (so 3 total attempts: initial + 2 retries).
	// Set to -1 to disable retry entirely (every failure denies
	// on the first attempt, matching pre-η behaviour).
	MaxRetries int

	// RetryBackoff returns how long to sleep before retry attempt
	// number `attempt` (1-based — attempt=1 is the first retry).
	// Default is exponential 5s * 5^(attempt-1) capped at 5m.
	// Tests inject a zero-delay function.
	RetryBackoff func(attempt int) time.Duration

	// Purger receives CDN invalidation calls after each admit
	// success. nil → purge.NoOp{} (the no-CDN default). Purge
	// failures are logged but never fail the admit pipeline —
	// the module is still indexed; the edge is just stale until
	// the next TTL expires.
	Purger purge.Provider

	// SweepStaleness bounds the maximum lifetime of a `fetching`
	// row before the boot sweep reclaims it back to `approved`.
	// 0 → defaultSweepStaleness (5 minutes — comfortably above
	// the 5-minute backoff cap so an in-flight retry isn't
	// reclaimed out from under itself). Set to a very small value
	// only in tests where you want every row swept.
	SweepStaleness time.Duration

	// CDNBaseURL is the canopy origin reachable through the CDN
	// (e.g., "https://bcr.bzlhub.com"). When empty, no URLs are
	// computed and Purger.Purge is not called even if a non-NoOp
	// Purger is wired — operator opted into a purger but didn't
	// configure the public origin, treat as misconfigured-no-op.
	CDNBaseURL string
}

const (
	defaultAdmitWorkers   = 1
	defaultAdmitPollEvery = 5 * time.Second
	defaultMaxRetries     = 2
	// defaultSweepStaleness is the boot-sweep cutoff for stuck
	// fetching rows. Sits above the 5-minute backoff cap so a
	// genuinely retrying worker isn't reclaimed mid-attempt.
	defaultSweepStaleness = 5 * time.Minute
)

// defaultRetryBackoff is the exponential schedule used when
// Options.RetryBackoff is nil: 5s, 25s, 125s, ... capped at 5m.
func defaultRetryBackoff(attempt int) time.Duration {
	const base = 5 * time.Second
	const cap = 5 * time.Minute
	if attempt <= 0 {
		return 0
	}
	d := base
	for range attempt - 1 {
		d *= 5
		if d >= cap {
			return cap
		}
	}
	return d
}

// Runner consumes auto_pass + approved requests, materializes them
// via the publisher, and advances them to indexed or denied.
type Runner struct {
	store          AdmitStore
	deps           pipelineDeps
	workers        int
	pollEvery      time.Duration
	maxRetries     int
	retryBackoff   func(attempt int) time.Duration
	purger         purge.Provider
	cdnBaseURL     string
	sweepStaleness time.Duration
	log            *slog.Logger
}

// New constructs a Runner. Panics on missing required deps —
// misconfiguration, not a runtime condition.
func New(opts Options) *Runner {
	if opts.Store == nil {
		panic("admit.New: Store is required")
	}
	if opts.Publisher == nil {
		panic("admit.New: Publisher is required")
	}
	if opts.Fetcher == nil {
		panic("admit.New: Fetcher is required")
	}
	if opts.Workers <= 0 {
		opts.Workers = defaultAdmitWorkers
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = defaultAdmitPollEvery
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	backoff := opts.RetryBackoff
	if backoff == nil {
		backoff = defaultRetryBackoff
	}
	purger := opts.Purger
	if purger == nil {
		purger = purge.NoOp{}
	}
	sweepStaleness := opts.SweepStaleness
	if sweepStaleness == 0 {
		sweepStaleness = defaultSweepStaleness
	}
	return &Runner{
		store: opts.Store,
		deps: pipelineDeps{
			fetcher:         opts.Fetcher,
			publisher:       opts.Publisher,
			bot:             opts.BotIdent,
			extractMaxBytes: opts.ExtractMaxBytes,
		},
		workers:        opts.Workers,
		pollEvery:      opts.PollEvery,
		maxRetries:     maxRetries,
		retryBackoff:   backoff,
		purger:         purger,
		cdnBaseURL:     opts.CDNBaseURL,
		sweepStaleness: sweepStaleness,
		log:            opts.Log,
	}
}

// Run spawns the worker pool and blocks until ctx is cancelled.
// Workers stagger their poll cycles across the interval.
//
// Boot sequence: reclaim any `fetching` rows left over from a prior
// process death (Plan 76 §2.3) BEFORE starting workers, so worker 0
// doesn't race the sweep on its first poll cycle.
func (r *Runner) Run(ctx context.Context) {
	r.log.Info("admit runner starting",
		"workers", r.workers, "poll_every", r.pollEvery,
		"sweep_staleness", r.sweepStaleness)

	r.sweepStuckFetching(ctx)

	var wg sync.WaitGroup
	for i := range r.workers {
		offset := time.Duration(i) * (r.pollEvery / time.Duration(r.workers))
		wg.Go(func() {
			r.workerLoop(ctx, offset)
		})
	}
	wg.Wait()
	r.log.Info("admit runner stopped")
}

// sweepStuckFetching reclaims any requests stuck in `fetching` past
// r.sweepStaleness back to `approved` so the worker loop picks them
// back up. Called once at Run() before workers start.
//
// Failures here are logged but do NOT prevent the runner from
// starting — a partial reclaim is better than refusing service.
// The next boot will retry the sweep.
func (r *Runner) sweepStuckFetching(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	before := time.Now().Add(-r.sweepStaleness)
	n, err := r.store.ReclaimStuckFetching(ctx, before)
	if err != nil {
		r.log.Warn("admit: boot sweep failed (continuing)",
			"err", err, "staleness", r.sweepStaleness)
		return
	}
	if n > 0 {
		r.log.Info("admit: boot sweep reclaimed stuck fetching rows",
			"count", n, "staleness", r.sweepStaleness)
	}
}

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
			r.tryProcessOne(ctx)
		}
	}
}

// tryProcessOne polls for one auto_pass or approved request, CAS-
// transitions it to fetching, and runs the admit pipeline.
func (r *Runner) tryProcessOne(ctx context.Context) {
	candidates, err := r.store.ListRequests(ctx, store.RequestQuery{
		States: []store.RequestState{store.RequestStateAutoPass, store.RequestStateApproved},
		Limit:  r.workers * 2,
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.log.Warn("admit: list candidates failed", "err", err)
		return
	}
	for _, req := range candidates {
		if ctx.Err() != nil {
			return
		}
		if err := r.store.TransitionRequest(ctx, req.ID,
			req.State, store.RequestStateFetching, nil); err != nil {
			if errors.Is(err, store.ErrStateMismatch) {
				continue
			}
			r.log.Warn("admit: lock-to-fetching failed",
				"err", err, "id", req.ID)
			continue
		}
		r.processOwned(ctx, *req)
		return
	}
}

// processOwned runs the pipeline against req (already in
// state=fetching) and applies the outcome.
//
// On a transient error (errors.Is(err, ErrTransient)) the runner
// sleeps per retryBackoff and re-invokes admitOne up to maxRetries
// times. Terminal errors deny immediately. ctx cancellation during
// backoff returns without transitioning the row — the request stays
// in `fetching` and the next canopy boot's sweepStuckFetching
// reclaims it (Plan 76 §2.3, resolved 2026-06-08).
func (r *Runner) processOwned(ctx context.Context, req store.Request) {
	var lastErr error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := r.retryBackoff(attempt)
			r.log.Info("admit: transient failure, will retry",
				"id", req.ID,
				"module", req.Module, "version", req.Version,
				"attempt", attempt, "max", r.maxRetries,
				"backoff", delay, "err", lastErr)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
		res, err := admitOne(ctx, r.deps, req)
		if err == nil {
			r.succeed(ctx, req, res, attempt)
			return
		}
		if !IsTransient(err) || attempt >= r.maxRetries {
			r.fail(ctx, req, err, attempt)
			return
		}
		lastErr = err
	}
}

func (r *Runner) succeed(ctx context.Context, req store.Request, res admitResult, retries int) {
	fields := &store.RequestFields{
		FetchedSHA:   res.Integrity,
		CommittedSHA: res.CommitSHA,
	}
	if retries > 0 {
		fields.RetryCount = &retries
	}
	if err := r.store.TransitionRequest(ctx, req.ID,
		store.RequestStateFetching, store.RequestStateIndexed, fields); err != nil {
		r.log.Error("admit: fetching→indexed transition failed",
			"err", err, "id", req.ID)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"request_id":    req.ID,
		"integrity":     res.Integrity,
		"committed_sha": res.CommitSHA,
		"bytes":         res.Bytes,
		"retries":       retries,
	})
	r.audit(ctx, store.AuditEvent{
		Kind:    "admit_success",
		Source:  "admit",
		Module:  req.Module,
		Version: req.Version,
		OK:      true,
		Payload: payload,
	})
	r.log.Info("admit: indexed",
		"id", req.ID,
		"module", req.Module, "version", req.Version,
		"commit", res.CommitSHA,
		"bytes", res.Bytes,
		"retries", retries)
	r.purgeAfterIndex(ctx, req)
}

// purgeAfterIndex invalidates the CDN URLs whose representations
// shifted because of this admit success. Failures are non-fatal —
// the module is indexed; the edge is just stale until TTL.
//
// No-op when the runner has the NoOp purger (default) or when
// CDNBaseURL is empty.
func (r *Runner) purgeAfterIndex(ctx context.Context, req store.Request) {
	if r.purger == nil || r.purger.Name() == "noop" || r.cdnBaseURL == "" {
		return
	}
	urls := purge.URLsForModule(r.cdnBaseURL, req.Module)
	if len(urls) == 0 {
		return
	}
	err := r.purger.Purge(ctx, urls)
	payload, _ := json.Marshal(map[string]any{
		"request_id": req.ID,
		"vendor":     r.purger.Name(),
		"urls":       urls,
		"ok":         err == nil,
		"err":        errString(err),
	})
	r.audit(ctx, store.AuditEvent{
		Kind:    "cdn_purge",
		Source:  "admit",
		Module:  req.Module,
		Version: req.Version,
		OK:      err == nil,
		Error:   errString(err),
		Payload: payload,
	})
	if err != nil {
		r.log.Warn("cdn purge failed (module still indexed)",
			"vendor", r.purger.Name(),
			"id", req.ID,
			"module", req.Module, "version", req.Version,
			"err", err)
	}
}

// errString returns "" for a nil error and the string form
// otherwise. Used so audit payloads serialize a clean empty
// string instead of "<nil>".
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (r *Runner) fail(ctx context.Context, req store.Request, cause error, retries int) {
	fields := &store.RequestFields{DenialReason: cause.Error()}
	if retries > 0 {
		fields.RetryCount = &retries
	}
	if err := r.store.TransitionRequest(ctx, req.ID,
		store.RequestStateFetching, store.RequestStateDenied, fields); err != nil {
		r.log.Error("admit: fetching→denied transition failed",
			"err", err, "id", req.ID)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"request_id": req.ID,
		"error":      cause.Error(),
		"retries":    retries,
		"transient":  IsTransient(cause),
	})
	r.audit(ctx, store.AuditEvent{
		Kind:    "admit_failure",
		Source:  "admit",
		Module:  req.Module,
		Version: req.Version,
		OK:      false,
		Error:   cause.Error(),
		Payload: payload,
	})
	r.log.Warn("admit: denied at fetch/publish",
		"id", req.ID,
		"module", req.Module, "version", req.Version,
		"retries", retries,
		"err", cause)
}

func (r *Runner) audit(ctx context.Context, ev store.AuditEvent) {
	if err := r.store.RecordAudit(ctx, ev); err != nil {
		r.log.Warn("admit: audit write failed (transition still committed)",
			"err", err, "kind", ev.Kind)
	}
}
