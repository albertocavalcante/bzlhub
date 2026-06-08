package bzlhub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub/health"
)

// MirrorStatusReport is the operator's at-a-glance health view of
// one canopy install. Mirror-side fields are zero when no Mirror
// is wired (File-backed install).
type MirrorStatusReport struct {
	IndexedModules  int            `json:"indexed_modules"`
	IndexedVersions int            `json:"indexed_versions"`
	DriftByStatus   map[string]int `json:"drift_by_status"`
	// PendingCompute counts rows still in the default Unknown
	// state — drift hasn't been computed for them yet.
	PendingCompute int       `json:"pending_compute"`
	MirrorPath     string    `json:"mirror_path,omitempty"`
	MirrorHEAD     string    `json:"mirror_head,omitempty"`
	LastSync       time.Time `json:"last_sync,omitzero"`
	// Computed is the CLI-subset health verdict from
	// health.DeriveLocal, populated at MirrorStatus time using the
	// same thresholds as the /api/v1/system/status wire endpoint.
	// JSON shape matches the wire's `computed` block (instant_state
	// + signals[]) so `bzlhub status --format=json` and `curl
	// /api/v1/system/status` are queryable with the same jq
	// expressions.
	//
	// The CLI doesn't run a federation cascade so only mirror sync
	// staleness + drift count signals can appear; operators relying
	// on this for alerting in a federated topology should pair it
	// with the wire endpoint.
	Computed api.ComputedStatus `json:"computed"`
}

// Compile-time guard that *Service still satisfies the
// api.MirrorHeader contract the /api/v1/system/status handler
// asserts at runtime.
var _ api.MirrorHeader = (*Service)(nil)

// MirrorHead returns the Mirror's HEAD commit SHA and last-sync
// timestamp. Returns ("", zero) when no Mirror is wired. Cheaper
// than MirrorStatus — no index walk.
func (s *Service) MirrorHead(ctx context.Context) (string, time.Time) {
	if s.mirror == nil {
		return "", time.Time{}
	}
	sha, _ := s.mirror.SnapshotSHA(ctx)
	return sha, s.mirror.LastSync()
}

// MirrorStatus collects index counters + Mirror state into one
// snapshot. Read-only; tolerates a missing Mirror by leaving its
// fields zero.
func (s *Service) MirrorStatus(ctx context.Context) (MirrorStatusReport, error) {
	rep := MirrorStatusReport{DriftByStatus: map[string]int{}}

	seen := map[string]struct{}{}
	for r, err := range s.store.AllVersionsWithDrift(ctx) {
		if err != nil {
			return rep, err
		}
		rep.IndexedVersions++
		seen[r.Module] = struct{}{}
		var d api.DriftSummary
		if err := json.Unmarshal(r.DriftRaw, &d); err != nil {
			continue
		}
		if d.Status == "" || d.Status == api.DriftStatusUnknown {
			rep.PendingCompute++
			continue
		}
		rep.DriftByStatus[string(d.Status)]++
	}
	rep.IndexedModules = len(seen)

	if s.mirror != nil {
		rep.MirrorPath = s.mirror.Path
		if sha, err := s.mirror.SnapshotSHA(ctx); err == nil {
			rep.MirrorHEAD = sha
		}
		rep.LastSync = s.mirror.LastSync()
	}

	// Server-derived verdict + signal breakdown, restricted to the
	// CLI's visible inputs. Pulled at the end so it reflects EVERY
	// field we just populated.
	behind := rep.DriftByStatus[string(api.DriftStatusBehind)]
	yanked := rep.DriftByStatus[string(api.DriftStatusYankedUpstream)]
	rep.Computed = health.DeriveLocal(behind, yanked, rep.LastSync, time.Now())

	return rep, nil
}
