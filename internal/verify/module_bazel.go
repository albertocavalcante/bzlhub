package verify

import (
	"fmt"

	gobzlmod "github.com/albertocavalcante/go-bzlmod"
)

// checkModuleBazelPresent confirms each module-version directory has a
// MODULE.bazel that parses with the same gobzlmod parser canopy uses
// during ingestion. A missing or unparseable MODULE.bazel makes the
// module unusable by Bazel — same severity as a corrupted blob.
//
// We parse rather than just stat()'ing because a binary-garbage
// MODULE.bazel exists on disk but isn't a real module; surfacing
// "present but unparseable" as a distinct case (via a different
// message) lets operators triage faster than "module not buildable".
func checkModuleBazelPresent(s *state) []Finding {
	var out []Finding
	for _, k := range sortedModuleKeys(s.modules) {
		m := s.modules[k]
		path := "modules/" + k.name + "/" + k.version + "/MODULE.bazel"

		if m.moduleBazelErr != "" {
			out = append(out, Finding{
				Kind:     KindModuleBazelPresent,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  "MODULE.bazel missing or unreadable: " + m.moduleBazelErr,
				Fix:      "re-ingest the module from upstream; MODULE.bazel is required for Bazel resolution",
			})
			continue
		}
		// Parse with gobzlmod (same surface ingest uses for closure
		// walks). A successful parse is the bar; we don't compare the
		// content against anything here — that's the deep-check's job.
		if _, err := gobzlmod.ParseModuleContent(string(m.moduleBazelRaw)); err != nil {
			out = append(out, Finding{
				Kind:     KindModuleBazelPresent,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  fmt.Sprintf("MODULE.bazel does not parse: %v", err),
				Fix:      "re-ingest the module from upstream; the on-disk file is corrupt",
				Details:  map[string]any{"parse_error": err.Error()},
			})
		}
	}
	return out
}
