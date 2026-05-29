package verify

import (
	"os"
	"path/filepath"
)

// checkOrphanBlobs surfaces any file under blobs/ that no source.json
// references. Severity is Info — orphan blobs aren't broken state, just
// wasted disk and a sign the operator deleted modules without pruning.
//
// Two categories surface here:
//   - canonical hex-named blobs (the standard case) whose content
//     address isn't claimed by any module's integrity field;
//   - non-canonical files (anything that doesn't match the 64-hex
//     blobs/<sha256-hex> convention) — these were either written by an
//     older canopy version or hand-placed, and definitely warrant a
//     finding.
func checkOrphanBlobs(s *state) []Finding {
	var out []Finding

	// canonical hex blobs not referenced by any source.json
	for _, hex := range sortedBlobHexes(s.blobs) {
		if s.referencedBlobs[hex] {
			continue
		}
		b := s.blobs[hex]
		out = append(out, Finding{
			Kind:     KindOrphanBlobs,
			Severity: SevInfo,
			Path:     "blobs/" + hex,
			Message:  "blob not referenced by any source.json",
			Fix:      "safe to delete; consider `canopy mirror prune` once that exists, or `rm` directly",
			Details: map[string]any{
				"size_bytes":  b.size,
				"sha256_hex": hex,
			},
		})
	}

	// non-canonical entries under blobs/ — surfaced via a direct readdir
	// since these are skipped by listBlobs (which only accepts hex
	// names). Cheap and runs after the main scan.
	blobsDir := filepath.Join(s.mirrorRoot, "blobs")
	ents, err := os.ReadDir(blobsDir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if hexBlobRE.MatchString(name) {
			continue
		}
		// also skip the temp-blob staging files mirror writes during
		// in-flight ingest; those are short-lived and not orphans per
		// se. They're prefixed ".tmp-blob-" per BlobWriter.
		if len(name) > 0 && name[0] == '.' {
			continue
		}
		info, _ := e.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		out = append(out, Finding{
			Kind:     KindOrphanBlobs,
			Severity: SevInfo,
			Path:     "blobs/" + name,
			Message:  "non-canonical file in blobs/ (not a content-addressed sha256-hex name)",
			Fix:      "rename or delete; canopy's mirror writer only produces lowercase-hex sha256 filenames",
			Details: map[string]any{
				"size_bytes": size,
				"filename":   name,
			},
		})
	}
	return out
}
