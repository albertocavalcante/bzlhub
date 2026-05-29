package verify

import "sort"

// checkScipPresent surfaces indexed (module, version) pairs that have
// no stored SCIP blob in module_scip. Warning, not Error: the module
// is still fetchable and Bazel doesn't care — but every consumer of
// canopy's code-nav surface (MCP lookup_symbol, the eventual web UI)
// 404s on it. The common cause is "this version was ingested before
// scip-bazel wiring landed"; the fix is a re-ingest.
//
// Only runs when a store is configured: without --db there's no notion
// of "what's indexed", so the check has nothing to compare.
func checkScipPresent(s *state) []Finding {
	if s.store == nil {
		return nil
	}

	var missing []moduleKey
	for k := range s.indexed {
		if !s.scipIndexed[k] {
			missing = append(missing, k)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].name != missing[j].name {
			return missing[i].name < missing[j].name
		}
		return missing[i].version < missing[j].version
	})

	out := make([]Finding, 0, len(missing))
	for _, k := range missing {
		out = append(out, Finding{
			Kind:     KindScipMissing,
			Severity: SevWarning,
			Module:   k.name,
			Version:  k.version,
			Message:  "module is indexed but has no SCIP blob",
			Fix:      "re-ingest the module to backfill its SCIP index (scip-bazel runs as part of ingest)",
		})
	}
	return out
}
