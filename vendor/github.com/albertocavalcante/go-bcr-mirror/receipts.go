package bcrmirror

import "time"

// CloneReceipt summarises a Clone invocation. Used by callers (canopy
// sync runner, audit log writers) to emit structured records of the
// network operation.
type CloneReceipt struct {
	// SHA is the HEAD commit after the clone completes.
	SHA string

	// Bytes is an approximation of network bytes transferred.
	// go-git does not surface an exact byte count for clone; this
	// is derived from the resulting pack file size.
	Bytes int64

	// Duration is the wall-clock time of the clone operation.
	Duration time.Duration

	// Sparse is true when the clone applied sparse-checkout
	// patterns (CloneOptions.Sparse was non-empty).
	Sparse bool
}

// SyncReceipt summarises a Sync invocation.
type SyncReceipt struct {
	// FromSHA is the local HEAD before the sync. Empty string on
	// the very first sync of a fresh mirror.
	FromSHA string

	// ToSHA is the local HEAD after the sync. Equal to FromSHA when
	// UpToDate is true.
	ToSHA string

	// Commits is the number of new commits introduced by the sync.
	// Zero when UpToDate is true.
	Commits int

	// Bytes is approximate network bytes transferred during the
	// fetch.
	Bytes int64

	// Duration is the wall-clock time of the sync.
	Duration time.Duration

	// UpToDate is true when the local mirror was already at the
	// remote HEAD; nothing changed.
	UpToDate bool
}

// CommitInfo is one entry from LogChanges. Carries identity, time,
// message, and the changed-files subset filtered to the LogChanges
// module scope.
type CommitInfo struct {
	// SHA is the full commit hash.
	SHA string

	// AuthorName is the commit author's display name (e.g. "Jane
	// Doe"). Split from email so callers can format independently
	// without re-parsing.
	AuthorName string

	// AuthorEmail is the commit author's email address.
	AuthorEmail string

	// AuthorAt is the author timestamp (not committer timestamp;
	// the two diverge under rebases).
	AuthorAt time.Time

	// Message is the full commit message. Callers truncate for
	// display as needed.
	Message string

	// Files is the list of files changed in this commit that fall
	// inside the LogChanges module scope. Path is repo-relative
	// (e.g. modules/bazel_skylib/1.7.1/source.json).
	Files []string
}

// SignerKey is an entry in VerifyOptions.AllowedSigners. Identifies a
// public key authorised to sign upstream commits.
//
// Used by v0.2's VerifyCommit implementation. Defined in v0.1 so the
// public type surface is locked.
type SignerKey struct {
	// Fingerprint is the key fingerprint (SHA256:base64-shape for
	// SSH-sig; long-form for GPG).
	Fingerprint string

	// Format identifies the signature format: "ssh-rsa",
	// "ssh-ed25519", "gpg", "minisign".
	Format string

	// Comment is a human-readable label (e.g. an email address).
	Comment string
}
