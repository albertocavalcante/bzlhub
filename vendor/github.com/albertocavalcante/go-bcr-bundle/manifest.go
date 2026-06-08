package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// APIVersion is the manifest's apiVersion string for this
// library's supported format revision. Future revisions land at
// new APIVersion values ("bcr-bundle/v2", etc.) with their own
// reader paths.
const APIVersion = "bcr-bundle/v1"

// Manifest is the parsed bundle manifest. Fields mirror the
// on-disk JSON shape (see
// ~/dev/md/2026-06-02-go-bcr-bundle-design/01-format-and-workflows.md
// §4).
type Manifest struct {
	APIVersion   string              `json:"apiVersion"`
	CreatedAt    time.Time           `json:"createdAt"`
	CreatedBy    string              `json:"createdBy"`
	SourceURL    string              `json:"sourceURL,omitempty"`
	SourceCommit string              `json:"sourceCommit,omitempty"`
	Modules      map[string][]string `json:"modules"`
	Blobs        []BlobEntry         `json:"blobs"`
	Checksums    map[string]string   `json:"checksums"`
	Signature    *Signature          `json:"signature,omitempty"`
}

// BlobEntry is one row of the manifest's blobs array.
type BlobEntry struct {
	Key  string `json:"key"` // e.g. "sha256-abc123..."
	Size int64  `json:"size"`
}

// Signature is the reserved-at-v0.0.1 manifest signature field.
// Populated by v0.2.x writers when WriteOptions.Signer is set;
// verified by v0.2.x readers when OpenOptions.Verifier is set.
//
// At v0.0.1 the field can appear in manifests being read (e.g.
// from a future-version producer) but will not be verified —
// callers that need verification get ErrNotImplemented when they
// configure a Verifier on this version.
type Signature struct {
	Algorithm string `json:"algorithm"` // currently "ed25519"
	KeyID     string `json:"keyId"`
	Value     string `json:"value"` // base64-encoded raw signature
}

// EncodeManifest writes manifest as canonical JSON: sorted map
// keys, sorted module-version slices, no trailing whitespace.
// Determinism is required so v0.2.x signatures over the manifest
// bytes are stable across re-encodings.
//
// The returned slice is owned by the caller; the library doesn't
// reuse it.
func EncodeManifest(m Manifest) ([]byte, error) {
	// Sort module version slices in place on a defensive copy.
	cp := m
	if m.Modules != nil {
		cp.Modules = make(map[string][]string, len(m.Modules))
		for k, vs := range m.Modules {
			cloned := append([]string(nil), vs...)
			sort.Strings(cloned)
			cp.Modules[k] = cloned
		}
	}
	// Sort blobs by Key for determinism.
	if len(cp.Blobs) > 0 {
		blobs := append([]BlobEntry(nil), cp.Blobs...)
		sort.Slice(blobs, func(i, j int) bool { return blobs[i].Key < blobs[j].Key })
		cp.Blobs = blobs
	}
	// Go's encoding/json sorts map keys alphabetically when
	// marshalling map[string]X, so Modules + Checksums are
	// deterministic out of the box.

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cp); err != nil {
		return nil, fmt.Errorf("bundle: encode manifest: %w", err)
	}
	// json.Encoder appends a trailing newline. The canonical form
	// drops it so byte-comparisons on canonicalised manifests are
	// stable.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// DecodeManifest parses canonical-or-non-canonical manifest JSON.
// Validates apiVersion; returns ErrUnsupportedBundle for unknown
// values. Returns ErrInvalidBundle on schema violation.
func DecodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: parse manifest: %v",
			ErrInvalidBundle, err)
	}
	if m.APIVersion == "" {
		return Manifest{}, fmt.Errorf("%w: manifest missing apiVersion",
			ErrInvalidBundle)
	}
	if m.APIVersion != APIVersion {
		return Manifest{}, fmt.Errorf("%w: got apiVersion=%q; want %q",
			ErrUnsupportedBundle, m.APIVersion, APIVersion)
	}
	if m.Modules == nil {
		// modules being absent is a schema violation distinct from
		// "empty modules map" (which is fine — a bundle from an
		// empty mirror).
		return Manifest{}, fmt.Errorf("%w: manifest missing modules field",
			ErrInvalidBundle)
	}
	if m.Checksums == nil {
		return Manifest{}, fmt.Errorf("%w: manifest missing checksums field",
			ErrInvalidBundle)
	}
	return m, nil
}
