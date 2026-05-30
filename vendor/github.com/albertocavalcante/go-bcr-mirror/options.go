package bcrmirror

import "time"

// CloneOptions controls the initial Clone behaviour.
type CloneOptions struct {
	// Depth caps the clone to the most recent N commits. 0 (the
	// default) is a full clone. Drift-aware operations (LogChanges,
	// MetadataAt) require full history; canopy's bcrmirror backend
	// uses 0 by default. A future "browse-only" consumer might use
	// Depth=1 for fastest clone at the cost of LogChanges.
	Depth int

	// Sparse, if non-empty, restricts the working tree to the
	// listed path patterns via sparse-checkout. Reduces disk
	// footprint on huge mirrors.
	//
	// Empirically measured (2026-05-29 spike, see canopy
	// reference_canopy_bcrmirror_clone_cost.md): for BCR's full
	// modules/ tree this is a 9-second penalty with no disk-size
	// win, since modules/ is the repo's entire content. Leave
	// empty unless an operator specifically wants a curated module
	// subset.
	Sparse []string

	// AllBranches, when true, clones every branch on the remote.
	// Default (false) is single-branch: only the chosen Branch is
	// fetched, matching the documented sync-runner
	// candidate/trusted convention.
	//
	// (Inverted from a hypothetical SingleBranch field so the Go
	// zero value matches the intended default — opt-in to the wider
	// clone, not opt-out of the narrow one.)
	AllBranches bool

	// Branch (default "main") is the branch to clone.
	Branch string

	// Timeout bounds the clone duration. 0 (the default) means
	// "use a sensible default of 30 minutes" — chosen as the upper
	// bound for a fresh BCR clone over a slow corp link.
	Timeout time.Duration
}

// SyncOptions controls Sync behaviour.
type SyncOptions struct {
	// Timeout bounds the sync duration. 0 → default 10 minutes.
	Timeout time.Duration

	// Force, when true, permits non-fast-forward pulls — the local
	// reference is reset to match remote even on divergence. This
	// is a hard reset, NOT a rebase (despite the option's prior
	// name; go-git's PullOptions.Force has reset semantics).
	// Default false — divergent pulls return ErrNotFastForward and
	// leave the working tree untouched so the caller can decide.
	Force bool
}

// VerifyOptions controls signature verification on commits.
//
// In v0.1.x, VerifyCommit returns ErrSignatureVerificationNotImplemented
// when RequireSignature is true. The full implementation lands in v0.2.
type VerifyOptions struct {
	// AllowedSignersFile is the file path of an OpenSSH
	// allowed_signers-format file. Used for SSH-sig verification.
	AllowedSignersFile string

	// AllowedSigners is an inline list, preferred over the file
	// when both are set.
	AllowedSigners []SignerKey

	// RequireSignature, when true, fails verification on any
	// unsigned commit. When false, unsigned commits pass.
	RequireSignature bool
}
