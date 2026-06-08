package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

func (h *handler) apiListVersions(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	// Prefer ListVersionsWithMeta on the real Service (one query
	// yields versions + per-row metadata). Fall back to the leaner
	// ListVersions for mock/test implementations of api.Canopy that
	// don't carry the richer query.
	type rowMeta struct {
		ingestedAt  time.Time
		compatLevel int
		tarballSize int64
	}
	var (
		versions      []string
		metaByVersion = map[string]rowMeta{}
		yankedReasons = map[string]string{}
	)
	if h.helper != nil {
		metaRows, err := h.helper.ListVersionsWithMeta(r.Context(), module)
		if err != nil {
			h.apiError(w, err)
			return
		}
		versions = make([]string, 0, len(metaRows))
		for _, mr := range metaRows {
			versions = append(versions, mr.Version)
			metaByVersion[mr.Version] = rowMeta{
				ingestedAt:  mr.IngestedAt,
				compatLevel: mr.CompatibilityLevel,
				tarballSize: mr.TarballSize,
			}
		}
		// Per-row yanked status comes from upstream metadata.json,
		// already mirrored locally. Single read per /api/modules/{m}
		// request — populate a version→reason map and look up per
		// row below.
		if meta := h.readRegistryMetadata(module); meta != nil {
			for v, reason := range meta.YankedVersions {
				yankedReasons[v] = reason
			}
		}
	} else {
		vs, err := h.c.ListVersions(r.Context(), module)
		if err != nil {
			h.apiError(w, err)
			return
		}
		versions = vs
	}
	// Pin counts: best-effort, only when a helper is wired (mock /
	// test backends skip — UI already treats an absent pin_count as
	// "no signal"). Computed once per request; O(N) over the
	// corpus's BazelDeps lists.
	pinByVersion := map[string]int{}
	pinTotal := 0
	if h.helper != nil {
		if usage, err := h.helper.ComputeUsageCountsByVersion(r.Context()); err == nil {
			if perVer, ok := usage[module]; ok {
				for v, n := range perVer {
					pinByVersion[v] = n
					pinTotal += n
				}
			}
		}
	}
	// Build presentation-ready entries: every row carries its own
	// pre-shaped hrefs (code-nav, diff-vs-next-older) so the UI
	// just renders. ListVersions sorts DESC, so entries[i+1] is
	// the next-older version — the natural "from" for a "what
	// changed when bumping to this row" diff. Stub versions
	// (literal "0.0.0", "", "0" — placeholders that surface when
	// a MODULE.bazel ships without a real version) get skipped
	// when picking that default "from" pair; they'd otherwise
	// produce a meaningless empty diff against the real version.
	entries := make([]versionEntry, 0, len(versions))
	for i, v := range versions {
		e := versionEntry{
			Version:     v,
			Href:        moduleVersionURL(module, v),
			CodeNavHref: codeNavRootURL(module, v),
			IsStub:      api.IsStubVersion(v),
		}
		if meta, ok := metaByVersion[v]; ok {
			if !meta.ingestedAt.IsZero() {
				e.IngestedAt = meta.ingestedAt.UTC().Format(time.RFC3339)
				// Cadence label vs the immediately-next-older row
				// (i+1 in DESC order). Uses the raw next row, not
				// the next-non-stub, because the label is about
				// release rhythm — stubs are still ingest events.
				if i+1 < len(versions) {
					if prev, ok := metaByVersion[versions[i+1]]; ok && !prev.ingestedAt.IsZero() {
						e.CadenceLabel = cadenceLabel(meta.ingestedAt.Sub(prev.ingestedAt))
					}
				}
			}
			e.CompatLevel = meta.compatLevel
			e.TarballSize = meta.tarballSize
		}
		if reason, ok := yankedReasons[v]; ok {
			e.YankedReason = reason
		}
		if n, ok := pinByVersion[v]; ok && n > 0 {
			e.PinCount = n
			if pinTotal > 0 {
				e.PinPct = int((float64(n) / float64(pinTotal) * 100) + 0.5)
			}
		}
		// Find the next-older non-stub version for the default
		// diff pair. Don't suggest a diff at all when this entry
		// is itself a stub — comparing against another version
		// from the placeholder side is rarely informative.
		if !e.IsStub {
			for j := i + 1; j < len(versions); j++ {
				if api.IsStubVersion(versions[j]) {
					continue
				}
				e.DiffHref = diffURL(module, versions[j], v)
				e.DiffFromVersion = versions[j]
				break
			}
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"module":   module,
		"versions": versions, // kept for backward compat with any plain-array consumers
		"entries":  entries,
	})
}

