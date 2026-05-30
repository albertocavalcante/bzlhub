package canopy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/drift"
)

// ErrNoMirrorForDrift is returned by RefreshDriftSummary when the
// Service has no git-aware Mirror attached. Operators on a File-
// backed install (no <root>/.git) can't refresh drift via this
// path; they fall back to `canopy drift` (HTTP-probe).
//
// Distinguishes the silent no-op (BackfillDriftSummary path, which
// logs and returns) from the explicit operator request (this verb)
// — the latter must surface a clear error so the CLI can render a
// useful hint rather than appearing to succeed.
var ErrNoMirrorForDrift = errors.New("canopy: drift refresh requires a git-aware mirror (run `canopy sync bootstrap` first, then `canopy serve --root` against a .git-rooted directory)")

// RefreshDriftSummary recomputes per-(module, version) drift
// verdicts for EVERY row in the index, overwriting any prior
// payload. It mirrors BackfillDriftSummary's git-aware compute but
// drops the "preserves populated rows" guard.
//
// Use cases:
//
//   - Operator just ran `canopy sync bootstrap` and wants the
//     drift chips populated without restarting a long-running
//     `canopy serve`. Backfill only runs at serve boot; Refresh
//     is the explicit between-boots path.
//   - Plan 21 B4's sync-runner will call this (or its successor)
//     after every successful bcrmirror.Sync, with the bonus
//     filter "only modules whose metadata changed in the new
//     commits."
//
// Returns ErrNoMirrorForDrift when no Mirror is attached; otherwise
// returns the count of rows whose drift cache was rewritten.
func (s *Service) RefreshDriftSummary(ctx context.Context) (int, error) {
	if s.mirror == nil {
		return 0, ErrNoMirrorForDrift
	}

	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	upstreamSHA, _ := s.mirror.SnapshotSHA(ctx) // best-effort; empty SHA is fine
	syncedAt := s.mirror.LastSync()
	upstreams := newUpstreamCache(s.mirror)
	var written int
	for _, mv := range rows {
		up, err := upstreams.lookup(ctx, mv.Module)
		if err != nil && !errors.Is(err, bcrmirror.ErrModuleNotFound) {
			slog.Debug("drift refresh: upstream lookup failed",
				"module", mv.Module, "err", err)
			continue
		}

		verdict := drift.ComputeForVersion(mv.Version, up)
		summary := api.DriftSummary{
			Status:         api.DriftStatus(verdict.Status),
			Behind:         verdict.Behind,
			LatestUpstream: verdict.LatestUpstream,
			ComputedAt:     now,
			UpstreamSHA:    upstreamSHA,
			SyncedAt:       syncedAt,
		}
		encoded, err := json.Marshal(summary)
		if err != nil {
			continue
		}
		if err := s.store.SetDriftSummary(ctx, mv.Module, mv.Version, encoded); err != nil {
			slog.Debug("drift refresh: set failed",
				"module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		written++
	}

	if written > 0 {
		slog.Info("drift summary refresh: rows rewritten",
			"count", written)
	}
	return written, nil
}
