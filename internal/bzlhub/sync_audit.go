package bzlhub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// syncAuditPayload is the shared wire shape both sync writers
// marshal into audit_events.payload and SyncHistory unmarshals
// back. One type, one schema; new fields land here and producers +
// reader pick them up together.
type syncAuditPayload struct {
	Remote             string `json:"remote,omitempty"`
	MirrorPath         string `json:"mirror_path,omitempty"`
	Branch             string `json:"branch,omitempty"`
	Reinit             bool   `json:"reinit,omitempty"`
	FromSHA            string `json:"from_sha,omitempty"`
	ToSHA              string `json:"to_sha,omitempty"`
	Commits            int    `json:"commits,omitempty"`
	Bytes              int64  `json:"bytes,omitempty"`
	UpToDate           bool   `json:"up_to_date,omitempty"`
	DriftRowsRewritten int    `json:"drift_rows_rewritten,omitempty"`

	// DriftRefreshError captures a post-Sync RefreshDriftSummary
	// failure on a sync_run_success row — Sync advanced but drift
	// didn't, and the operator needs to see that in the audit log.
	DriftRefreshError string `json:"drift_refresh_error,omitempty"`
}

// emitSyncAudit writes one sync-* audit row. opErr is the
// top-level operation outcome; nil means OK. payload's marshalled
// bytes land in the payload column.
func (s *Service) emitSyncAudit(ctx context.Context, kind string, start time.Time, opErr error, payload syncAuditPayload) {
	ok := opErr == nil
	errMsg := ""
	if opErr != nil {
		errMsg = opErr.Error()
	}
	raw, _ := json.Marshal(payload)
	s.audit(ctx, store.AuditEvent{
		Kind:       kind,
		Source:     "cli",
		OK:         ok,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      errMsg,
		Payload:    raw,
	})
}
