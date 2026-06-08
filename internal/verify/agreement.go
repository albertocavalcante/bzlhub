package verify

import "sort"

// checkIndexMirrorAgreement surfaces (module, version) pairs present
// on one side (DB index vs. on-disk mirror tree) but not the other.
//
// Severity is Warning rather than Error because the canopy server can
// still operate in either degraded mode:
//   - "indexed but missing from disk": serve endpoints will 404 on
//     fetch but search/show still work
//   - "on disk but not indexed": Bazel can still resolve via BCR HTTP
//     endpoints, but search/diff against this version returns nothing
//
// Either is bad — but neither is "registry is broken right now" — so
// Warning is the right tier. Operators get one finding per gap, named
// by module@version.
func checkIndexMirrorAgreement(s *state) []Finding {
	if s.store == nil {
		// No DB configured — can't run this check. Stay silent rather
		// than emit a Warning for the operator's choice not to pass
		// --db; the CLI guards this in its own way (default --db is set).
		return nil
	}

	var out []Finding

	// indexed-but-no-tree
	var ghostKeys []moduleKey
	for k := range s.indexed {
		if _, ok := s.modules[k]; !ok {
			ghostKeys = append(ghostKeys, k)
		}
	}
	sort.Slice(ghostKeys, func(i, j int) bool {
		if ghostKeys[i].name != ghostKeys[j].name {
			return ghostKeys[i].name < ghostKeys[j].name
		}
		return ghostKeys[i].version < ghostKeys[j].version
	})
	for _, k := range ghostKeys {
		out = append(out, Finding{
			Kind:     KindIndexMirrorAgreement,
			Severity: SevWarning,
			Module:   k.name,
			Version:  k.version,
			Message:  "present in index but missing from mirror tree",
			Fix:      "re-ingest the module, or delete the orphaned index row",
			Details:  map[string]any{"side": "index-only"},
		})
	}

	// tree-but-no-index
	var orphanTreeKeys []moduleKey
	for k := range s.modules {
		if !s.indexed[k] {
			orphanTreeKeys = append(orphanTreeKeys, k)
		}
	}
	sort.Slice(orphanTreeKeys, func(i, j int) bool {
		if orphanTreeKeys[i].name != orphanTreeKeys[j].name {
			return orphanTreeKeys[i].name < orphanTreeKeys[j].name
		}
		return orphanTreeKeys[i].version < orphanTreeKeys[j].version
	})
	for _, k := range orphanTreeKeys {
		out = append(out, Finding{
			Kind:     KindIndexMirrorAgreement,
			Severity: SevWarning,
			Module:   k.name,
			Version:  k.version,
			Path:     "modules/" + k.name + "/" + k.version,
			Message:  "present in mirror tree but missing from index",
			Fix:      "run `bzlhub ingest <module-dir>` to register the on-disk version into the index",
			Details:  map[string]any{"side": "mirror-only"},
		})
	}
	return out
}
