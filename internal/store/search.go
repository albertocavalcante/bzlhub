package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/bzlhub/internal/api"
)

// Search runs a full-text query with optional hermeticity filtering.
// Hits are ordered by FTS rank (BM25), limit applied.
func (s *Store) Search(ctx context.Context, q api.Query) (*api.SearchResults, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return &api.SearchResults{}, nil
	}

	// FTS5 MATCH expression. Trigram tokenizer makes simple substring queries
	// work without quotation gymnastics.
	args := []any{text}
	// LEFT JOIN versions so the projection always returns a row even
	// when an fts hit's underlying (module, version) is missing from
	// versions — coalesce the flag to 0 in that case. Concretely this
	// only matters during a partial / mid-ingest state; once the row
	// exists, the cached has_source_index column gives a cheap O(1)
	// per-hit answer for the "show Code → link?" UI gate.
	query := `
		SELECT m.module_name, m.version, m.kind, m.name, m.file,
		       snippet(fts_text, 0, '[', ']', '…', 16) AS snip,
		       COALESCE(v.has_source_index, 0) AS has_source_index
		FROM fts_text
		JOIN fts_meta m ON m.rowid = fts_text.rowid
		LEFT JOIN versions v
		       ON v.module_name = m.module_name AND v.version = m.version
		WHERE fts_text MATCH ?`
	if len(q.Hermeticity) > 0 {
		placeholders := make([]string, len(q.Hermeticity))
		for i, c := range q.Hermeticity {
			placeholders[i] = "?"
			args = append(args, string(c))
		}
		query += fmt.Sprintf(`
			AND EXISTS (
				SELECT 1 FROM hermeticity_classes hc
				WHERE hc.module_name = m.module_name
				  AND hc.version = m.version
				  AND hc.class IN (%s)
			)`, strings.Join(placeholders, ","))
	}
	query += `
		ORDER BY rank
		LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	results := &api.SearchResults{}
	for rows.Next() {
		var h api.Hit
		var snippet sql.NullString
		var hasSourceIndex int
		if err := rows.Scan(&h.Module, &h.Version, &h.MatchKind, &h.MatchName, &h.File, &snippet, &hasSourceIndex); err != nil {
			return nil, err
		}
		if snippet.Valid {
			h.Snippet = snippet.String
		}
		h.HasSourceIndex = hasSourceIndex != 0
		h.Hermeticity = s.classesFor(ctx, h.Module, h.Version)
		results.Hits = append(results.Hits, h)
	}
	results.Total = len(results.Hits)
	return results, rows.Err()
}

func (s *Store) classesFor(ctx context.Context, module, version string) []report.HermeticityClass {
	rows, err := s.db.QueryContext(ctx,
		`SELECT class FROM hermeticity_classes WHERE module_name = ? AND version = ?`,
		module, version)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []report.HermeticityClass
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err == nil {
			out = append(out, report.HermeticityClass(c))
		}
	}
	return out
}

// VersionExists is a cheap "does this (module, version) have a row?"
// probe. Used by airgap emitters that template output unconditionally
// — without this guard a typo in the URL silently produces a 200 with
// a snippet for a non-existent module.
func (s *Store) VersionExists(ctx context.Context, name, version string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM versions WHERE module_name = ? AND version = ? LIMIT 1`,
		name, version).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
