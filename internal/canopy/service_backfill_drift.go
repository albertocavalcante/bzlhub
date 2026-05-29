package canopy

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/albertocavalcante/canopy/internal/api"
)

// BackfillDriftSummary is the boot-time reconcile seam for the
// versions.drift_summary_json column. The function is intentionally
// minimal for M1 (Plan 28): it walks the index, counts rows whose
// cached drift is "unknown", and logs the count at INFO. It does
// NOT write any drift data — that is the future responsibility of:
//
//   - Plan 20 A4 (bcrmirror): drift becomes a `git log` query
//     against the cloned BCR; the result is cached here.
//   - Plan 21 B4 (sync-runner): the candidate→trusted promotion
//     pipeline writes a layered DriftSummary including
//     UpstreamSHA, SyncedAt, PromotedAt fields (Plan 21 honest-
//     staleness model).
//   - Plan 26 κ6 (ModuleReport-in-AC): ModuleReport's drift
//     subfield is fetched from REAPI ActionCache when shared
//     across canopy instances.
//
// All three future paths reuse the seam below — calling
// store.SetDriftSummary with the JSON-encoded api.DriftSummary —
// without re-wiring the cmd-side BackfillDriftSummary call site.
//
// The signature mirrors BackfillSourceIndexFlags so cmd/canopy/serve.go
// can call both in the same boot block. The returned int is "rows
// updated"; at M1 that is always 0. The unknown-count is surfaced
// via slog so operators see "200 modules with unknown drift" on
// boot — actionable observability that motivates configuring a
// drift source.
func (s *Service) BackfillDriftSummary(ctx context.Context) (int, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return 0, err
	}
	var unknown int
	for _, mv := range rows {
		payload, err := s.store.GetDriftSummary(ctx, mv.Module, mv.Version)
		if err != nil {
			// Read failures on individual rows are non-fatal —
			// keep walking. The boot reconcile must not abort over
			// one corrupt row.
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
	// M1: no source wired; no rows updated.
	return 0, nil
}
