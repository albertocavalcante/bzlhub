package bzlhub

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
)

// SyncRunOptions controls a single sync-run invocation.
type SyncRunOptions struct {
	// Force permits a non-fast-forward pull — the Mirror is hard-
	// reset to the remote tip even if local and remote diverged.
	Force bool

	// SkipRefresh suppresses the post-Sync drift recompute. Used
	// when the operator wants to inspect upstream changes before
	// canopy rewrites drift verdicts in their index.
	SkipRefresh bool
}

// SyncRunReceipt summarises one sync-run for the audit log + CLI.
type SyncRunReceipt struct {
	FromSHA            string
	ToSHA              string
	Commits            int
	Bytes              int64
	Duration           time.Duration
	UpToDate           bool
	DriftRowsRewritten int
}

// SyncRun fetches updates from the Mirror's configured upstream and,
// when something advanced, recomputes drift across the index.
// Returns ErrNoMirrorForDrift when no Mirror is wired. Up-to-date
// probes skip the refresh; any Sync failure (including
// ErrNotFastForward without Force) returns the wrapped error after
// writing an audit row.
func (s *Service) SyncRun(ctx context.Context, opts SyncRunOptions) (SyncRunReceipt, error) {
	var rec SyncRunReceipt
	if s.mirror == nil {
		return rec, ErrNoMirrorForDrift
	}

	start := time.Now()
	sr, err := s.mirror.Sync(ctx, bcrmirror.SyncOptions{Force: opts.Force})
	if err != nil {
		s.recordSyncRunAudit(ctx, "sync_run_failure", start, sr, 0, err, nil)
		return rec, fmt.Errorf("bzlhub.SyncRun: %w", err)
	}

	rec.FromSHA = sr.FromSHA
	rec.ToSHA = sr.ToSHA
	rec.Commits = sr.Commits
	rec.Bytes = sr.Bytes
	rec.Duration = sr.Duration
	rec.UpToDate = sr.UpToDate

	if sr.UpToDate {
		s.recordSyncRunAudit(ctx, "sync_run_uptodate", start, sr, 0, nil, nil)
		return rec, nil
	}

	var written int
	var refreshErr error
	if !opts.SkipRefresh {
		written, refreshErr = s.RefreshDriftSummary(ctx)
		if refreshErr != nil {
			// Sync landed; surface the refresh failure but
			// don't fail the verb. Operator can retry
			// `bzlhub drift refresh`.
			slog.Warn("sync_run: drift refresh after Sync failed",
				"from", sr.FromSHA, "to", sr.ToSHA, "err", refreshErr)
		}
	}
	rec.DriftRowsRewritten = written

	s.recordSyncRunAudit(ctx, "sync_run_success", start, sr, written, nil, refreshErr)
	return rec, nil
}

// recordSyncRunAudit writes one audit row capturing the sync-run
// outcome. refreshErr captures a post-Sync drift recompute failure
// on the success path; it's nil otherwise.
func (s *Service) recordSyncRunAudit(ctx context.Context, kind string, start time.Time, sr bcrmirror.SyncReceipt, driftRows int, opErr error, refreshErr error) {
	refreshErrMsg := ""
	if refreshErr != nil {
		refreshErrMsg = refreshErr.Error()
	}
	s.emitSyncAudit(ctx, kind, start, opErr, syncAuditPayload{
		FromSHA:            sr.FromSHA,
		ToSHA:              sr.ToSHA,
		Commits:            sr.Commits,
		Bytes:              sr.Bytes,
		UpToDate:           sr.UpToDate,
		DriftRowsRewritten: driftRows,
		DriftRefreshError:  refreshErrMsg,
	})
}

