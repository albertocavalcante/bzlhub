package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// checkBlobIntegrity verifies that the on-disk blob for each module
// matches the SHA256 integrity declared in its source.json. Two failure
// modes get their own Kind so consumers can scope on the precise
// problem:
//
//   - KindBlobIntegrity: blob present but content hash doesn't match.
//   - KindBlobMissing:   source.json references a blob that isn't on disk.
//
// Modules whose source.json couldn't be parsed are skipped here — the
// source_json_schema check surfaces those separately. Surfacing the
// same problem twice would just clutter the report.
func checkBlobIntegrity(s *state) []Finding {
	var out []Finding
	for _, k := range sortedModuleKeys(s.modules) {
		m := s.modules[k]
		if m.source == nil {
			// schema check handles this; don't double-report
			continue
		}
		if m.expectedBlobHex == "" {
			// integrity field missing/malformed → schema check handles it
			continue
		}
		blob, ok := s.blobs[m.expectedBlobHex]
		if !ok {
			out = append(out, Finding{
				Kind:     KindBlobMissing,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     "blobs/" + m.expectedBlobHex,
				Message:  fmt.Sprintf("blob %s referenced by source.json is not on disk", m.expectedBlobHex),
				Fix:      "re-ingest the module (canopy ingest ...) or restore the blob from backup",
				Details: map[string]any{
					"expected_sha256_hex": m.expectedBlobHex,
					"source_url":          m.source.URL,
				},
			})
			continue
		}

		actualHex, err := hashFile(blob.path)
		if err != nil {
			// Treat unreadable blob as a missing-equivalent integrity
			// failure — the operator can't trust it either way.
			out = append(out, Finding{
				Kind:     KindBlobIntegrity,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     "blobs/" + m.expectedBlobHex,
				Message:  fmt.Sprintf("could not read blob %s: %v", m.expectedBlobHex, err),
				Fix:      "check filesystem health; re-ingest if the file is unrecoverable",
				Details: map[string]any{
					"expected_sha256_hex": m.expectedBlobHex,
					"read_error":          err.Error(),
				},
			})
			continue
		}
		if actualHex != m.expectedBlobHex {
			out = append(out, Finding{
				Kind:     KindBlobIntegrity,
				Severity: SevError,
				Module:   k.name,
				Version:  k.version,
				Path:     "blobs/" + m.expectedBlobHex,
				Message:  "blob hash mismatch (computed != source.json integrity)",
				Fix:      "re-ingest with `canopy ingest --from <upstream> <m>@<v> --mirror-to <root>`, or restore from backup",
				Details: map[string]any{
					"expected_sha256_hex": m.expectedBlobHex,
					"actual_sha256_hex":   actualHex,
				},
			})
		}
	}
	return out
}

// hashFile streams a file through sha256, never loading the full
// archive in memory. The "verify" command needs to scale to mirrors
// holding GB of cumulative blob data without OOM'ing.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
