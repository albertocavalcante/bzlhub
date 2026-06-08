package bzlhub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/drift"
)

// BackfillDriftSummary is the boot-time reconcile seam for the
// versions.drift_summary_json column. When a Mirror is wired, it
// computes drift for rows still in the default Unknown state by
// comparing each local version against the upstream metadata.json
// at the Mirror's HEAD; populated rows are skipped (refresh
// re-warms but never overwrites — call RefreshDriftSummary for
// that). Returns the count of rows written. Per-row errors are
// non-fatal; only ListAllVersions failure propagates.
//
// Without a Mirror the function logs the unknown-row count and
// returns zero. The HTTP-probe `bzlhub drift` CLI is the
// alternative for File-backed installs.
func (s *Service) BackfillDriftSummary(ctx context.Context) (int, error) {
	if s.mirror == nil {
		var unknown int
		for r, err := range s.store.AllVersionsWithDrift(ctx) {
			if err != nil {
				return 0, err
			}
			if isDriftUnknown(r.DriftRaw) {
				unknown++
			}
		}
		if unknown > 0 {
			slog.Info("drift summary backfill: rows pending compute",
				"unknown_count", unknown)
		}
		return 0, nil
	}

	now := time.Now().UTC()
	upstreamSHA, _ := s.mirror.SnapshotSHA(ctx)
	syncedAt := s.mirror.LastSync()
	upstreams := newUpstreamCache(s.mirror)
	var written int
	for r, err := range s.store.AllVersionsWithDrift(ctx) {
		if err != nil {
			return written, err
		}
		if !isDriftUnknown(r.DriftRaw) {
			continue
		}

		up, err := upstreams.lookup(ctx, r.Module)
		if err != nil && !errors.Is(err, bcrmirror.ErrModuleNotFound) {
			slog.Debug("drift backfill: upstream lookup failed",
				"module", r.Module, "err", err)
			continue
		}

		summary := driftSummaryFromVerdict(drift.ComputeForVersion(r.Version, up), now, upstreamSHA, syncedAt)
		if err := writeDriftSummary(ctx, s, r.Module, r.Version, summary); err != nil {
			slog.Debug("drift backfill: set failed",
				"module", r.Module, "version", r.Version, "err", err)
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

// isDriftUnknown reports whether a drift_summary_json blob is the
// default empty / Unknown state. Treats parse failures as Unknown
// — one corrupt row shouldn't be silently treated as populated.
func isDriftUnknown(raw []byte) bool {
	if len(raw) == 0 || string(raw) == "{}" {
		return true
	}
	var d api.DriftSummary
	if err := json.Unmarshal(raw, &d); err != nil {
		return true
	}
	return d.Status == "" || d.Status == api.DriftStatusUnknown
}

// driftSummaryFromVerdict bundles the staleness stamps + the
// per-version drift verdict into the persisted shape.
func driftSummaryFromVerdict(v drift.VersionDrift, now time.Time, upstreamSHA string, syncedAt time.Time) api.DriftSummary {
	return api.DriftSummary{
		Status:         api.DriftStatus(v.Status),
		Behind:         v.Behind,
		LatestUpstream: v.LatestUpstream,
		ComputedAt:     now,
		UpstreamSHA:    upstreamSHA,
		SyncedAt:       syncedAt,
	}
}

// writeDriftSummary marshals + persists a DriftSummary for one row.
func writeDriftSummary(ctx context.Context, s *Service, module, version string, summary api.DriftSummary) error {
	encoded, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return s.store.SetDriftSummary(ctx, module, version, encoded)
}

// upstreamCache lives in upstream_cache.go.
