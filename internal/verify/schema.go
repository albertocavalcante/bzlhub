package verify

import (
	"fmt"
	"strings"
)

// checkSourceJSONSchema validates per-module source.json against
// canopy's expectations: file readable + parseable, type=="archive"
// (the only supported kind today), url present, integrity present and
// well-formed ("sha256-<base64>" decoding to 32 bytes).
//
// This check is intentionally narrower than a JSON-Schema validator —
// we surface only the fields canopy actually relies on at fetch time.
// Adding broader semantic checks here would create false positives
// against valid BCR data that just doesn't carry optional fields.
func checkSourceJSONSchema(s *state) []Finding {
	var out []Finding
	for _, k := range sortedModuleKeys(s.modules) {
		m := s.modules[k]
		path := "modules/" + k.name + "/" + k.version + "/source.json"

		if m.sourceJSONErr != "" {
			out = append(out, Finding{
				Kind:     KindSourceJSONSchema,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  "source.json unreadable or unparseable: " + m.sourceJSONErr,
				Fix:      "re-ingest the module, or fix the file on disk",
			})
			continue
		}
		if m.source == nil {
			// shouldn't happen if sourceJSONErr is empty, but be defensive
			continue
		}
		src := m.source

		// The BCR convention is type=="archive" for tarball downloads.
		// Empty type is also accepted because real BCR source.json files
		// frequently omit the field (the canonical mirror under
		// /tmp/canopy-diff-mirror does). Treat "" as "archive" — only
		// a non-empty non-archive value is an error.
		if src.Type != "" && src.Type != "archive" {
			out = append(out, Finding{
				Kind:     KindSourceJSONSchema,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  fmt.Sprintf("unsupported source.json type %q (canopy only handles archive)", src.Type),
				Fix:      "this module uses a non-archive source.json (git_repository, etc.); canopy can't mirror it currently",
				Details:  map[string]any{"type": src.Type},
			})
		}

		if strings.TrimSpace(src.URL) == "" {
			out = append(out, Finding{
				Kind:     KindSourceJSONSchema,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  "source.json has no url",
				Fix:      "re-ingest from upstream; url is required for archive-type sources",
			})
		}

		if strings.TrimSpace(src.Integrity) == "" {
			out = append(out, Finding{
				Kind:     KindSourceJSONSchema,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  "source.json has no integrity",
				Fix:      "re-ingest from upstream; SRI integrity is required to verify blob contents",
			})
		} else if !strings.HasPrefix(src.Integrity, "sha256-") {
			out = append(out, Finding{
				Kind:     KindSourceJSONSchema,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  fmt.Sprintf("integrity %q is not in sha256-<base64> form", src.Integrity),
				Fix:      "canopy currently supports only sha256 SRI",
				Details:  map[string]any{"integrity": src.Integrity},
			})
		} else if m.expectedBlobHex == "" {
			// sriToHex returned !ok despite the sha256- prefix: base64
			// decode failed or wrong byte length. Either way operators
			// need to know.
			out = append(out, Finding{
				Kind:     KindSourceJSONSchema,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     path,
				Message:  fmt.Sprintf("integrity %q malformed (base64 invalid or not 32 bytes)", src.Integrity),
				Fix:      "re-ingest from upstream to repair the integrity field",
				Details:  map[string]any{"integrity": src.Integrity},
			})
		}
	}
	return out
}