// versionEntry is one row of the /api/modules/{m} response with
// presentation-ready fields. The UI iterates and renders.
type versionEntry struct {
	Version         string `json:"version"`
	Href            string `json:"href"`
	CodeNavHref     string `json:"code_nav_href"`
	DiffHref        string `json:"diff_href,omitempty"`
	DiffFromVersion string `json:"diff_from_version,omitempty"`
	// IsStub flags the placeholder version values that surface
	// when a MODULE.bazel was ingested without a real version
	// declaration ("0.0.0", "", "0"). The UI badges these so
	// users don't mistake them for real releases.
	IsStub bool `json:"is_stub,omitempty"`
	// IngestedAt is when this (module, version) row was first
	// written to canopy's index, RFC3339-formatted. The UI badges
	// each row with a relative-time display ("23h ago"). Empty
	// when not available (test/mock backends).
	IngestedAt string `json:"ingested_at,omitempty"`
	// CadenceLabel is a compact "+timedelta" hint vs the next-
	// older version's IngestedAt — "+3d", "+2.4mo", "+10h". v0.2
	// semantic: this is *ingest* cadence (when canopy got it),
	// not necessarily upstream publish cadence. Empty for the
	// oldest version or when one of the timestamps is missing.
	CadenceLabel string `json:"cadence_label,omitempty"`
	// CompatLevel is the module's declared compatibility_level for
	// this version. 0 = "no compat declared" (the BCR convention);
	// non-zero values indicate compat-cohort membership. Drives
	// the "L<N>" chip on /modules/<name> rows.
	CompatLevel int `json:"compat_level,omitempty"`
	// TarballSize is the compressed source tarball size in bytes.
	// 0/missing for pre-migration ingests.
	TarballSize int64 `json:"tarball_size,omitempty"`
	// YankedReason, when non-empty, is the upstream-declared reason
	// this version was yanked (from metadata.json's yanked_versions
	// map). The UI badges the row with "yanked" + hover-tooltip.
	YankedReason string `json:"yanked_reason,omitempty"`
	// PinCount is the number of distinct consumer modules that pin
	// exactly this (module, version) via a bazel_dep declaration in
	// canopy's indexed corpus. Zero / missing means "no consumers
	// pin this version" — which is the common case for old releases
	// once the corpus rolls forward. The UI badges rows with
	// pin_count > 0 to surface adoption among the rest.
	PinCount int `json:"pin_count,omitempty"`
	// PinPct is PinCount as a percentage of the total pins across
	// every indexed version of this module (rounded to nearest int,
	// 0-100). Lets the UI render "majority pin" highlighting on the
	// dominant version without the client recomputing.
	PinPct int `json:"pin_pct,omitempty"`
}

func moduleVersionURL(m, v string) string { return "/modules/" + m + "/" + v }
func codeNavRootURL(m, v string) string   { return "/modules/" + m + "/" + v + "/code-nav/" }
func diffURL(m, from, to string) string {
	return "/modules/" + m + "/diff?from=" + from + "&to=" + to
}

// cadenceLabel formats a duration as a compact "+<n><unit>" hint
// for the versions table superscript. Granularity drops at the
// boundary that makes the unit non-zero (no "+0d"); a near-zero
// duration becomes "+0h" rather than empty so the label is always
// present when both timestamps exist.
//
// Examples (all positive — d would be the older-to-newer gap):
//
//	3*time.Hour      -> "+3h"
//	3*24*time.Hour   -> "+3d"
//	60*24*time.Hour  -> "+2.0mo" (months are 30d)
//	365*24*time.Hour -> "+12mo" (no year unit; keeps the label compact)
func cadenceLabel(d time.Duration) string {
	if d < 0 {
		return ""
	}
	if d < time.Hour {
		return "+<1h"
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("+%dh", int(d/time.Hour))
	}
	days := d / (24 * time.Hour)
	if days < 30 {
		return fmt.Sprintf("+%dd", days)
	}
	months := float64(d) / float64(30*24*time.Hour)
	if months < 12 {
		return fmt.Sprintf("+%.1fmo", months)
	}
	return fmt.Sprintf("+%dmo", int(months))
}
