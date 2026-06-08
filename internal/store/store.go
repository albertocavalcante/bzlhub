// Package store wraps a SQLite database holding canopy's index of
// ingested modules. Writes happen via WriteReport; reads via Search and
// GetReport. Both use FTS5 with the trigram tokenizer for sub-10ms search.
//
// File layout:
//   - store.go     Store struct + Open/Close + migrations + tiny shared helpers
//   - reports.go   WriteReport / GetReport (the canonical ModuleReport roundtrip)
//   - search.go    Search + hermeticity helpers + VersionExists
//   - scip.go      SCIP blob storage (per-module index of code-nav data)
//   - versions.go  Version listing + tarball-size metadata
//
// Plus the pre-existing siblings: audit.go, external.go, github_meta.go,
// module_sources.go, module_extension_sources.go, use_extension_usages.go
// — each owning one schema slice.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store is canopy's index store.
type Store struct {
	db *sql.DB
}

// Open opens a SQLite database at the given path (":memory:" works) and runs
// migrations.
//
// `foreign_keys=ON` MUST be applied per-connection — SQLite's pragma is
// session-scoped and the sql.DB pool hands out fresh connections on
// demand. Setting it via the DSN ensures every connection coming out of
// the pool has cascades enabled, which is load-bearing for the
// re-ingest cascade in WriteReport.
func Open(ctx context.Context, path string) (*Store, error) {
	// Per-connection pragmas baked into the DSN. journal_mode=WAL +
	// busy_timeout=5s defang the "database is locked" SQLITE_BUSY
	// errors that surface under concurrent writers (preflight pool,
	// admit pool, audit retention sweep, webhook watermark advance,
	// HTTP handlers all sharing one file lock). Saw one in the
	// preflight no-duplicate-processing test before this — WAL lets
	// readers proceed during writes; busy_timeout queues racing
	// writers for up to 5s instead of failing immediately.
	//
	// WAL adds -wal + -shm sidecar files next to the database;
	// backup scripts should copy them together or use
	// `sqlite3 .backup`.  Self-hosted/canopy-demo/DEPLOY.md backup
	// section uses a named volume tar which captures all three.
	dsn := path
	pragmas := "_pragma=foreign_keys(true)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	if !strings.Contains(dsn, "?") {
		dsn += "?" + pragmas
	} else {
		dsn += "&" + pragmas
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Idempotent column-add migrations for pre-existing databases.
	// Each helper is "check + ALTER" so re-runs are no-ops; safe to
	// invoke on every Open. Promote to a proper migration framework
	// when v1.0 commits to a schema version field.
	if err := ensureColumn(ctx, db, "versions", "tarball_size",
		"ALTER TABLE versions ADD COLUMN tarball_size INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate tarball_size: %w", err)
	}
	// has_source_index: cached bool for "does this (module, version)'s
	// SCIP blob contain at least one indexed Starlark document?" The
	// listing page reads this directly; the search hit projection
	// joins on it; the ingest path keeps it fresh on every WriteScipBlob.
	// Default 0 so freshly-migrated rows don't lie — a one-shot backfill
	// at boot reconciles existing rows whose blobs are non-empty.
	if err := ensureColumn(ctx, db, "versions", "has_source_index",
		"ALTER TABLE versions ADD COLUMN has_source_index INTEGER NOT NULL DEFAULT 0"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate has_source_index: %w", err)
	}
	if err := ensureColumn(ctx, db, "audit_events", "user_id",
		"ALTER TABLE audit_events ADD COLUMN user_id TEXT"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate audit_events.user_id: %w", err)
	}
	// drift_summary_json: cached JSON-encoded api.DriftSummary per
	// (module, version). Read by ListModules/GetModule for the
	// inline-drift-badges feature (Plan 19 Idea A, Plan 22 PR 3).
	// Default '{}' decodes to api.DriftSummary{} (Status=unknown),
	// which is the correct shape for rows that predate the column
	// or for canopies running without a configured drift source.
	// JSON column over typed columns matches the existing
	// hermeticity_json + report_json convention and lets Plan 21's
	// layered staleness fields land additively without further
	// migration.
	if err := ensureColumn(ctx, db, "versions", "drift_summary_json",
		"ALTER TABLE versions ADD COLUMN drift_summary_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate drift_summary_json: %w", err)
	}
	if err := normalizeAuditTimestamps(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate audit_events.ts: %w", err)
	}
	return &Store{db: db}, nil
}

// normalizeAuditTimestamps rewrites pre-fix audit_events.ts strings
// (RFC3339Nano with truncated trailing zeros — variable width) to the
// canonical fixed-width 9-digit fractional form used by
// auditTimestampLayout. The Since filter compares timestamps as SQL
// strings, so mixed widths flipped lex ordering at the 'Z'(90) vs
// '0'(48) boundary. Reads still go through time.Parse(RFC3339Nano,
// …), which accepts both forms, but every row must be canonical for
// the Since filter to be correct across the full history.
//
// Cheap and idempotent: the canonical row count is 30 characters
// ("YYYY-MM-DDTHH:MM:SS.NNNNNNNNNZ"). If no shorter rows exist, this
// is a single COUNT and exits.
func normalizeAuditTimestamps(ctx context.Context, db *sql.DB) error {
	const canonicalLen = 30
	var pending int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE LENGTH(ts) <> ?`, canonicalLen,
	).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, ts FROM audit_events WHERE LENGTH(ts) <> ?`, canonicalLen)
	if err != nil {
		return err
	}
	defer rows.Close()
	type update struct {
		id int64
		ts string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var ts string
		if err := rows.Scan(&id, &ts); err != nil {
			return err
		}
		t, perr := time.Parse(time.RFC3339Nano, ts)
		if perr != nil {
			// Row is corrupt or non-RFC3339Nano; leave it alone — we
			// shouldn't silently mangle it. Subsequent Since filters
			// over it remain best-effort.
			continue
		}
		updates = append(updates, update{id: id, ts: t.UTC().Format(auditTimestampLayout)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `UPDATE audit_events SET ts = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.ts, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureColumn runs the given ALTER iff the column doesn't already
// exist on the table. Tiny additive-migration helper used by Open.
func ensureColumn(ctx context.Context, db *sql.DB, table, col, alterSQL string) error {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col,
	).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, alterSQL)
	return err
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// PragmaForeignKeys returns the current foreign_keys pragma value on a
// freshly-allocated pool connection. Used by tests; not part of the
// production read/write surface.
func PragmaForeignKeys(ctx context.Context, s *Store) (int, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var v int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// boolInt converts Go's bool to SQLite's INTEGER convention (1/0).
// Used by the relational inserts where we mirror struct booleans.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
