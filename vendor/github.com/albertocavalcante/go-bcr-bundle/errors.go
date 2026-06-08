// Package bundle implements the airgap transport format for the
// canopy portfolio. See doc.go for the package overview; see
// CHANGELOG.md and ~/dev/md/2026-06-02-go-bcr-bundle-design/ for
// the design rationale.
package bundle

import "errors"

// Sentinel errors. Callers compare with errors.Is. The library
// wraps these with operator-meaningful context (path, key, etc.)
// at the call site, so the unwrapped sentinel is the stable
// predicate even when the wrapped message changes.
var (
	// ErrInvalidBundle is returned on Open when the archive is
	// corrupted, the manifest is missing or malformed, the
	// manifest's schema is violated, or a checksum doesn't match.
	// Use ErrChecksumMismatch directly when an integrity-specific
	// branch is needed.
	ErrInvalidBundle = errors.New("bundle: invalid")

	// ErrUnsupportedBundle is returned on Open when the manifest's
	// apiVersion isn't recognised by this library. Operators should
	// upgrade the consuming library (forward-compat path: future
	// bundle formats land under new apiVersion values without
	// breaking v1 readers).
	ErrUnsupportedBundle = errors.New("bundle: unsupported apiVersion")

	// ErrSignatureInvalid is returned when Manifest.Signature
	// failed verification. Distinct from ErrInvalidBundle so
	// operators can distinguish "corrupted in transit" (would
	// have been caught by checksum verification first) from
	// "actively tampered" (signature catches checksum-recomputation
	// attacks).
	//
	// Reserved at v0.0.1 — signing ships at v0.2.x. v0.0.1 returns
	// ErrNotImplemented when a non-nil Verifier is configured.
	ErrSignatureInvalid = errors.New("bundle: signature invalid")

	// ErrChecksumMismatch is returned when a file's on-disk
	// content hashes differently from its manifest.checksums
	// entry. Surfaces on Open during the integrity-verification
	// pass.
	ErrChecksumMismatch = errors.New("bundle: checksum mismatch")

	// ErrNotFound is returned by Bundle.Read when the requested
	// relPath isn't present in the bundle. Path-generic; callers
	// map to operator-meaningful sentinels at higher layers (the
	// canopy adapter, for example, maps this to its
	// internal/api.ErrModuleNotFound when relPath looked like
	// a module path).
	ErrNotFound = errors.New("bundle: path not found")

	// ErrBlobNotFound is returned by Bundle.ReadBlob when the
	// requested key isn't present. Separate from ErrNotFound so
	// blob-specific recovery paths can branch cleanly.
	ErrBlobNotFound = errors.New("bundle: blob not found")

	// ErrNotImplemented is returned when an API field is
	// configured but the feature behind it hasn't shipped in
	// this library version. Retained from v0.0.1 (where it
	// gated unwired Signer/Verifier); v0.2.0+ unblocks those
	// fields, but the sentinel stays as the convention for any
	// future deferred features (loud-fail beats silent-no-op
	// for security-relevant configuration).
	ErrNotImplemented = errors.New("bundle: feature not implemented in this version")
)
