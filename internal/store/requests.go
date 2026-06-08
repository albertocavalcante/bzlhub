package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RequestState is the phase a procurement request is in. See
// docs/plans/67-procurement.md for the canonical state diagram and
// docs/plans/72-execution-roadmap-v0-corp-bzlhub.md §C4 for the
// build plan. The transition graph itself is encoded in
// RequestState.CanTransitionTo.
type RequestState string

const (
	// RequestStatePending — newly submitted, awaiting preflight.
	RequestStatePending RequestState = "pending"
	// RequestStatePreflighting — preflight checks in progress.
	RequestStatePreflighting RequestState = "preflighting"
	// RequestStateAutoPass — preflight cleared every hard gate;
	// advancing automatically without human review.
	RequestStateAutoPass RequestState = "auto_pass"
	// RequestStateNeedsReview — preflight wants human eyes
	// (unknown license, network-fetch-unpinned, etc.).
	RequestStateNeedsReview RequestState = "needs_review"
	// RequestStateApproved — a reviewer with policy.approve_request
	// said yes.
	RequestStateApproved RequestState = "approved"
	// RequestStateFetching — canopy is fetching the source archive.
	RequestStateFetching RequestState = "fetching"
	// RequestStateIndexed — terminal success; module + version is
	// in the registry mirror.
	RequestStateIndexed RequestState = "indexed"
	// RequestStateDenied — terminal failure; preflight or a
	// reviewer said no (or fetch failed irrecoverably).
	RequestStateDenied RequestState = "denied"
)

// legalTransitions maps from-state to the set of allowed to-states
// per Plan 67. Anything not in the inner map is rejected by
// CanTransitionTo (and by TransitionRequest at the SQL layer).
var legalTransitions = map[RequestState]map[RequestState]bool{
	RequestStatePending: {
		RequestStatePreflighting: true,
		// Manual early-deny: a reviewer browsing the queue can deny
		// an obviously-bad request without waiting for preflight to
		// pick it up. Plan 67's diagram shows the preflight path as
		// the common case; this edge is the human-in-the-loop
		// escape hatch.
		RequestStateDenied: true,
	},
	RequestStatePreflighting: {
		RequestStateAutoPass:    true,
		RequestStateNeedsReview: true,
		RequestStateDenied:      true,
	},
	RequestStateNeedsReview: {
		RequestStateApproved: true,
		RequestStateDenied:   true,
	},
	RequestStateAutoPass: {
		RequestStateFetching: true,
	},
	RequestStateApproved: {
		RequestStateFetching: true,
	},
	RequestStateFetching: {
		RequestStateIndexed: true,
		RequestStateDenied:  true,
	},
	// indexed + denied are terminal — no outgoing edges.
}

// CanTransitionTo reports whether s → to is a legal transition.
// Terminal states (indexed, denied) reject every transition.
func (s RequestState) CanTransitionTo(to RequestState) bool {
	allowed, ok := legalTransitions[s]
	if !ok {
		return false
	}
	return allowed[to]
}

// IsTerminal reports whether s has no outgoing transitions.
// indexed (success) and denied (failure) are the terminals.
func (s RequestState) IsTerminal() bool {
	_, ok := legalTransitions[s]
	return !ok
}

// Request is one procurement request row. Built by handlers from
// the POST /api/v1/requests body, persisted via CreateRequest,
// stepped through states by the preflight runner + reviewer
// actions via TransitionRequest.
type Request struct {
	ID              int64           `json:"id"`
	SubmitterSub    string          `json:"submitter_sub"`
	SubmitterEmail  string          `json:"submitter_email,omitempty"`
	AuthMethod      string          `json:"auth_method"`
	Module          string          `json:"module"`
	Version         string          `json:"version"`
	SourceURL       string          `json:"source_url,omitempty"`
	SubmitterNotes  string          `json:"submitter_notes,omitempty"`
	State           RequestState    `json:"state"`
	StateChangedAt  time.Time       `json:"state_changed_at"`
	CreatedAt       time.Time       `json:"created_at"`
	PreflightJSON   json.RawMessage `json:"preflight_json,omitempty"`
	DenialReason    string          `json:"denial_reason,omitempty"`
	FetchedSHA      string          `json:"fetched_sha,omitempty"`
	CommittedSHA    string          `json:"committed_sha,omitempty"`
	RetryCount      int             `json:"retry_count"`
}

