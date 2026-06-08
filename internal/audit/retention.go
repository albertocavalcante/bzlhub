// Package audit drives the audit-events retention sweep — a tiny
// background goroutine that periodically prunes audit_events rows
// older than policy.audit.retain_days. Lifts the retain_days config
// from "stored but inert" to actually-enforced.
package audit

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// RetentionSource is the slice of *store.Store the daemon touches.
// Interface keeps tests independent of SQLite.
type RetentionSource interface {
	PruneAudit(ctx context.Context, olderThan time.Duration) (int, error)
}

// RetentionOptions configures a RetentionDaemon.
type RetentionOptions struct {
	// RetainDays is the maximum age (in days) a row stays in
	// audit_events. 0 disables the daemon — Run returns immediately.
	RetainDays int

	// Interval is the sweep cadence. 0 → 1h default. Smaller values
	// are fine for tests; production rarely needs faster than hourly.
	Interval time.Duration

	Log *slog.Logger
}

const defaultRetentionInterval = time.Hour

// RetentionDaemon sweeps audit_events on Interval.
type RetentionDaemon struct {
	source     RetentionSource
	retainDays int
	interval   time.Duration
	log        *slog.Logger

	// sweeps counts completed PruneAudit calls (success or
	// failure). Exported via the test seam for assertion; not part
	// of the production observability surface.
	sweeps atomic.Int64
}

// NewRetentionDaemon constructs a daemon. Panics on nil source —
// misconfiguration, not a runtime condition.
func NewRetentionDaemon(source RetentionSource, opts RetentionOptions) *RetentionDaemon {
	if source == nil {
		panic("audit.NewRetentionDaemon: source is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultRetentionInterval
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &RetentionDaemon{
		source:     source,
		retainDays: opts.RetainDays,
		interval:   opts.Interval,
		log:        opts.Log,
	}
}

// Run starts the sweep loop and blocks until ctx is cancelled.
// When RetainDays ≤ 0 returns immediately (caller can wire the
// daemon unconditionally; the daemon decides whether to do work).
func (d *RetentionDaemon) Run(ctx context.Context) {
	if d.retainDays <= 0 {
		d.log.Info("audit retention disabled (retain_days=0)")
		return
	}
	d.log.Info("audit retention daemon starting",
		"retain_days", d.retainDays, "interval", d.interval)

	tick := time.NewTicker(d.interval)
	defer tick.Stop()
	// Sweep on start too — otherwise a freshly-restarted canopy
	// waits a full interval before doing the first prune even when
	// there's a backlog.
	d.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			d.log.Info("audit retention daemon stopped")
			return
		case <-tick.C:
			d.sweep(ctx)
		}
	}
}

func (d *RetentionDaemon) sweep(ctx context.Context) {
	defer d.sweeps.Add(1)
	older := time.Duration(d.retainDays) * 24 * time.Hour
	n, err := d.source.PruneAudit(ctx, older)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("audit retention sweep failed", "err", err)
		return
	}
	if n > 0 {
		d.log.Info("audit retention pruned rows",
			"deleted", n, "older_than_days", d.retainDays)
	}
}
