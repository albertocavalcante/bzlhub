package admit

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BuildSourceJSON returns the serialized source.json for one new
// BCR entry: the upstream archive URL, its SRI integrity hash, and
// the optional strip_prefix.
//
// All three correspond to the source.json fields Bazel reads when
// resolving the module — see
// https://bazel.build/external/registry#source-json.
func BuildSourceJSON(url, integrity, stripPrefix string) ([]byte, error) {
	if url == "" {
		return nil, errors.New("source.json: url required")
	}
	if integrity == "" {
		return nil, errors.New("source.json: integrity required")
	}
	// SRI ("Subresource Integrity") values are "<algo>-<base64>".
	// Bazel today only honors sha256/sha512; we don't enforce the
	// algorithm choice here but the scheme prefix is mandatory.
	if !strings.ContainsRune(integrity, '-') {
		return nil, errors.New(`source.json: integrity must be SRI-formatted ("sha256-...")`)
	}
	out := map[string]any{
		"url":       url,
		"integrity": integrity,
	}
	if stripPrefix != "" {
		out["strip_prefix"] = stripPrefix
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal source.json: %w", err)
	}
	return b, nil
}

// SRIFromSHA256 returns the SRI-formatted hash for a raw 32-byte
// SHA-256 digest ("sha256-<base64>"). The publish package's BlobRef
// already emits this format — this helper exists for callers that
// have a raw digest (e.g., from hashing in-memory).
func SRIFromSHA256(digest []byte) string {
	return "sha256-" + base64.StdEncoding.EncodeToString(digest)
}

// DetectStripPrefix returns the common top-level directory shared
// by every entry, or "" when there isn't one. Used to set
// source.json's strip_prefix automatically for the common case of
// a GitHub-style archive where every path is rooted at
// "<repo>-<tag>/".
//
// Returns "" when entries is empty, when no common prefix exists,
// or when any entry is a top-level file (no slash).
func DetectStripPrefix(entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	first := topLevelDir(entries[0])
	if first == "" {
		return ""
	}
	for _, e := range entries[1:] {
		if topLevelDir(e) != first {
			return ""
		}
	}
	return first
}

func topLevelDir(path string) string {
	i := strings.IndexByte(path, '/')
	if i <= 0 {
		return ""
	}
	return path[:i]
}
