package bzlhub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"

	"github.com/albertocavalcante/bzlhub/internal/drift"
)

// ErrNoMirrorForDrift is returned when drift recompute is invoked
// against a Service with no git-aware Mirror attached.
var ErrNoMirrorForDrift = errors.New("canopy: drift refresh requires a git-aware mirror")

// RefreshDriftSummary recomputes drift verdicts for every
// (module, version) row, overwriting prior payloads. Unlike
// BackfillDriftSummary it does not preserve populated rows.
// Returns the count of rows written.
func (s *Service) RefreshDriftSummary(ctx context.Context) (int, error) {
	if s.mirror == nil {
		return 0, ErrNoMirrorForDrift
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
		up, err := upstreams.lookup(ctx, r.Module)
		if err != nil && !errors.Is(err, bcrmirror.ErrModuleNotFound) {
			slog.Debug("drift refresh: upstream lookup failed",
				"module", r.Module, "err", err)
			continue
		}
		summary := driftSummaryFromVerdict(drift.ComputeForVersion(r.Version, up), now, upstreamSHA, syncedAt)
		if err := writeDriftSummary(ctx, s, r.Module, r.Version, summary); err != nil {
			slog.Debug("drift refresh: set failed",
				"module", r.Module, "version", r.Version, "err", err)
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
