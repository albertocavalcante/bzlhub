package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/albertocavalcante/canopy/internal/githubmeta"
)

// GitHubMetaRow is the persisted form of githubmeta.Meta plus refresh
// bookkeeping (the last terminal HTTP status the refresher saw).
type GitHubMetaRow struct {
	Module     string
	Meta       githubmeta.Meta
	HTTPStatus int
}

// UpsertGitHubMeta records the result of a refresh for one module.
// status is the terminal HTTP code the refresher observed (200 / 304
// / 404 / 429). For 304, callers should pass a Meta with the prior
// ETag preserved and FetchedAt updated to "now"; only flat columns
// + fetched_at are updated (meta_json stays as the last 200 body).
func (s *Store) UpsertGitHubMeta(ctx context.Context, module string, m githubmeta.Meta, status int) error {
	if module == "" {
		return errors.New("UpsertGitHubMeta: empty module")
	}
	if m.FetchedAt.IsZero() {
		m.FetchedAt = time.Now().UTC()
	}
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal github meta: %w", err)
	}
	// 304: keep meta_json as-is from the previous 200, refresh only
	// the bookkeeping columns. We do this by UPDATEing in place when
	// a row exists; ON CONFLICT below handles the fresh-row case.
	if status == 304 {
		res, err := s.db.ExecContext(ctx, `
			UPDATE module_github_meta
			   SET fetched_at = ?, http_status = ?, etag = ?
			 WHERE module_name = ?
		`, m.FetchedAt.UTC().Format(time.RFC3339Nano), status, m.ETag, module)
		if err != nil {
			return fmt.Errorf("update github_meta (304): %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		// Row didn't exist (shouldn't happen for a 304, but fall
		// through to insert defensively rather than silently dropping
		// the refresh state).
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO module_github_meta
		    (module_name, owner, repo, stars, forks, watchers,
		     primary_language, etag, http_status, fetched_at, meta_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(module_name) DO UPDATE SET
		    owner            = excluded.owner,
		    repo             = excluded.repo,
		    stars            = excluded.stars,
		    forks            = excluded.forks,
		    watchers         = excluded.watchers,
		    primary_language = excluded.primary_language,
		    etag             = excluded.etag,
		    http_status      = excluded.http_status,
		    fetched_at       = excluded.fetched_at,
		    meta_json        = excluded.meta_json
	`,
		module, m.Owner, m.Repo, m.Stars, m.Forks, m.Watchers,
		m.PrimaryLanguage, m.ETag, status,
		m.FetchedAt.UTC().Format(time.RFC3339Nano), string(body),
	)
	if err != nil {
		return fmt.Errorf("upsert github_meta: %w", err)
	}
	return nil
}

// GetGitHubMeta returns the stored meta for a module. Returns
// (nil, nil) when there's no row (refresher hasn't seen this module
// yet) — the absence is not an error.
func (s *Store) GetGitHubMeta(ctx context.Context, module string) (*GitHubMetaRow, error) {
	var (
		out      GitHubMetaRow
		metaJSON string
		fetched  string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT module_name, http_status, fetched_at, meta_json
		  FROM module_github_meta
		 WHERE module_name = ?
	`, module).Scan(&out.Module, &out.HTTPStatus, &fetched, &metaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query github_meta: %w", err)
	}
	if err := json.Unmarshal([]byte(metaJSON), &out.Meta); err != nil {
		return nil, fmt.Errorf("unmarshal github_meta: %w", err)
	}
	if t, perr := time.Parse(time.RFC3339Nano, fetched); perr == nil {
		out.Meta.FetchedAt = t
	}
	return &out, nil
}

// GitHubMetaCandidate is one row produced by ListGitHubMetaCandidates
// — the bookkeeping the refresher needs to decide whether to re-fetch
// a given module.
type GitHubMetaCandidate struct {
	Module    string
	ETag      string
	FetchedAt time.Time
	// HasRow is false when there's no module_github_meta row for this
	// module yet (refresher should fetch unconditionally). When true,
	// the refresher should send If-None-Match using ETag.
	HasRow bool
}

// ListGitHubMetaCandidates returns one entry per indexed module,
// joined against module_github_meta so the refresher can sort by
// staleness in a single scan. Modules with no row yet appear with
// HasRow=false and a zero FetchedAt — the refresher should treat
// these as the highest-priority work.
func (s *Store) ListGitHubMetaCandidates(ctx context.Context) ([]GitHubMetaCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.name,
		       COALESCE(gm.etag, ''),
		       COALESCE(gm.fetched_at, ''),
		       CASE WHEN gm.module_name IS NULL THEN 0 ELSE 1 END
		  FROM modules m
		  LEFT JOIN module_github_meta gm ON gm.module_name = m.name
		 ORDER BY gm.fetched_at ASC NULLS FIRST, m.name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list github_meta candidates: %w", err)
	}
	defer rows.Close()
	var out []GitHubMetaCandidate
	for rows.Next() {
		var c GitHubMetaCandidate
		var fetched string
		var has int
		if err := rows.Scan(&c.Module, &c.ETag, &fetched, &has); err != nil {
			return nil, err
		}
		c.HasRow = has == 1
		if t, perr := time.Parse(time.RFC3339Nano, fetched); perr == nil {
			c.FetchedAt = t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
