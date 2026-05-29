package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/albertocavalcante/assay/report"
)

// WriteReport persists a ModuleReport, replacing any prior version row
// for the same (module, version). Atomic via a single transaction.
func (s *Store) WriteReport(ctx context.Context, r *report.ModuleReport) error {
	if r.Name == "" {
		return errors.New("report has empty Name")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO modules(name) VALUES(?)`, r.Name); err != nil {
		return fmt.Errorf("insert module: %w", err)
	}

	bcJSON, _ := json.Marshal(r.BazelCompatibility)
	hermJSON, _ := json.Marshal(r.Hermeticity)
	reportJSON, _ := json.Marshal(r)

	// REPLACE clears the row + cascades to dependent tables. Then re-insert.
	if _, err := tx.ExecContext(ctx, `DELETE FROM versions WHERE module_name = ? AND version = ?`,
		r.Name, r.Version); err != nil {
		return fmt.Errorf("delete prior version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO versions(module_name, version, compatibility_level, bazel_compatibility, hermeticity_json, report_json)
		VALUES(?, ?, ?, ?, ?, ?)`,
		r.Name, r.Version, r.CompatibilityLevel, string(bcJSON), string(hermJSON), string(reportJSON)); err != nil {
		return fmt.Errorf("insert version: %w", err)
	}

	// Clear stale FTS rows for this (module, version).
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_text WHERE rowid IN (
		SELECT rowid FROM fts_meta WHERE module_name = ? AND version = ?
	)`, r.Name, r.Version); err != nil {
		return fmt.Errorf("clear fts_text: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_meta WHERE module_name = ? AND version = ?`,
		r.Name, r.Version); err != nil {
		return fmt.Errorf("clear fts_meta: %w", err)
	}

	// Index the module name itself. No file context — the module entry
	// is the module name itself, not a definition site.
	if err := insertFTS(ctx, tx, r.Name, r.Version, "module", r.Name, "", r.Name); err != nil {
		return err
	}

	// Rules.
	for _, rule := range r.Rules {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO rules(module_name, version, name, doc, file, line, private)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			r.Name, r.Version, rule.Name, rule.Doc, rule.Provenance.File, rule.Provenance.StartRow, boolInt(rule.Private)); err != nil {
			return fmt.Errorf("insert rule %s: %w", rule.Name, err)
		}
		text := rule.Name + " " + rule.Doc
		if err := insertFTS(ctx, tx, r.Name, r.Version, "rule", rule.Name, rule.Provenance.File, text); err != nil {
			return err
		}
	}

	// Providers.
	for _, p := range r.Providers {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO providers(module_name, version, name, doc, file, line, private)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			r.Name, r.Version, p.Name, p.Doc, p.Provenance.File, p.Provenance.StartRow, boolInt(p.Private)); err != nil {
			return fmt.Errorf("insert provider %s: %w", p.Name, err)
		}
		text := p.Name + " " + p.Doc
		if err := insertFTS(ctx, tx, r.Name, r.Version, "provider", p.Name, p.Provenance.File, text); err != nil {
			return err
		}
	}

	// Macros (FTS only — no separate relational table yet).
	for _, m := range r.Macros {
		text := m.Name + " " + m.Doc
		if err := insertFTS(ctx, tx, r.Name, r.Version, "macro", m.Name, m.Provenance.File, text); err != nil {
			return err
		}
	}

	// Hermeticity classes.
	for _, c := range r.Hermeticity.Classes {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO hermeticity_classes(module_name, version, class)
			VALUES(?, ?, ?)`, r.Name, r.Version, string(c)); err != nil {
			return fmt.Errorf("insert hermeticity class: %w", err)
		}
	}

	return tx.Commit()
}

// insertFTS adds one (module, version, kind, name, file, text) row to
// the FTS5 virtual table + sidecar meta table. Called from WriteReport
// per indexed entity (module/rule/provider/macro).
func insertFTS(ctx context.Context, tx *sql.Tx, module, version, kind, name, file, text string) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO fts_text(text) VALUES(?)`, text)
	if err != nil {
		return fmt.Errorf("insert fts_text: %w", err)
	}
	rowid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO fts_meta(rowid, module_name, version, kind, name, file)
		VALUES(?, ?, ?, ?, ?, ?)`, rowid, module, version, kind, name, file); err != nil {
		return fmt.Errorf("insert fts_meta: %w", err)
	}
	return nil
}

// GetReport reconstructs a ModuleReport from the stored report_json blob.
func (s *Store) GetReport(ctx context.Context, name, version string) (*report.ModuleReport, error) {
	var blob string
	err := s.db.QueryRowContext(ctx,
		`SELECT report_json FROM versions WHERE module_name = ? AND version = ?`,
		name, version).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s@%s: not found", name, version)
	}
	if err != nil {
		return nil, err
	}
	var r report.ModuleReport
	if err := json.Unmarshal([]byte(blob), &r); err != nil {
		return nil, fmt.Errorf("decode report: %w", err)
	}
	return &r, nil
}