// RequestQuery filters a ListRequests call. Zero values disable
// the matching filter dimension.
type RequestQuery struct {
	States    []RequestState // any-of match
	Submitter string         // exact match on submitter_sub
	Limit     int            // 0 = no limit (capped at 500 server-side)
}

// RequestFields carries optional column updates applied as part of
// TransitionRequest. Nil/empty fields are NOT written — partial
// updates are intentional so callers don't accidentally clobber
// preflight_json on a later transition that doesn't have it.
type RequestFields struct {
	PreflightJSON json.RawMessage
	DenialReason  string
	FetchedSHA    string
	CommittedSHA  string
	RetryCount    *int // pointer so 0 can be explicit
}

// ErrRequestNotFound is returned by GetRequest when id doesn't
// exist. Distinct error so handlers can return 404 cleanly.
var ErrRequestNotFound = errors.New("store: request not found")

// ErrIllegalTransition is returned by TransitionRequest when the
// from-state → to-state pair isn't in the legal-transitions graph.
// Application bug — never a user-input problem.
var ErrIllegalTransition = errors.New("store: illegal state transition")

// ErrStateMismatch is returned by TransitionRequest when the SQL
// optimistic-concurrency CAS finds the row's state isn't the
// expected from-state. Indicates another worker raced ahead;
// callers should re-read and decide whether to retry.
var ErrStateMismatch = errors.New("store: request state mismatch (concurrent transition)")

// CreateRequest inserts a new request in state=pending. Returns
// the auto-generated ID.
//
// Required fields: SubmitterSub, AuthMethod, Module, Version. All
// other fields are optional and may be empty.
func (s *Store) CreateRequest(ctx context.Context, r Request) (int64, error) {
	r.SubmitterSub = strings.TrimSpace(r.SubmitterSub)
	r.Module = strings.TrimSpace(r.Module)
	r.Version = strings.TrimSpace(r.Version)
	r.AuthMethod = strings.TrimSpace(r.AuthMethod)
	if r.SubmitterSub == "" {
		return 0, errors.New("store: CreateRequest: submitter_sub required")
	}
	if r.AuthMethod == "" {
		return 0, errors.New("store: CreateRequest: auth_method required")
	}
	if r.Module == "" {
		return 0, errors.New("store: CreateRequest: module required")
	}
	if r.Version == "" {
		return 0, errors.New("store: CreateRequest: version required")
	}
	now := time.Now().UTC().Format(auditTimestampLayout)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO requests
		    (submitter_sub, submitter_email, auth_method, module, version,
		     source_url, submitter_notes, state, state_changed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		r.SubmitterSub,
		nullableString(r.SubmitterEmail),
		r.AuthMethod,
		r.Module, r.Version,
		nullableString(r.SourceURL),
		nullableString(r.SubmitterNotes),
		string(RequestStatePending),
		now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: LastInsertId: %w", err)
	}
	return id, nil
}

