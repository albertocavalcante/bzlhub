package bcrmirror

import "errors"

// Sentinel errors returned by Mirror operations. Callers compare via
// errors.Is.
var (
	// ErrModuleNotFound is returned by read operations when the
	// directory modules/<module>/ doesn't exist in the mirror.
	ErrModuleNotFound = errors.New("bcrmirror: module not found in mirror")

	// ErrVersionNotFound is returned by read operations when the
	// directory modules/<module>/<version>/ doesn't exist (the
	// module itself may or may not exist).
	ErrVersionNotFound = errors.New("bcrmirror: version not found in mirror")

	// ErrPatchNotFound is returned by ReadPatch when the specific
	// patch file doesn't exist under the version directory.
	ErrPatchNotFound = errors.New("bcrmirror: patch not found")

	// ErrNoMirror is returned by Open when Path doesn't exist or
	// isn't a valid git repository. Clone never returns this — Clone
	// creates the path.
	ErrNoMirror = errors.New("bcrmirror: mirror path does not exist")

	// ErrAlreadyCloned is returned by Clone when Path already
	// contains a valid clone of Remote. The receipt carries the
	// existing SHA; callers typically inspect the error type and
	// proceed rather than treating it as a hard failure.
	ErrAlreadyCloned = errors.New("bcrmirror: path already contains a clone")

	// ErrNotFastForward is returned by Sync when the local clone
	// diverged from remote — a fast-forward pull is impossible. The
	// caller decides whether to force-pull (via SyncOptions.Force)
	// or surface the divergence to the operator.
	ErrNotFastForward = errors.New("bcrmirror: sync would require non-fast-forward")

	// ErrInvalidSignature is returned by VerifyCommit when the
	// signature itself is invalid (wrong digest, malformed,
	// expired key).
	ErrInvalidSignature = errors.New("bcrmirror: commit signature invalid")

	// ErrUnknownSigner is returned by VerifyCommit when the
	// signature is valid but the signer's key is not in the allowed
	// set.
	ErrUnknownSigner = errors.New("bcrmirror: commit signed by unknown key")

	// ErrSignatureVerificationNotImplemented is returned by
	// VerifyCommit at v0.1.x when RequireSignature is true. Full
	// implementation lands in v0.2; see docs/plans/51 in canopy.
	ErrSignatureVerificationNotImplemented = errors.New(
		"bcrmirror: signature verification not implemented (target v0.2)")

	// ErrInvalidName is returned by Read*/List*/MetadataAt/LogChanges
	// when a caller-supplied module, version, or patch name contains
	// characters that could escape the modules/ subtree (e.g. "..",
	// "/", leading ".", NUL byte) OR is empty.
	//
	// This is canopy's load-bearing security boundary against path
	// traversal: an unconstrained operator (or untrusted caller) MUST
	// NOT be able to read .git/config, /etc/passwd, etc. through a
	// crafted module name. Validation is applied at every public-API
	// entry point that consumes a name as a path component.
	ErrInvalidName = errors.New("bcrmirror: invalid name (path traversal or unsafe characters)")
)
