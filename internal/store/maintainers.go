package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Maintainer is one row in module_maintainers.
type Maintainer struct {
	Module    string    `json:"module"`
	UserEmail string    `json:"user_email"`
	GrantedAt time.Time `json:"granted_at"`
	GrantedBy string    `json:"granted_by"`
}

// AddMaintainer grants userEmail as a maintainer of module. The
// grantedBy field records who issued the grant (audit trail).
//
// Idempotent: re-granting the same (module, email) is a no-op and
// LEAVES the original granted_at + granted_by unchanged. Operators
// re-running a grant script don't need to track which grants
// already happened.
func (s *Store) AddMaintainer(ctx context.Context, module, userEmail, grantedBy string) error {
	if module == "" {
		return errors.New("store: AddMaintainer: module required")
	}
	if userEmail == "" {
		return errors.New("store: AddMaintainer: user_email required")
	}
	if grantedBy == "" {
		return errors.New("store: AddMaintainer: granted_by required")
	}
	now := time.Now().UTC().Format(auditTimestampLayout)
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO module_maintainers
		    (module, user_email, granted_at, granted_by)
		VALUES (?, ?, ?, ?)
	`, module, userEmail, now, grantedBy)
	if err != nil {
		return fmt.Errorf("store: insert maintainer: %w", err)
	}
	return nil
}

// RemoveMaintainer revokes userEmail's grant on module. No-op when
// no such grant exists (idempotent).
func (s *Store) RemoveMaintainer(ctx context.Context, module, userEmail string) error {
	if module == "" {
		return errors.New("store: RemoveMaintainer: module required")
	}
	if userEmail == "" {
		return errors.New("store: RemoveMaintainer: user_email required")
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM module_maintainers WHERE module = ? AND user_email = ?
	`, module, userEmail)
	if err != nil {
		return fmt.Errorf("store: delete maintainer: %w", err)
	}
	return nil
}

// IsMaintainer reports whether userEmail currently holds a grant on
// module. Pure SQL probe — no caching at this layer (the policy
// Evaluator wraps this with a TTL'd cache).
func (s *Store) IsMaintainer(ctx context.Context, module, userEmail string) (bool, error) {
	if module == "" || userEmail == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM module_maintainers
		WHERE module = ? AND user_email = ? LIMIT 1
	`, module, userEmail).Scan(&n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: probe maintainer: %w", err)
	}
	return true, nil
}

// ListMaintainers returns every grant for module in granted_at
// ascending order (oldest grant first — useful for "show me the
// original owner" UIs).
func (s *Store) ListMaintainers(ctx context.Context, module string) ([]Maintainer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT module, user_email, granted_at, granted_by
		FROM module_maintainers
		WHERE module = ?
		ORDER BY granted_at ASC
	`, module)
	if err != nil {
		return nil, fmt.Errorf("store: list maintainers: %w", err)
	}
	defer rows.Close()
	var out []Maintainer
	for rows.Next() {
		var (
			m         Maintainer
			grantedAt string
		)
		if err := rows.Scan(&m.Module, &m.UserEmail, &grantedAt, &m.GrantedBy); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, grantedAt); err == nil {
			m.GrantedAt = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
