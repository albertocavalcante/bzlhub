package store

import (
	"context"
	"database/sql"
	"errors"
)

// emptyDriftJSON is the canonical "no drift data" payload, matching
// the column's SQL default. UPDATEs that pass nil or zero-length
// bytes write this verbatim so the column never goes NULL and
// readers never need to handle a sql.NullString.
var emptyDriftJSON = []byte("{}")

// SetDriftSummary persists the JSON-encoded drift summary for
// (name, version). The store layer is JSON-shape-agnostic; the
// canonical shape lives in internal/api/canopy.go (DriftSummary).
// Callers marshal there and hand bytes here.
//
// Passing nil or an empty slice resets the row to the column
// default '{}' — that is the "no drift data" signal a fresh
// canopy or an unconfigured drift source emits.
//
// Idempotent. Called from the future drift-cache write path
// (Plan 19 Idea A backend, Plan 26 κ6 ModuleReport-in-AC pulldown)
// and from the boot-time BackfillDriftSummary seam.
func (s *Store) SetDriftSummary(ctx context.Context, name, version string, payload []byte) error {
	if name == "" || version == "" {
		return errors.New("SetDriftSummary: module + version required")
	}
	if len(payload) == 0 {
		payload = emptyDriftJSON
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE versions SET drift_summary_json = ? WHERE module_name = ? AND version = ?`,
		string(payload), name, version)
	return err
}

// GetDriftSummary reads the cached JSON for (name, version).
// Returns the default '{}' for rows that don't exist — callers
// rendering inline badges shouldn't have to distinguish "row
// missing" from "drift unknown"; both yield the same chip.
func (s *Store) GetDriftSummary(ctx context.Context, name, version string) ([]byte, error) {
	var payload string
	err := s.db.QueryRowContext(ctx,
		`SELECT drift_summary_json FROM versions WHERE module_name = ? AND version = ?`,
		name, version).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return append([]byte(nil), emptyDriftJSON...), nil
	}
	if err != nil {
		return nil, err
	}
	if payload == "" {
		return append([]byte(nil), emptyDriftJSON...), nil
	}
	return []byte(payload), nil
}
