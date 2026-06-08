package drift

import (
	"github.com/albertocavalcante/bzlhub/internal/fetch"
)

// VersionStatus is the per-(module, version) outcome returned by
// ComputeForVersion. It mirrors api.DriftStatus's string constants
// but is defined here to avoid pulling internal/api into drift —
// api already imports drift for *drift.Report, so the reverse arrow
// would form a cycle. Callers in internal/canopy translate this
// string into api.DriftStatus at the assembly site.
type VersionStatus string

const (
	VersionStatusInSync         VersionStatus = "in-sync"
	VersionStatusBehind         VersionStatus = "behind"
	VersionStatusYankedUpstream VersionStatus = "yanked-upstream"
	VersionStatusLocalOnly      VersionStatus = "local-only"
)

// VersionDrift is the primitive triple the per-version backfill
// needs: status verdict, behind count (zero when status != Behind),
// and upstream's current latest version string (empty when
// LocalOnly).
type VersionDrift struct {
	Status         VersionStatus
	Behind         int
	LatestUpstream string
}

// ComputeForVersion derives the per-(module, version) drift verdict
// that PR7's backfill writes to versions.drift_summary_json. Callers
// call this once per local version row after reading the upstream
// metadata.json via bcrmirror.MetadataAt at the Mirror's current
// HEAD.
//
// Semantics — status precedence (highest first):
//
//   - LocalOnly      when upstream is nil (module not present
//                    upstream; caller signals via ErrModuleNotFound).
//   - YankedUpstream when the local version is listed in upstream's
//                    yanked_versions map (security signal > freshness
//                    signal, per Plan 19 Idea A).
//   - Behind         when upstream has strictly newer versions than
//                    local. Behind count = number of strictly newer.
//   - InSync         otherwise — including the "local is ahead of
//                    upstream" case (canopy's own published variants).
func ComputeForVersion(localVersion string, upstream *fetch.MetadataJSON) VersionDrift {
	if upstream == nil {
		return VersionDrift{Status: VersionStatusLocalOnly}
	}

	if _, yanked := upstream.YankedVersions[localVersion]; yanked {
		return VersionDrift{
			Status:         VersionStatusYankedUpstream,
			LatestUpstream: pickLatest(upstream.Versions),
		}
	}

	var newer int
	for _, v := range upstream.Versions {
		if compareVersions(v, localVersion) > 0 {
			newer++
		}
	}
	if newer > 0 {
		return VersionDrift{
			Status:         VersionStatusBehind,
			Behind:         newer,
			LatestUpstream: pickLatest(upstream.Versions),
		}
	}

	return VersionDrift{Status: VersionStatusInSync}
}
