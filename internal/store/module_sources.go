package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ModuleSourceKind discriminates how a (module, version) entry was
// served by canopy. Mirrors the SQL CHECK constraint on
// module_sources.source_kind in schema.sql.
type ModuleSourceKind string

const (
	// SourceLocal — served from the local --root mirror.
	SourceLocal ModuleSourceKind = "local"
	// SourceHTTPUpstream — served from an --upstream cascade probe.
	SourceHTTPUpstream ModuleSourceKind = "http-upstream"
	// SourceCollisionShadowed — present in this upstream but a
	// higher-priority source already served the request. Recorded
	// for audit; not served.
	SourceCollisionShadowed ModuleSourceKind = "collision-shadowed"
)

// LogModuleSource records one (module, version, source_url) audit
// row. INSERT OR IGNORE so repeated logs for the same row are
// no-ops at the DB layer — the cascade layer additionally
// deduplicates via an in-memory recently-seen map (Plan 16's
// "5-minute write-coalesce") to avoid even the wasted INSERT.
//
// Never returns the unique-constraint violation as an error;
// returns other DB errors verbatim.
func (s *Store) LogModuleSource(ctx context.Context, module, version, sourceURL string, kind ModuleSourceKind) error {
	if module == "" || version == "" || sourceURL == "" {
		return fmt.Errorf("LogModuleSource: module/version/sourceURL required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO module_sources
		    (module_name, version, source_url, source_kind, seen_at)
		VALUES (?, ?, ?, ?, ?)`,
		module, version, sourceURL, string(kind),
		time.Now().UTC().Format(auditTimestampLayout),
	)
	if err != nil {
		return fmt.Errorf("insert module_source: %w", err)
	}
	return nil
}

// ModuleCollision is one (module, version) pair with shadowed
// upstreams — i.e., at least one upstream was probed-as-200 but
// not served because a higher-priority source won. Surfaced by
// /api/v1/upstreams' collisions_sample field.
type ModuleCollision struct {
	Module     string   `json:"module"`
	Version    string   `json:"version"`
	ServedFrom string   `json:"served_from"`         // 'local' or upstream URL
	Shadowed   []string `json:"shadowed"`            // URLs of upstreams that ALSO had it
	LastSeen   string   `json:"last_seen,omitempty"` // RFC3339 — newest seen_at across the group
}

// GetCollisionsCount returns the number of distinct (module,
// version) pairs that have at least one collision-shadowed source.
// Cheap COUNT(DISTINCT); intended for the collisions_count field
// in the upstreams introspection response.
func (s *Store) GetCollisionsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT module_name || '@' || version)
		  FROM module_sources
		 WHERE source_kind = 'collision-shadowed'
	`).Scan(&n)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("count collisions: %w", err)
	}
	return n, nil
}

// GetCollisionsSample returns the most-recent N collision groups
// for the /api/v1/upstreams collisions_sample field. Plan 16 spec
// suggests up to 10 newest.
//
// Implementation: walk module_sources, find (m, v) pairs that have
// AT LEAST one collision-shadowed row, then assemble each group's
// served_from (the non-shadowed row) + shadowed list (all
// collision-shadowed rows) + last_seen (max seen_at across all
// rows in the group). Single query with self-join would be faster
// at scale but two queries are simpler and the federation hasn't
// hit a scale where that matters.
func (s *Store) GetCollisionsSample(ctx context.Context, limit int) ([]ModuleCollision, error) {
	if limit <= 0 {
		limit = 10
	}
	// Pick the newest N (m, v) pairs that have a shadow.
	rows, err := s.db.QueryContext(ctx, `
		SELECT module_name, version, MAX(seen_at) as latest
		  FROM module_sources
		 WHERE source_kind = 'collision-shadowed'
		 GROUP BY module_name, version
		 ORDER BY latest DESC
		 LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query collisions: %w", err)
	}
	defer rows.Close()
	type pair struct {
		module, version, latest string
	}
	var groups []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.module, &p.version, &p.latest); err != nil {
			return nil, fmt.Errorf("scan collision group: %w", err)
		}
		groups = append(groups, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return []ModuleCollision{}, nil
	}

	// For each group, fetch all source rows + assemble.
	out := make([]ModuleCollision, 0, len(groups))
	for _, g := range groups {
		srcRows, err := s.db.QueryContext(ctx, `
			SELECT source_url, source_kind
			  FROM module_sources
			 WHERE module_name = ? AND version = ?
		`, g.module, g.version)
		if err != nil {
			return nil, fmt.Errorf("query sources for %s@%s: %w", g.module, g.version, err)
		}
		entry := ModuleCollision{
			Module:   g.module,
			Version:  g.version,
			LastSeen: g.latest,
		}
		var shadowed []string
		var served string
		for srcRows.Next() {
			var url, kind string
			if err := srcRows.Scan(&url, &kind); err != nil {
				srcRows.Close()
				return nil, fmt.Errorf("scan source: %w", err)
			}
			switch kind {
			case string(SourceCollisionShadowed):
				shadowed = append(shadowed, url)
			case string(SourceLocal), string(SourceHTTPUpstream):
				// Whichever non-shadowed row exists is the served
				// source. There should be exactly one; if multiple,
				// the lex-first wins for determinism.
				if served == "" || url < served {
					served = url
				}
			}
		}
		srcRows.Close()
		// Stable order for deterministic JSON output.
		sortStrings(shadowed)
		entry.ServedFrom = served
		entry.Shadowed = shadowed
		out = append(out, entry)
	}
	return out, nil
}

// sortStrings is a tiny strings.Sort helper kept here to avoid
// adding a sort import just for the one call site.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && strings.Compare(s[j-1], s[j]) > 0; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
