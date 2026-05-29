package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// WriteScipBlob persists a SCIP index for (module, version), replacing
// any prior blob. The bytes are scip-bazel's protobuf output. Kept in
// its own table from versions(report_json) so the index can be
// (re)generated without rewriting the canonical ModuleReport.
//
// Returns an error if the (module, version) row doesn't exist — the
// SCIP blob is supplementary to a real ingested module, never standalone.
func (s *Store) WriteScipBlob(ctx context.Context, name, version string, blob []byte) error {
	if name == "" || version == "" {
		return errors.New("WriteScipBlob: module + version required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO module_scip(module_name, version, blob)
		VALUES (?, ?, ?)
		ON CONFLICT(module_name, version) DO UPDATE SET
			blob = excluded.blob,
			indexed_at = datetime('now')`,
		name, version, blob)
	if err != nil {
		return fmt.Errorf("write scip blob %s@%s: %w", name, version, err)
	}
	return nil
}

// ListScipVersions returns every (module, version) pair that has a
// stored SCIP blob, sorted by module ASC then version ASC. Built for
// `canopy verify`'s scip_present check, which needs the full set
// rather than per-pair existence probes. Returns an empty (not nil)
// slice when the module_scip table is empty.
func (s *Store) ListScipVersions(ctx context.Context) ([]ModuleVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT module_name, version FROM module_scip ORDER BY module_name, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModuleVersion{}
	for rows.Next() {
		var mv ModuleVersion
		if err := rows.Scan(&mv.Module, &mv.Version); err != nil {
			return nil, err
		}
		out = append(out, mv)
	}
	return out, rows.Err()
}

// ErrScipNotFound is returned by GetScipBlob when no SCIP index exists
// for the requested (module, version). Callers should use errors.Is to
// classify this specifically — distinguishing "not yet indexed" from
// generic store failures lets handlers render a helpful 404 instead of
// a generic 5xx (see internal/server/codenav.go for the friendly-page
// surface).
var ErrScipNotFound = errors.New("scip blob not found")

// GetScipBlob returns the stored SCIP index bytes for (module, version).
// Returns an error wrapping ErrScipNotFound when no row exists (no
// scip index produced for this module-version yet, or the ingest
// predates scip-bazel wiring).
func (s *Store) GetScipBlob(ctx context.Context, name, version string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT blob FROM module_scip WHERE module_name = ? AND version = ?`,
		name, version).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s@%s: %w", name, version, ErrScipNotFound)
	}
	if err != nil {
		return nil, err
	}
	return blob, nil
}
