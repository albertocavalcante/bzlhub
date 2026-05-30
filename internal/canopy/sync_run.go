package canopy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/canopy/internal/store"
)

// SyncRunOptions controls a single sync-run invocation.
type SyncRunOptions struct {
	// Force, when true, permits a non-fast-forward pull — the
	// local Mirror is hard-reset to the remote's branch tip even
	// if local and remote diverged. Passed through to
	// bcrmirror.SyncOptions.Force. Defaults to false so accidental
	// invocations on a hand-edited mirror surface the divergence
	// rather than silently discarding the local commits.
	Force bool
}

// SyncRunReceipt summarises one sync-run for the audit log + CLI
// stdout. Mirrors bcrmirror.SyncReceipt with an extra
// DriftRowsRewritten count from the post-sync drift refresh pass.
type SyncRunReceipt struct {
	FromSHA            string
	ToSHA              string
	Commits            int
	Bytes              int64
	Duration           time.Duration
	UpToDate           bool
	DriftRowsRewritten int
}

// SyncRun fetches updates from the Mirror's configured upstream and
// recomputes drift verdicts for every (module, version) row in the
// index. Intended as the periodic counterpart to SyncBootstrap —
// invoked from a systemd timer / launchd / cron / GH Actions
// schedule with `canopy sync run` (Plan 21 sync-runner concept,
// minimum viable iteration: this PR is the verb itself; future
// PRs add LAST_SYNC heartbeat, layered staleness fields on
// DriftSummary, egress audit integration, signature verification,
// and the candidate→trusted promotion model from Plan 21 Part 4.)
//
// Behaviour:
//
//   - No Mirror attached (File backend) → ErrNoMirrorForDrift, no
//     audit. Consistent with RefreshDriftSummary's surface so the
//     CLI can render one shared "needs git-aware mirror" hint.
//   - Sync returns UpToDate → audit "sync_run_uptodate", skip the
//     drift refresh entirely (no upstream metadata changed, so
//     drift can't have changed). Receipt carries UpToDate=true.
//   - Sync returns ErrNotFastForward (local diverged from remote)
//     AND Force=false → audit "sync_run_failure", return the
//     wrapped bcrmirror.ErrNotFastForward so callers can errors.Is
//     and either back off or retry with Force.
//   - Sync advances → audit "sync_run_success" with FromSHA/ToSHA/
//     Commits, then call RefreshDriftSummary so the drift cache
//     reflects the new upstream view. A refresh error is logged
//     but does NOT fail the verb (the sync itself succeeded; an
//     operator can retry the refresh via `canopy drift refresh`).
//   - Any other Sync failure (network, auth) → audit
//     "sync_run_failure", return the wrapped error.
func (s *Service) SyncRun(ctx context.Context, opts SyncRunOptions) (SyncRunReceipt, error) {
	var rec SyncRunReceipt
	if s.mirror == nil {
		return rec, ErrNoMirrorForDrift
	}

	start := time.Now()
	sr, err := s.mirror.Sync(ctx, bcrmirror.SyncOptions{Force: opts.Force})
	if err != nil {
		s.recordSyncRunAudit(ctx, "sync_run_failure", start, sr, 0, err)
		return rec, fmt.Errorf("canopy.SyncRun: %w", err)
	}

	rec.FromSHA = sr.FromSHA
	rec.ToSHA = sr.ToSHA
	rec.Commits = sr.Commits
	rec.Bytes = sr.Bytes
	rec.Duration = sr.Duration
	rec.UpToDate = sr.UpToDate

	if sr.UpToDate {
		s.recordSyncRunAudit(ctx, "sync_run_uptodate", start, sr, 0, nil)
		return rec, nil
	}

	written, refreshErr := s.RefreshDriftSummary(ctx)
	if refreshErr != nil {
		// Don't fail the verb on a refresh error — the Sync itself
		// landed; the operator can retry refresh via `canopy drift
		// refresh`. But surface it in the log so the gap is
		// visible.
		slog.Warn("sync_run: drift refresh after Sync failed",
			"from", sr.FromSHA, "to", sr.ToSHA, "err", refreshErr)
	}
	rec.DriftRowsRewritten = written

	s.recordSyncRunAudit(ctx, "sync_run_success", start, sr, written, nil)
	return rec, nil
}

// recordSyncRunAudit writes one audit row capturing the sync-run
// outcome. Payload carries the Sync receipt + drift refresh count
// so /api/history reviewers can reconstruct what advanced + what
// drift was rewritten in the same boot.
func (s *Service) recordSyncRunAudit(ctx context.Context, kind string, start time.Time, sr bcrmirror.SyncReceipt, driftRows int, opErr error) {
	ok := opErr == nil
	errMsg := ""
	if opErr != nil {
		errMsg = opErr.Error()
	}
	payload, mErr := json.Marshal(struct {
		FromSHA            string `json:"from_sha,omitempty"`
		ToSHA              string `json:"to_sha,omitempty"`
		Commits            int    `json:"commits,omitempty"`
		Bytes              int64  `json:"bytes,omitempty"`
		UpToDate           bool   `json:"up_to_date,omitempty"`
		DriftRowsRewritten int    `json:"drift_rows_rewritten,omitempty"`
	}{
		FromSHA:            sr.FromSHA,
		ToSHA:              sr.ToSHA,
		Commits:            sr.Commits,
		Bytes:              sr.Bytes,
		UpToDate:           sr.UpToDate,
		DriftRowsRewritten: driftRows,
	})
	if mErr != nil {
		slog.Warn("sync_run audit payload marshal failed", "err", mErr)
		payload = nil
	}
	s.audit(ctx, store.AuditEvent{
		Kind:       kind,
		Source:     "cli",
		OK:         ok,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      errMsg,
		Payload:    payload,
	})
}