// GetRequest fetches one request by ID. Returns ErrRequestNotFound
// when no row matches.
func (s *Store) GetRequest(ctx context.Context, id int64) (*Request, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, submitter_sub, submitter_email, auth_method, module, version,
		       source_url, submitter_notes, state, state_changed_at, created_at,
		       preflight_json, denial_reason, fetched_sha, committed_sha,
		       retry_count
		FROM requests WHERE id = ?
	`, id)
	r, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, err
	}
	return r, nil
}

// ListRequests returns requests matching q, newest first.
//
// Limit defaults to all rows when q.Limit ≤ 0, capped at 500 to
// keep the JSON response bounded — UI pagination layers on top.
func (s *Store) ListRequests(ctx context.Context, q RequestQuery) ([]*Request, error) {
	const maxLimit = 500
	limit := q.Limit
	if limit <= 0 || limit > maxLimit {
		limit = maxLimit
	}

	var where []string
	var args []any
	if len(q.States) > 0 {
		placeholders := make([]string, len(q.States))
		for i, st := range q.States {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ",")+")")
	}
	if q.Submitter != "" {
		where = append(where, "submitter_sub = ?")
		args = append(args, q.Submitter)
	}
	sqlStr := `
		SELECT id, submitter_sub, submitter_email, auth_method, module, version,
		       source_url, submitter_notes, state, state_changed_at, created_at,
		       preflight_json, denial_reason, fetched_sha, committed_sha,
		       retry_count
		FROM requests
	`
	if len(where) > 0 {
		sqlStr += "WHERE " + strings.Join(where, " AND ") + " "
	}
	sqlStr += "ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list requests: %w", err)
	}
	defer rows.Close()
	var out []*Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindOpenRequest returns the most recent non-terminal request for
// (module, version), or ErrRequestNotFound when there is none.
// Used by submit handlers to collapse concurrent same-module
// submissions into the existing request thread per Plan 67 (no
// DB UNIQUE — app-level dedup keeps the user-facing error story
// clean and idempotent).
func (s *Store) FindOpenRequest(ctx context.Context, module, version string) (*Request, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, submitter_sub, submitter_email, auth_method, module, version,
		       source_url, submitter_notes, state, state_changed_at, created_at,
		       preflight_json, denial_reason, fetched_sha, committed_sha,
		       retry_count
		FROM requests
		WHERE module = ? AND version = ?
		  AND state NOT IN (?, ?)
		ORDER BY id DESC LIMIT 1
	`, module, version, string(RequestStateIndexed), string(RequestStateDenied))
	r, err := scanRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, err
	}
	return r, nil
}

