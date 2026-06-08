package bzlhub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/store"
)

// syncHistoryKinds is the closed set of audit_events.kind values
// the sync history view surfaces.
var syncHistoryKinds = []string{
	"sync_bootstrap_success",
	"sync_bootstrap_failure",
	"sync_run_success",
	"sync_run_uptodate",
	"sync_run_failure",
}

// SyncHistoryEntry is one event in the sync-runner's audit trail,
// flattened from store.AuditEvent + its payload into a stable wire
// shape for the CLI + API.
type SyncHistoryEntry struct {
	Timestamp         time.Time `json:"timestamp"`
	Kind              string    `json:"kind"`
	OK                bool      `json:"ok"`
	DurationMs        int64     `json:"duration_ms,omitempty"`
	Error             string    `json:"error,omitempty"`
	FromSHA           string    `json:"from_sha,omitempty"`
	ToSHA             string    `json:"to_sha,omitempty"`
	Commits           int       `json:"commits,omitempty"`
	Bytes             int64     `json:"bytes,omitempty"`
	UpToDate          bool      `json:"up_to_date,omitempty"`
	Reinit            bool      `json:"reinit,omitempty"`
	Remote            string    `json:"remote,omitempty"`
	MirrorPath        string    `json:"mirror_path,omitempty"`
	DriftRefreshError string    `json:"drift_refresh_error,omitempty"`
}

// SyncHistory returns recent sync_* audit events, newest first.
// limit <= 0 inherits ListAudit's default. Zero `since` disables
// the time filter.
func (s *Service) SyncHistory(ctx context.Context, limit int, since time.Time) ([]SyncHistoryEntry, error) {
	events, err := s.store.ListAudit(ctx, store.AuditQuery{
		Kinds: syncHistoryKinds,
		Limit: limit,
		Since: since,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SyncHistoryEntry, 0, len(events))
	for _, ev := range events {
		entry := SyncHistoryEntry{
			Timestamp:  ev.Timestamp,
			Kind:       ev.Kind,
			OK:         ev.OK,
			DurationMs: ev.DurationMs,
			Error:      ev.Error,
		}
		if len(ev.Payload) > 0 {
			var p syncAuditPayload
			if jerr := json.Unmarshal(ev.Payload, &p); jerr == nil {
				entry.FromSHA = p.FromSHA
				entry.ToSHA = p.ToSHA
				entry.Commits = p.Commits
				entry.Bytes = p.Bytes
				entry.UpToDate = p.UpToDate
				entry.Reinit = p.Reinit
				entry.Remote = p.Remote
				entry.MirrorPath = p.MirrorPath
				entry.DriftRefreshError = p.DriftRefreshError
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// syncAuditPayload + emitSyncAudit live in sync_audit.go.
