package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func openTempStore(t testing.TB) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordAndListAudit(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	// Three events spaced by source + kind.
	evs := []AuditEvent{
		{Kind: "bump_success", Source: "drift-ui", Module: "rules_go", Version: "0.52.0", OK: true, DurationMs: 1200, Payload: json.RawMessage(`{"rules":42}`)},
		{Kind: "bump_failure", Source: "cli", Module: "libpfm", Version: "4.11.0", OK: false, Error: "integrity mismatch", DurationMs: 800},
		{Kind: "ingest_recursive_success", Source: "mcp", Module: "bazel_skylib", Version: "1.9.0", OK: true, DurationMs: 4500, Payload: json.RawMessage(`{"visited":3,"mirrored":3}`)},
	}
	for _, e := range evs {
		if err := s.RecordAudit(ctx, e); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	// Unfiltered list returns all three, newest-first.
	got, err := s.ListAudit(ctx, AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Kind != "ingest_recursive_success" {
		t.Errorf("newest-first ordering broken: %s", got[0].Kind)
	}
}

func TestListAuditFilters(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, kind := range []string{"bump_success", "bump_failure", "ingest_recursive_success", "bump_success"} {
		_ = s.RecordAudit(ctx, AuditEvent{
			Kind:      kind,
			Source:    []string{"drift-ui", "cli", "mcp", "rest"}[i],
			Module:    "rules_x",
			OK:        kind != "bump_failure",
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
		})
	}

	// Filter by kind.
	bumps, _ := s.ListAudit(ctx, AuditQuery{Kinds: []string{"bump_success"}})
	if len(bumps) != 2 {
		t.Errorf("bump_success count: got %d want 2", len(bumps))
	}

	// Filter by source.
	cliEvs, _ := s.ListAudit(ctx, AuditQuery{Source: "cli"})
	if len(cliEvs) != 1 || cliEvs[0].Source != "cli" {
		t.Errorf("source filter: %+v", cliEvs)
	}

	// Limit.
	one, _ := s.ListAudit(ctx, AuditQuery{Limit: 1})
	if len(one) != 1 {
		t.Errorf("limit=1: got %d", len(one))
	}

	// Since: only events after the 2nd insert (index 2 onward).
	since := now.Add(2*time.Millisecond - time.Microsecond)
	tail, _ := s.ListAudit(ctx, AuditQuery{Since: since})
	if len(tail) != 2 {
		t.Errorf("since filter: got %d want 2", len(tail))
	}
}

// Regression: RFC3339Nano truncates trailing-zero fractional digits, so
// lex comparison of two timestamps with different fractional lengths
// can disagree with chronological order (e.g., "...48.1Z" lex-sorts
// after "...48.101Z" because 'Z' > '0'). The audit store compares Since
// with SQL string >=, so the format MUST be fixed-width — otherwise the
// Since filter flakes whenever now.Nanosecond() ends in zeros.
func TestListAudit_SinceFilter_TrailingZeroNanos(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	// Force nanoseconds with trailing zeros (truncates RFC3339Nano to ".1Z").
	now := time.Date(2026, 5, 20, 13, 50, 48, 100_000_000, time.UTC)
	for i := range 4 {
		_ = s.RecordAudit(ctx, AuditEvent{
			Kind:      "bump_success",
			Source:    "cli",
			Module:    "m",
			OK:        true,
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
		})
	}

	since := now.Add(2*time.Millisecond - time.Microsecond)
	tail, err := s.ListAudit(ctx, AuditQuery{Since: since})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tail) != 2 {
		t.Errorf("trailing-zero nanos: got %d events, want 2 — lex/chrono divergence", len(tail))
	}
}

// The audit-timestamp lex/chrono fix only works if EVERY row uses the
// canonical 9-digit fractional layout. Existing prod DBs have rows
// written by the old RFC3339Nano-truncating code path; Open() runs a
// normalization migration that re-formats them. This test seeds a row
// in the legacy shape (bypassing the public API), reopens, and asserts
// the Since filter is correct across the boundary.
func TestNormalizeAuditTimestamps_RewritesLegacyRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	// Insert a row whose ts has trailing-zero nanos, written through
	// the legacy RFC3339Nano path (which truncates).
	legacy := time.Date(2026, 5, 20, 13, 50, 48, 100_000_000, time.UTC)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO audit_events (ts, kind, source, ok) VALUES (?, ?, ?, ?)`,
		legacy.Format(time.RFC3339Nano), "bump_success", "cli", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	// Confirm the row is in legacy form (length != 30).
	var prevLen int
	if err := s.db.QueryRowContext(ctx,
		`SELECT LENGTH(ts) FROM audit_events`,
	).Scan(&prevLen); err != nil {
		t.Fatal(err)
	}
	if prevLen == 30 {
		t.Fatalf("legacy row already canonical (len=%d), test premise broken", prevLen)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen — migration should normalize the row.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var newLen int
	if err := s.db.QueryRowContext(ctx,
		`SELECT LENGTH(ts) FROM audit_events`,
	).Scan(&newLen); err != nil {
		t.Fatal(err)
	}
	if newLen != 30 {
		t.Errorf("post-migration len = %d, want 30", newLen)
	}

	// Since filter must now be correct for the previously-flaky boundary.
	since := legacy.Add(time.Millisecond)
	tail, err := s.ListAudit(ctx, AuditQuery{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Errorf("since filter post-migration: got %d events, want 0 (legacy row is before since)", len(tail))
	}
}

func TestPayloadRoundtrip(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	want := json.RawMessage(`{"visited":31,"mirrored":30,"errors":["libpfm@4.11.0"]}`)
	if err := s.RecordAudit(ctx, AuditEvent{Kind: "ingest_recursive_success", Source: "mcp", Module: "rules_go", Version: "0.52.0", OK: true, Payload: want}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAudit(ctx, AuditQuery{})
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v %d", err, len(got))
	}
	// Compare canonical-form JSON: drivers may normalize whitespace.
	var a, b any
	_ = json.Unmarshal(want, &a)
	_ = json.Unmarshal(got[0].Payload, &b)
	if !jsonEqual(a, b) {
		t.Fatalf("payload mismatch: want %s got %s", want, got[0].Payload)
	}
}

func jsonEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
