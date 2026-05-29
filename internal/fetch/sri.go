// Package fetch downloads source archives from BCR-compatible registries
// with SHA256 SRI verification.
package fetch

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash"
	"io"
	"strings"
)

// VerifyingReader wraps an io.Reader and computes a running SHA256.
// After EOF, call Verify to compare against an expected SRI string of the
// form "sha256-<base64>". Mismatch returns an error; the caller MUST treat
// the read bytes as untrusted on mismatch.
type VerifyingReader struct {
	r        io.Reader
	hasher   hash.Hash
	expected string
}

// NewVerifyingReader wraps r. The expected integrity string follows SRI:
// "sha256-<base64>". Empty integrity disables verification (returns nil from
// Verify) but the hash is still computed for callers that want it via Sum.
func NewVerifyingReader(r io.Reader, expected string) *VerifyingReader {
	return &VerifyingReader{r: r, hasher: sha256.New(), expected: expected}
}

func (v *VerifyingReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if n > 0 {
		v.hasher.Write(p[:n])
	}
	return n, err
}

// Verify compares the computed hash to the expected SRI string. Returns nil
// on match (or when expected was empty). Call only after the wrapped reader
// has returned io.EOF; otherwise the running hash is incomplete.
func (v *VerifyingReader) Verify() error {
	if v.expected == "" {
		return nil
	}
	algo, want, ok := splitSRI(v.expected)
	if !ok {
		return fmt.Errorf("malformed integrity %q: want \"sha256-<base64>\"", v.expected)
	}
	if algo != "sha256" {
		return fmt.Errorf("unsupported integrity algorithm %q (only sha256 supported)", algo)
	}
	got := base64.StdEncoding.EncodeToString(v.hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("integrity mismatch: want %s-%s, got %s-%s", algo, want, algo, got)
	}
	return nil
}

// Sum returns the raw SHA256 of all data read so far. Useful when the
// caller wants to record an integrity string for a freshly-fetched blob.
func (v *VerifyingReader) Sum() []byte { return v.hasher.Sum(nil) }

// SRI formats raw SHA256 bytes as the canonical "sha256-<base64>" string.
func SRI(sum []byte) string {
	return "sha256-" + base64.StdEncoding.EncodeToString(sum)
}

func splitSRI(s string) (algo, b64 string, ok bool) {
	return strings.Cut(s, "-")
}
