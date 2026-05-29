package store

import (
	"context"
	"fmt"

	"github.com/albertocavalcante/canopy/internal/api"
)

// ExternalRef is one stored external URL reference.
type ExternalRef struct {
	URL        string
	Host       string
	Class      string
	Mutability string
	SHA256     string
	Integrity  string
	APIName    string
	RuleName   string
	Platform   string
	Tainted    bool
	File       string
}

// ExternalForkError is a stored per-fork interpretation error.
type ExternalForkError struct {
	Platform string
	Message  string
}

// WriteExternalRefs replaces all external_refs and external_fork_errors
// rows for (name, version) atomically. Idempotent: re-ingest of the same
// (name, version) wipes prior rows before inserting the new set.
func (s *Store) WriteExternalRefs(ctx context.Context, name, version string, refs []ExternalRef, forkErrors []ExternalForkError) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM external_refs WHERE module_name = ? AND version = ?`,
		name, version,
	); err != nil {
		return fmt.Errorf("delete refs: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM external_fork_errors WHERE module_name = ? AND version = ?`,
		name, version,
	); err != nil {
		return fmt.Errorf("delete fork errors: %w", err)
	}

	for _, r := range refs {
		tainted := 0
		if r.Tainted {
			tainted = 1
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT OR REPLACE INTO external_refs
                (module_name, version, url, host, class, mutability, sha256, integrity, api_name, rule_name, platform, tainted, file)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			name, version, r.URL, r.Host, r.Class, r.Mutability,
			r.SHA256, r.Integrity, r.APIName, r.RuleName,
			defaultPlatform(r.Platform), tainted, r.File,
		); err != nil {
			return fmt.Errorf("insert ref %q: %w", r.URL, err)
		}
	}

	for _, e := range forkErrors {
		if _, err := tx.ExecContext(ctx, `
            INSERT OR REPLACE INTO external_fork_errors
                (module_name, version, platform, error_message)
            VALUES (?,?,?,?)`,
			name, version, defaultPlatform(e.Platform), e.Message,
		); err != nil {
			return fmt.Errorf("insert fork error: %w", err)
		}
	}

	return tx.Commit()
}

// GetExternalRefs returns every external_refs row for (name, version),
// ordered by class then URL for stable callers.
func (s *Store) GetExternalRefs(ctx context.Context, name, version string) ([]ExternalRef, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT url, host, class, mutability, sha256, integrity, api_name, rule_name, platform, tainted, file
          FROM external_refs
         WHERE module_name = ? AND version = ?
         ORDER BY class, url, platform, file`,
		name, version,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []ExternalRef
	for rows.Next() {
		var r ExternalRef
		var tainted int
		if err := rows.Scan(&r.URL, &r.Host, &r.Class, &r.Mutability,
			&r.SHA256, &r.Integrity, &r.APIName, &r.RuleName,
			&r.Platform, &tainted, &r.File); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		r.Tainted = tainted != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetExternalForkErrors returns every external_fork_errors row for
// (name, version).
func (s *Store) GetExternalForkErrors(ctx context.Context, name, version string) ([]ExternalForkError, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT platform, error_message
          FROM external_fork_errors
         WHERE module_name = ? AND version = ?
         ORDER BY platform, error_message`,
		name, version,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []ExternalForkError
	for rows.Next() {
		var e ExternalForkError
		if err := rows.Scan(&e.Platform, &e.Message); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func defaultPlatform(s string) string {
	if s == "" {
		return api.DefaultPlatform
	}
	return s
}
