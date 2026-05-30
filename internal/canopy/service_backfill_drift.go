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
	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/store"
)

// BackfillDriftSummary is the boot-time reconcile seam for the
// versions.drift_summary_json column. PR7 wires the git-aware path:
// when the Service has a Mirror attached (UseMirror), the function
// walks rows whose drift is still the default "unknown" state and
// computes a per-(module, version) DriftSummary by comparing the
// local version against the upstream metadata.json read from the
// Mirror at HEAD.
//
// The walk:
//
//   - Skips rows that already carry a populated DriftSummary
//     (Plan 28 cut point #1: refresh re-warms, it does not
//     overwrite — operator-supplied verdicts and sync-runner
//     writes survive boot).
//   - Caches each module's parsed upstream metadata once per
//     module so the per-version loop doesn't re-read the same
//     bytes from disk per row.
//   - Translates bcrmirror.ErrModuleNotFound to "local-only"
//     status (the module isn't present upstream).
//   - Times the upstream lookup with ComputedAt, stamped once per
//     boot so all rows written in this pass share a coherent
//     timestamp.
//
// When the Service has no Mirror (the *backend.File path), the
// function logs the unknown-row count for operator visibility and
// returns zero without writing. Those installs fall back to the
// `canopy drift` CLI verb (HTTP-probe path) for on-demand drift.
//
// The returned int is the number of rows whose drift cache was
// updated. Errors on individual rows are non-fatal — the boot
// reconcile must not abort over one corrupt row — and are logged at
// DEBUG. The function returns an error only on store-wide failures
// (the initial ListAllVersions read).
//
// Plan-trail:
//
//   - M1 (Plan 28): pure log walk, this seam was a no-op.
//   - M2 PR7 (Plan 20 A4): bcrmirror-backed writer below.
//   - Plan 21 B4 (sync-runner): the candidate→trusted promotion
//     pipeline writes a layered DriftSummary including UpstreamSHA,
//     SyncedAt, PromotedAt fields without re-wiring this seam.
func (s *Service) BackfillDriftSummary(ctx context.Context) (int, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return 0, err
	}

	if s.mirror == nil {
		return logUnknownCount(ctx, s, rows), nil
	}

	now := time.Now().UTC()
	upstreamSHA, _ := s.mirror.SnapshotSHA(ctx)
	syncedAt := s.mirror.LastSync()
	upstreams := newUpstreamCache(s.mirror)
	var written int
	for _, mv := range rows {
		payload, err := s.store.GetDriftSummary(ctx, mv.Module, mv.Version)
		if err != nil {
			continue
		}
		var existing api.DriftSummary
		if err := json.Unmarshal(payload, &existing); err != nil {
			continue
		}
		if existing.Status != "" && existing.Status != api.DriftStatusUnknown {
			continue
		}

		up, err := upstreams.lookup(ctx, mv.Module)
		if err != nil && !errors.Is(err, bcrmirror.ErrModuleNotFound) {
			slog.Debug("drift backfill: upstream lookup failed",
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
			slog.Debug("drift backfill: set failed",
				"module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		written++
	}

	if written > 0 {
		slog.Info("drift summary backfill: rows written",
			"count", written)
	}
	return written, nil
}

// logUnknownCount preserves the M1 observability contract for File-
// backed installs (no Mirror wired). Counts default-state drift rows
// and surfaces them at INFO with the docs hint, so operators see
// "200 modules with unknown drift" on boot and have a clear path to
// enabling git-aware drift.
func logUnknownCount(ctx context.Context, s *Service, rows []store.ModuleVersion) int {
	var unknown int
	for _, mv := range rows {
		payload, err := s.store.GetDriftSummary(ctx, mv.Module, mv.Version)
		if err != nil {
			continue
		}
		var d api.DriftSummary
		if err := json.Unmarshal(payload, &d); err != nil {
			continue
		}
		if d.Status == "" || d.Status == api.DriftStatusUnknown {
			unknown++
		}
	}
	if unknown > 0 {
		slog.Info("drift summary backfill: rows pending compute",
			"unknown_count", unknown,
			"hint", "configure a drift source (see docs/plans/20-airgap-and-bcr-fork.md, plan 21)")
	}
	return 0
}

// upstreamCache memoises bcrmirror.MetadataAt + JSON decode per
// module across one backfill pass. The cache short-circuits both the
// "module exists upstream" + "module is local-only" outcomes so a
// repeat module name (every version of it) costs one map lookup.
type upstreamCache struct {
	mirror *bcrmirror.Mirror
	hits   map[string]upstreamCacheEntry
}

type upstreamCacheEntry struct {
	meta *fetch.MetadataJSON
	err  error
}

func newUpstreamCache(m *bcrmirror.Mirror) *upstreamCache {
	return &upstreamCache{
		mirror: m,
		hits:   map[string]upstreamCacheEntry{},
	}
}

func (c *upstreamCache) lookup(ctx context.Context, module string) (*fetch.MetadataJSON, error) {
	if hit, ok := c.hits[module]; ok {
		return hit.meta, hit.err
	}
	raw, err := c.mirror.MetadataAt(ctx, module, "HEAD")
	if err != nil {
		c.hits[module] = upstreamCacheEntry{err: err}
		return nil, err
	}
	var meta fetch.MetadataJSON
	if jerr := json.Unmarshal(raw, &meta); jerr != nil {
		c.hits[module] = upstreamCacheEntry{err: jerr}
		return nil, jerr
	}
	c.hits[module] = upstreamCacheEntry{meta: &meta}
	return &meta, nil
}
