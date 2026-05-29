package canopy

import (
	"context"
	"errors"
	"log/slog"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/store"
	"github.com/albertocavalcante/canopy/internal/verify"
)

// BackfillSourceIndexFlags reconciles versions.has_source_index for
// rows whose SCIP blobs predate the cached column. For each row
// where the cached flag is false but a SCIP blob exists with at
// least one indexed document, the flag is flipped to true and the
// row is counted as updated. Rows with no SCIP blob OR with an
// already-true flag are skipped cheaply.
//
// One-shot at boot. Cost is bounded by index size — one GetScipBlob
// + parse per false-flag row. ErrScipNotFound is the expected
// no-op signal (module has no SCIP), not an error.
//
// Returns the number of rows updated. Callers log it; partial
// failures are logged at WARN but don't abort the walk.
func (s *Service) BackfillSourceIndexFlags(ctx context.Context) (int, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return 0, err
	}
	var updated int
	for _, mv := range rows {
		// Cheap pre-check: skip rows where the flag is already true.
		// Eventual-consistent reads — a flipping flag during this
		// walk just means we either re-check (idempotent) or skip
		// (also fine, the next ingest will reconcile).
		has, err := s.store.GetHasSourceIndex(ctx, mv.Module, mv.Version)
		if err != nil {
			slog.Warn("backfill: read flag failed", "module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		if has {
			continue
		}
		blob, err := s.store.GetScipBlob(ctx, mv.Module, mv.Version)
		if err != nil {
			if errors.Is(err, store.ErrScipNotFound) {
				continue
			}
			slog.Warn("backfill: read blob failed", "module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		if !scipBlobHasFiles(blob) {
			continue
		}
		if err := s.store.SetHasSourceIndex(ctx, mv.Module, mv.Version, true); err != nil {
			slog.Warn("backfill: set flag failed", "module", mv.Module, "version", mv.Version, "err", err)
			continue
		}
		updated++
	}
	return updated, nil
}

// CollisionsCount + CollisionsSample expose Plan 16 Layer D state
// to the /api/v1/upstreams handler. Both delegate to the store
// layer's module_sources queries; the Service is the natural shim
// because the cmd-side cascade wiring already passes svc to the
// HTTP handler.
func (s *Service) CollisionsCount(ctx context.Context) (int, error) {
	return s.store.GetCollisionsCount(ctx)
}

func (s *Service) CollisionsSample(ctx context.Context, limit int) ([]api.ModuleCollisionInfo, error) {
	raw, err := s.store.GetCollisionsSample(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.ModuleCollisionInfo, 0, len(raw))
	for _, c := range raw {
		out = append(out, api.ModuleCollisionInfo{
			Module:     c.Module,
			Version:    c.Version,
			ServedFrom: c.ServedFrom,
			Shadowed:   c.Shadowed,
			LastSeen:   c.LastSeen,
		})
	}
	return out, nil
}

// Verify proxies to the verify package. Kept off api.Canopy because
// verify → store → api would close an import cycle; mcpsrv accepts
// a separate Verifier interface that Service also satisfies.
func (s *Service) Verify(ctx context.Context, opts verify.Options) (*verify.Report, error) {
	return verify.Verify(ctx, opts)
}

// Search, searchByKind, searchByAttr live in search.go.

func (s *Service) GetModuleVersion(ctx context.Context, name, version string) (*report.ModuleReport, error) {
	return s.store.GetReport(ctx, name, version)
}
