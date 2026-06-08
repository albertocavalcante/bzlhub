package bzlhub

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// SyncRunLoop runs SyncRun on entry and every `interval` thereafter
// until ctx is cancelled. Suitable for `bzlhub sync run --interval`
// in operators who'd rather not wire systemd / cron themselves.
//
// onIteration, when non-nil, fires after each iteration with the
// receipt and any error. The loop survives per-iteration errors —
// only ctx cancellation stops it. Errors are slog.Warn'd; receipts
// for failed iterations are zero-valued except for whatever fields
// SyncRun populated before failing.
//
// interval <= 0 is an explicit error — a zero interval would hot-
// spin and we'd rather refuse than burn an audit row per
// microsecond.
func (s *Service) SyncRunLoop(ctx context.Context, opts SyncRunOptions, interval time.Duration, onIteration func(SyncRunReceipt, error)) error {
	if interval <= 0 {
		return errors.New("bzlhub.SyncRunLoop: interval must be > 0")
	}

	// Initial iteration fires immediately — operators don't want
	// to wait `interval` for the first sync.
	s.runOnce(ctx, opts, onIteration)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			s.runOnce(ctx, opts, onIteration)
		}
	}
}

// runOnce wraps one SyncRun call with error logging and the
// optional callback. Per-iteration errors don't propagate out of
// SyncRunLoop; they're surfaced via slog + callback.
func (s *Service) runOnce(ctx context.Context, opts SyncRunOptions, onIteration func(SyncRunReceipt, error)) {
	rec, err := s.SyncRun(ctx, opts)
	if err != nil {
		// ctx cancellation isn't "an error" the loop should warn
		// about — it's the normal shutdown path.
		if !errors.Is(err, context.Canceled) {
			slog.Warn("sync_run iteration failed", "err", err)
		}
	}
	if onIteration != nil {
		onIteration(rec, err)
	}
}
