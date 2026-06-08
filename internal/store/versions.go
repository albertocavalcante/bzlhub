package store

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"time"
)

// ModuleVersion is one indexed (module, version) row, used by callers
// that need to walk the whole index (e.g. `bzlhub verify`).
//
// IngestedAt is the wall-clock time the row was first written to the
// versions table — surfaces "how fresh is the data in this registry?"
// without needing per-event audit-log queries.
type ModuleVersion struct {
	Module     string
	Version    string
	IngestedAt time.Time
}

// VersionRow is one row from ListVersionsWithMeta — version string
// plus per-row metadata the UI surfaces on /modules/<name> rows
// (ingest timestamp, compatibility level, tarball size).
type VersionRow struct {
	Version            string
	IngestedAt         time.Time
	CompatibilityLevel int
	TarballSize        int64
}

// ListAllVersions returns every (module, version) row in the index,
// sorted by module ASC then version ASC. Built for `bzlhub verify`'s
// index-vs-mirror agreement check, which needs the full set rather
// than a per-name lookup. Stable ordering keeps reports diffable.
func (s *Store) ListAllVersions(ctx context.Context) ([]ModuleVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT module_name, version, ingested_at FROM versions ORDER BY module_name, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModuleVersion
	for rows.Next() {
		var mv ModuleVersion
		var ingested string
		if err := rows.Scan(&mv.Module, &mv.Version, &ingested); err != nil {
			return nil, err
		}
		// SQLite datetime('now') emits "2006-01-02 15:04:05" (UTC,
		// no T, no zone). Parse into time.Time; fall through to
		// zero value on bad data rather than failing the whole
		// listing — IngestedAt is an enhancement, not load-bearing.
		if t, err := time.Parse(time.DateTime, ingested); err == nil {
			mv.IngestedAt = t.UTC()
		}
		out = append(out, mv)
	}
	return out, rows.Err()
}

// ModuleVersionDrift bundles a versions row with its drift payload
// for the single-query backfill / status walkers — eliminates the
// N+1 of ListAllVersions + per-row GetDriftSummary.
type ModuleVersionDrift struct {
	Module      string
	Version     string
	DriftRaw    []byte // raw drift_summary_json column; "{}" when unset
	IngestedAt  time.Time
}

// AllVersionsWithDrift streams every (module, version) row paired
// with its drift_summary_json blob, sorted by module then version.
// One SQL round-trip in place of len(rows)+1 GetDriftSummary calls.
//
// Streaming via iter.Seq2 means ctx cancellation stops the SQL
// fetch mid-stream (not just the consumer's processing), and the
// underlying *sql.Rows is closed automatically when the range
// loop ends — break, return, panic, or completion. Callers MUST
// check the per-row error before reading the row value.
func (s *Store) AllVersionsWithDrift(ctx context.Context) iter.Seq2[ModuleVersionDrift, error] {
	return func(yield func(ModuleVersionDrift, error) bool) {
		rows, err := s.db.QueryContext(ctx,
			`SELECT module_name, version, drift_summary_json, ingested_at
			 FROM versions ORDER BY module_name, version`)
		if err != nil {
			yield(ModuleVersionDrift{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var mvd ModuleVersionDrift
			var driftStr, ingested string
			if err := rows.Scan(&mvd.Module, &mvd.Version, &driftStr, &ingested); err != nil {
				yield(ModuleVersionDrift{}, err)
				return
			}
			mvd.DriftRaw = []byte(driftStr)
			if t, err := time.Parse(time.DateTime, ingested); err == nil {
				mvd.IngestedAt = t.UTC()
			}
			if !yield(mvd, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(ModuleVersionDrift{}, err)
		}
	}
}

// SetTarballSize persists the compressed-tarball size for an
// already-ingested (module, version). Idempotent. No-op when the
// row doesn't exist (caller violated invariant; not our problem).
func (s *Store) SetTarballSize(ctx context.Context, name, version string, size int64) error {
	if name == "" || version == "" {
		return errors.New("SetTarballSize: module + version required")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE versions SET tarball_size = ? WHERE module_name = ? AND version = ?`,
		size, name, version)
	return err
}

// SetHasSourceIndex persists the "this version has a non-empty SCIP
// index" flag. Idempotent. Called from the ingest path after the
// SCIP blob is written, and from the boot-time backfill that
// reconciles pre-migration rows.
func (s *Store) SetHasSourceIndex(ctx context.Context, name, version string, has bool) error {
	if name == "" || version == "" {
		return errors.New("SetHasSourceIndex: module + version required")
	}
	v := 0
	if has {
		v = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE versions SET has_source_index = ? WHERE module_name = ? AND version = ?`,
		v, name, version)
	return err
}

// GetHasSourceIndex reads the cached flag. Returns false for rows
// that don't exist (caller violation; not our problem).
func (s *Store) GetHasSourceIndex(ctx context.Context, name, version string) (bool, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		`SELECT has_source_index FROM versions WHERE module_name = ? AND version = ?`,
		name, version).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// GetTarballSize returns the compressed-tarball size for (name, version).
// Returns 0 + no error for unknown sizes (pre-migration ingests).
func (s *Store) GetTarballSize(ctx context.Context, name, version string) (int64, error) {
	var size int64
	err := s.db.QueryRowContext(ctx,
		`SELECT tarball_size FROM versions WHERE module_name = ? AND version = ?`,
		name, version).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return size, err
}

// ListVersions returns versions of a module in descending lexical order.
func (s *Store) ListVersions(ctx context.Context, name string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version FROM versions WHERE module_name = ? ORDER BY version DESC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListVersionsWithMeta is ListVersions plus the per-row metadata
// columns the UI badges each row with. Returns rows DESC by version,
// same ordering as ListVersions.
func (s *Store) ListVersionsWithMeta(ctx context.Context, name string) ([]VersionRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version, ingested_at, compatibility_level, tarball_size
		   FROM versions
		  WHERE module_name = ?
		  ORDER BY version DESC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionRow
	for rows.Next() {
		var r VersionRow
		var ingested string
		if err := rows.Scan(&r.Version, &ingested, &r.CompatibilityLevel, &r.TarballSize); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.DateTime, ingested); err == nil {
			r.IngestedAt = t.UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
