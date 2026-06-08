package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// auditTimestampLayout is a fixed-width RFC3339-compatible layout that
// always emits 9 fractional-second digits. RFC3339Nano truncates trailing
// zeros, which makes lex (string) comparison disagree with chronological
// order at boundaries — e.g., "...48.1Z" lex-sorts after "...48.101Z"
// because 'Z'(90) > '0'(48). The Since filter compares timestamps as
// text in SQL, so a fixed width is mandatory for correctness.
//
// Reads still go through time.Parse(time.RFC3339Nano, …), which accepts
// both this layout and legacy truncated stamps.
const auditTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// AuditEvent is one row of the audit_events table. JSON tags align with
// the wire shape exposed by /api/history and the bzlhub_history MCP tool.
type AuditEvent struct {
	ID         int64           `json:"id"`
	Timestamp  time.Time       `json:"timestamp"`
	Kind       string          `json:"kind"`              // e.g. "bump_success", "ingest_recursive_failure"
	Source     string          `json:"source"`            // "drift-ui" | "cli" | "mcp" | "rest" | "unknown"
	Module     string          `json:"module,omitempty"`
	Version    string          `json:"version,omitempty"`
	OK         bool            `json:"ok"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	Error      string          `json:"error,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	// UserID is the authenticated request's display name (email or
	// username from the auth.Identity attached to ctx). Empty for
	// anonymous requests + pre-migration rows.
	UserID string `json:"user_id,omitempty"`
}

// ListAuditAfterID returns events with id > afterID, OLDEST-first
// (ascending id). Used by the webhook delivery daemon as a watermark
// cursor — pass the last successfully-delivered id back to fetch
// the next batch.
//
// Limit defaults to 100 when ≤ 0 and is capped at 1000 to keep
// each batch bounded.
func (s *Store) ListAuditAfterID(ctx context.Context, afterID int64, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, kind, source, module, version, ok, duration_ms, error, payload, user_id
		FROM audit_events
		WHERE id > ?
		ORDER BY id ASC
		LIMIT ?
	`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit_events after id: %w", err)
	}
	defer rows.Close()
	return scanAuditRows(rows)
}

// MaxAuditID returns the highest id in audit_events, or 0 when
// the table is empty. Used by webhook delivery to set its
// watermark at boot — events recorded BEFORE canopy started
// aren't re-delivered.
func (s *Store) MaxAuditID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM audit_events`).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("max audit_events.id: %w", err)
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

// PruneAudit deletes audit_events rows whose ts is older than now
// minus olderThan. Returns the number of rows deleted. A
// retention sweep called from canopy's audit-retention daemon.
//
// olderThan ≤ 0 is a no-op (returns 0). The store doesn't enforce
// a minimum retention — operators wanting "never prune" set
// policy.audit.retain_days to 0 and skip wiring the daemon.
func (s *Store) PruneAudit(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-olderThan).Format(auditTimestampLayout)
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM audit_events WHERE ts < ?
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune audit_events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("RowsAffected: %w", err)
	}
	return int(n), nil
}

// RecordAudit appends one event. Failures here are surfaced to the caller
// so the audit trail doesn't silently miss state changes; the caller
// decides whether to retry, surface to the user, or proceed regardless.
func (s *Store) RecordAudit(ctx context.Context, ev AuditEvent) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.Source == "" {
		ev.Source = "unknown"
	}
	var payload sql.NullString
	if len(ev.Payload) > 0 && string(ev.Payload) != "null" {
		payload = sql.NullString{String: string(ev.Payload), Valid: true}
	}
	var errVal sql.NullString
	if ev.Error != "" {
		errVal = sql.NullString{String: ev.Error, Valid: true}
	}
	var modVal, verVal sql.NullString
	if ev.Module != "" {
		modVal = sql.NullString{String: ev.Module, Valid: true}
	}
	if ev.Version != "" {
		verVal = sql.NullString{String: ev.Version, Valid: true}
	}
	var dur sql.NullInt64
	if ev.DurationMs > 0 {
		dur = sql.NullInt64{Int64: ev.DurationMs, Valid: true}
	}

	okInt := 0
	if ev.OK {
		okInt = 1
	}
	var userVal sql.NullString
	if ev.UserID != "" {
		userVal = sql.NullString{String: ev.UserID, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_events
		    (ts, kind, source, module, version, ok, duration_ms, error, payload, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ev.Timestamp.UTC().Format(auditTimestampLayout),
		ev.Kind, ev.Source,
		modVal, verVal,
		okInt, dur, errVal, payload, userVal,
	)
	if err != nil {
		return fmt.Errorf("insert audit_events: %w", err)
	}
	return nil
}

// AuditQuery filters a ListAudit call. All fields optional; zero values
// disable that filter dimension.
type AuditQuery struct {
	Kinds  []string  // exact-match any-of
	Source string    // exact match
	Module string    // exact match
	Since  time.Time // ts >=
	Limit  int       // default 100, max 10000
}

// ListAudit returns matching events newest-first. Bounded by Limit to
// keep responses small; agents/UIs are expected to paginate via Since.
func (s *Store) ListAudit(ctx context.Context, q AuditQuery) ([]AuditEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}

	clauses := []string{"1=1"}
	args := []any{}
	if len(q.Kinds) > 0 {
		placeholders := make([]string, len(q.Kinds))
		for i, k := range q.Kinds {
			placeholders[i] = "?"
			args = append(args, k)
		}
		clauses = append(clauses, "kind IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.Source != "" {
		clauses = append(clauses, "source = ?")
		args = append(args, q.Source)
	}
	if q.Module != "" {
		clauses = append(clauses, "module = ?")
		args = append(args, q.Module)
	}
	if !q.Since.IsZero() {
		clauses = append(clauses, "ts >= ?")
		args = append(args, q.Since.UTC().Format(auditTimestampLayout))
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, kind, source, module, version, ok, duration_ms, error, payload, user_id
		FROM audit_events
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY ts DESC, id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit_events: %w", err)
	}
	defer rows.Close()
	return scanAuditRows(rows)
}

// scanAuditRows is the shared row-decode loop for every audit-list
// query. Caller closes the *sql.Rows.
func scanAuditRows(rows *sql.Rows) ([]AuditEvent, error) {
	var out []AuditEvent
	for rows.Next() {
		var (
			ev      AuditEvent
			tsStr   string
			mod     sql.NullString
			ver     sql.NullString
			okInt   int
			dur     sql.NullInt64
			errVal  sql.NullString
			payload sql.NullString
			userID  sql.NullString
		)
		if err := rows.Scan(&ev.ID, &tsStr, &ev.Kind, &ev.Source, &mod, &ver, &okInt, &dur, &errVal, &payload, &userID); err != nil {
			return nil, fmt.Errorf("scan audit row: %w", err)
		}
		if t, perr := time.Parse(time.RFC3339Nano, tsStr); perr == nil {
			ev.Timestamp = t
		}
		ev.Module = mod.String
		ev.Version = ver.String
		ev.OK = okInt != 0
		if dur.Valid {
			ev.DurationMs = dur.Int64
		}
		ev.Error = errVal.String
		if payload.Valid {
			ev.Payload = json.RawMessage(payload.String)
		}
		ev.UserID = userID.String
		out = append(out, ev)
	}
	return out, rows.Err()
}