// AnyRequestFor reports whether ANY request exists for (module,
// version), including terminal ones. Used by `bzlhub seed` to make
// re-runs idempotent without re-submitting requests that previously
// reached indexed or denied.
func (s *Store) AnyRequestFor(ctx context.Context, module, version string) (bool, error) {
	if module == "" || version == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM requests WHERE module = ? AND version = ? LIMIT 1
	`, module, version).Scan(&n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("store: probe AnyRequestFor: %w", err)
	}
	return true, nil
}

// TransitionRequest atomically moves request id from fromState to
// toState, optionally writing fields. Verifies the transition is
// legal per the state-machine graph BEFORE touching the database.
//
// Optimistic-concurrency: the UPDATE includes `WHERE state =
// fromState` so two workers racing to move the same request both
// pass the legal-transition check but only the first wins. The
// loser gets ErrStateMismatch and can re-read the row.
//
// Returns nil on success, ErrIllegalTransition on a bad transition
// pair, ErrStateMismatch when CAS fails, or wrapped sql errors.
func (s *Store) TransitionRequest(ctx context.Context, id int64, fromState, toState RequestState, fields *RequestFields) error {
	if !fromState.CanTransitionTo(toState) {
		return fmt.Errorf("%w: %s → %s", ErrIllegalTransition, fromState, toState)
	}
	now := time.Now().UTC().Format(auditTimestampLayout)
	setClauses := []string{"state = ?", "state_changed_at = ?"}
	args := []any{string(toState), now}
	if fields != nil {
		if len(fields.PreflightJSON) > 0 {
			setClauses = append(setClauses, "preflight_json = ?")
			args = append(args, string(fields.PreflightJSON))
		}
		if fields.DenialReason != "" {
			setClauses = append(setClauses, "denial_reason = ?")
			args = append(args, fields.DenialReason)
		}
		if fields.FetchedSHA != "" {
			setClauses = append(setClauses, "fetched_sha = ?")
			args = append(args, fields.FetchedSHA)
		}
		if fields.CommittedSHA != "" {
			setClauses = append(setClauses, "committed_sha = ?")
			args = append(args, fields.CommittedSHA)
		}
		if fields.RetryCount != nil {
			setClauses = append(setClauses, "retry_count = ?")
			args = append(args, *fields.RetryCount)
		}
	}
	args = append(args, id, string(fromState))
	res, err := s.db.ExecContext(ctx, `
		UPDATE requests SET `+strings.Join(setClauses, ", ")+`
		WHERE id = ? AND state = ?
	`, args...)
	if err != nil {
		return fmt.Errorf("store: transition request: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: RowsAffected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w (id=%d, expected=%s)", ErrStateMismatch, id, fromState)
	}
	return nil
}

// CountOpenRequestsForUser returns how many non-terminal requests
// submitterSub currently owns. "Non-terminal" means the row is NOT in
// indexed or denied — every other state (pending / preflighting /
// needs_review / auto_pass / approved / fetching) counts.
//
// Backs the Plan 76 §2.5 max_pending_per_user pool cap: the handler
// rejects a new submit when this count is already at the configured
// cap. Cheap query — submitter_sub has an index per the schema, and
// the WHERE NOT IN list is small.
func (s *Store) CountOpenRequestsForUser(ctx context.Context, submitterSub string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM requests
		WHERE submitter_sub = ? AND state NOT IN ('indexed', 'denied')
	`, submitterSub).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count open requests for user: %w", err)
	}
	return n, nil
}

// ReclaimStuckFetching resets requests stuck in state=fetching whose
// state_changed_at is before `before` back to state=approved so the
// admit loop picks them up on its next poll cycle.
//
// Recovery path for the crash-mid-retry edge documented in Plan 76 §2.3:
// if canopy dies while a request is mid-fetch (mid-backoff or mid-attempt),
// the row sits in fetching forever — workerLoop only picks up auto_pass +
// approved. Calling this at boot reclaims those rows.
//
// Returns the number of rows reclaimed. Bypasses the legalTransitions
// graph deliberately: fetching → approved is a recovery edge, semantically
// equivalent to "we never started this attempt, please retry from scratch."
// retry_count is preserved so the admit runner's retry budget continues
// from where it left off.
//
// Callers MUST pick a `before` threshold larger than the worst-case
// in-flight admit attempt — otherwise the sweep races active workers
// and steals their rows. 5 minutes is the recommended default (covers
// the 5-minute backoff cap + a comfortable margin for the fetch itself).
func (s *Store) ReclaimStuckFetching(ctx context.Context, before time.Time) (int, error) {
	now := time.Now().UTC().Format(auditTimestampLayout)
	cutoff := before.UTC().Format(auditTimestampLayout)
	res, err := s.db.ExecContext(ctx, `
		UPDATE requests
		SET state = ?, state_changed_at = ?
		WHERE state = ? AND state_changed_at < ?
	`,
		string(RequestStateApproved), now,
		string(RequestStateFetching), cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("store: reclaim stuck fetching: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: RowsAffected: %w", err)
	}
	return int(n), nil
}

// scanRequest reads one row into a *Request. Works against both
// *sql.Row and *sql.Rows via the Scan interface.
func scanRequest(row interface {
	Scan(dest ...any) error
}) (*Request, error) {
	var (
		r              Request
		submitterEmail sql.NullString
		sourceURL      sql.NullString
		notes          sql.NullString
		preflight      sql.NullString
		denial         sql.NullString
		fetchedSHA     sql.NullString
		committedSHA   sql.NullString
		stateChanged   string
		createdAt      string
	)
	err := row.Scan(
		&r.ID, &r.SubmitterSub, &submitterEmail, &r.AuthMethod,
		&r.Module, &r.Version,
		&sourceURL, &notes,
		&r.State,
		&stateChanged, &createdAt,
		&preflight, &denial, &fetchedSHA, &committedSHA,
		&r.RetryCount,
	)
	if err != nil {
		return nil, err
	}
	if submitterEmail.Valid {
		r.SubmitterEmail = submitterEmail.String
	}
	if sourceURL.Valid {
		r.SourceURL = sourceURL.String
	}
	if notes.Valid {
		r.SubmitterNotes = notes.String
	}
	if preflight.Valid {
		r.PreflightJSON = json.RawMessage(preflight.String)
	}
	if denial.Valid {
		r.DenialReason = denial.String
	}
	if fetchedSHA.Valid {
		r.FetchedSHA = fetchedSHA.String
	}
	if committedSHA.Valid {
		r.CommittedSHA = committedSHA.String
	}
	if t, err := time.Parse(time.RFC3339Nano, stateChanged); err == nil {
		r.StateChangedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		r.CreatedAt = t
	}
	return &r, nil
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
