package bzlhub

import (
	"context"

	"github.com/albertocavalcante/assay/report"
)

// CorpusStats is the aggregate "how big is this canopy" view that
// drives the home dashboard counters. JSON-serializable.
type CorpusStats struct {
	Modules           int `json:"modules"`
	Versions          int `json:"versions"`
	DocumentedSymbols int `json:"documented_symbols"`
}

// ComputeCorpusStats walks every indexed (module, version), loads
// the report for the latest version of each module, and sums the
// public-API symbol counts (rules + providers + macros + aspects +
// repo_rules + module_extensions + toolchains).
//
// O(modules) report loads per call; ~5ms per load at canopy's
// current scale. At thousands of modules this becomes a measurable
// cost; promote to a denormalized column on the versions table at
// that point.
//
// Exposed for the HTTP layer's home-page dashboard via the
// server.ReadHelper interface.
func (s *Service) ComputeCorpusStats(ctx context.Context) (*CorpusStats, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	stats := &CorpusStats{Versions: len(rows)}

	// Pick the latest version per module from the ASC stream.
	type latestRef struct {
		module  string
		version string
	}
	latest := []latestRef{}
	for _, mv := range rows {
		if n := len(latest); n > 0 && latest[n-1].module == mv.Module {
			latest[n-1].version = mv.Version
			continue
		}
		latest = append(latest, latestRef{mv.Module, mv.Version})
	}
	stats.Modules = len(latest)

	for _, l := range latest {
		rep, err := s.store.GetReport(ctx, l.module, l.version)
		if err != nil {
			// Best-effort: missing a single report doesn't fail
			// the whole stat. Operator surfaces this via /history.
			continue
		}
		stats.DocumentedSymbols += countPublicSymbols(rep)
	}
	return stats, nil
}

// countPublicSymbols totals the public-API symbols across all of
// the kinds canopy renders in its documentation surface. Mirrors
// the symbol-count semantics that registry frontends advertise on
// home dashboards.
func countPublicSymbols(rep *report.ModuleReport) int {
	if rep == nil {
		return 0
	}
	n := 0
	for _, r := range rep.Rules {
		if !r.Private {
			n++
		}
	}
	for _, p := range rep.Providers {
		if !p.Private {
			n++
		}
	}
	for _, a := range rep.Aspects {
		if !a.Private {
			n++
		}
	}
	for _, rr := range rep.RepositoryRules {
		if !rr.Private {
			n++
		}
	}
	for _, me := range rep.ModuleExtensions {
		if !me.Private {
			n++
		}
	}
	n += len(rep.Macros)     // macros have no private flag in the report schema
	n += len(rep.Toolchains) // toolchain types are always public
	return n
}

// ComputeUsageCounts walks every indexed (module, version)'s
// bazel_deps and tallies references per dep-name. Dedupes by
// (consumer-module, dep-name) to avoid inflating the count when a
// module declares the same dep across multiple versions —
// "rules_go is used by gazelle" should count once whether
// gazelle's index has one version or five.
//
// Shared by ListModules (populates the listing chips) and the
// per-module API augmentation (populates the detail header chip).
// O(N) over indexed modules per call; documented as
// deferred-to-cached when the corpus crosses thousands.
func (s *Service) ComputeUsageCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	usage := map[string]int{}
	seen := map[string]bool{} // "consumer→depName"
	for _, mv := range rows {
		rep, err := s.store.GetReport(ctx, mv.Module, mv.Version)
		if err != nil || rep == nil {
			continue
		}
		for _, d := range rep.BazelDeps {
			key := mv.Module + "→" + d.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			usage[d.Name]++
		}
	}
	return usage, nil
}

// ComputeUsageCountsByVersion is ComputeUsageCounts split by the
// version each consumer pinned. Returned shape:
//
//	usage["rules_go"]["0.50.1"] = 12   // 12 distinct consumers pin v0.50.1
//	usage["rules_go"]["0.49.0"] = 3
//
// Dedupes by (consumer-module, dep-name, dep-version) so a consumer
// that indexes multiple of its OWN versions but all pin the same
// (dep-name, dep-version) only counts once toward that version's
// adoption — same invariant as ComputeUsageCounts, projected onto
// the per-version axis.
//
// O(N) over indexed modules per call. Cheap enough to call on every
// /api/modules/{m} request at corpus sizes <1k; cached at the
// service layer when the corpus crosses thousands.
func (s *Service) ComputeUsageCountsByVersion(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	usage := map[string]map[string]int{}
	seen := map[string]bool{} // "consumer→depName@depVersion"
	for _, mv := range rows {
		rep, err := s.store.GetReport(ctx, mv.Module, mv.Version)
		if err != nil || rep == nil {
			continue
		}
		for _, d := range rep.BazelDeps {
			key := mv.Module + "→" + d.Name + "@" + d.Version
			if seen[key] {
				continue
			}
			seen[key] = true
			byVer, ok := usage[d.Name]
			if !ok {
				byVer = map[string]int{}
				usage[d.Name] = byVer
			}
			byVer[d.Version]++
		}
	}
	return usage, nil
}
