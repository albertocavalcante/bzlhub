// Package publish defines the write-side abstraction for canopy's
// registry. The Backend interface (internal/backend) is read-only;
// Publisher is its writing counterpart.
//
// Operators pick a Publisher impl based on deployment shape:
//
//   - FilesystemPublisher writes BCR-shape directly to a destination
//     directory. The default; what `ingest --mirror-to` produces today.
//
//   - GitDirectPublisher (future) commits into a git working tree and
//     pushes to the default branch of a configured registry repo.
//
//   - GitPRPublisher (future) commits into a branch, pushes, and opens
//     a pull request via a Forge implementation.
//
// All three speak the same Publisher interface; callers don't change
// shape between them.
package publish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Publisher writes a module version into the registry. Concrete
// implementations vary in how the bytes ultimately land (filesystem,
// git repo, git repo + PR), but the call sequence is the same.
//
// The expected sequence for one module version:
//
//  1. sink, err := pub.BeginBlob(ctx, srcURL)
//  2. stream tarball bytes through sink (integrity verified by caller
//     or by the stream itself; the sink computes content address)
//  3. ref, err := sink.Close()
//  4. receipt, err := pub.Publish(ctx, PublishRequest{..., Blob: ref})
//
// The streaming step exists because tarballs can be tens of MB; we
// don't want to buffer them in memory just to compute SHA-256.
type Publisher interface {
	// BeginBlob opens a streaming sink for the module's source archive.
	// srcURL is informational (telemetry, logging); it does not
	// determine the final on-disk location, which is content-addressed.
	BeginBlob(ctx context.Context, srcURL string) (BlobSink, error)

	// Publish materializes a module version. The Blob in req must
	// already be finalized (BlobSink.Close called and ref captured).
	Publish(ctx context.Context, req PublishRequest) (Receipt, error)
}

// BlobSink streams bytes for one module's source archive. Implementations
// compute content-addressed identity as bytes flow through.
type BlobSink interface {
	io.Writer

	// Close finalizes the blob and returns a reference to it. After
	// a successful Close the sink must not be written to again.
	Close() (BlobRef, error)

	// Abort discards the in-flight blob without committing. Safe to
	// call after Close; the second call is a no-op. Always safe to
	// defer at the call site.
	Abort()
}

// BlobRef identifies a finalized blob in the registry's blob store.
type BlobRef struct {
	// Key is implementation-defined. For FilesystemPublisher it is
	// the absolute on-disk path of the content-addressed file. For
	// git-backed publishers it will be a content hash that the
	// configured object store resolves.
	Key string

	// Integrity is the SRI-formatted SHA-256 ("sha256-<base64>").
	Integrity string

	// Bytes is the size of the blob.
	Bytes int64
}

// PublishRequest carries everything needed to land one module version
// in the registry. The required fields are Module, Version, SourceJSON,
// and Blob; the rest are optional.
type PublishRequest struct {
	Module  string
	Version string

	// SourceJSON is the bytes of source.json describing where Bazel
	// should fetch the archive and how to verify it. Required.
	SourceJSON []byte

	// ModuleBazel is the verbatim MODULE.bazel from the module's root.
	// Optional — modules that publish without one (rare) skip this.
	ModuleBazel []byte

	// UpstreamMetadata is the raw bytes of the upstream registry's
	// metadata.json for this module, when available. Selected fields
	// (homepage, maintainers, repository, yanked_versions) are lifted
	// into the local metadata.json on merge. Optional; nil means
	// "no upstream to lift from."
	UpstreamMetadata []byte

	// Blob references the already-staged source archive. Required.
	Blob BlobRef

	// Requester identifies the human (or bot) who triggered this
	// publish. Used by git-backed publishers as the commit Author;
	// FilesystemPublisher ignores it. Required for GitDirectPublisher
	// and GitPRPublisher.
	Requester Identity

	// SourceURL is the upstream archive URL the blob was fetched from.
	// Surfaced in commit-message trailers and (eventually) PR bodies.
	// Optional — empty means "unknown / first-publication."
	SourceURL string
}

// Identity is a Git-style {Name, Email} pair. Used for commit Author
// and Committer attribution.
type Identity struct {
	Name  string
	Email string
}

// IsZero reports whether the identity is empty.
func (i Identity) IsZero() bool { return i.Name == "" && i.Email == "" }

// String renders an identity in Git's "Name <email>" form. Returns ""
// for the zero value.
func (i Identity) String() string {
	if i.IsZero() {
		return ""
	}
	return i.Name + " <" + i.Email + ">"
}

// Receipt summarizes what a Publish call produced. The fields populated
// depend on the strategy.
type Receipt struct {
	// Strategy is "filesystem" for FilesystemPublisher, "git-direct"
	// or "git-pr" for the future git-backed variants.
	Strategy string

	// DiskPath is the directory the module version was written under,
	// for filesystem strategies.
	DiskPath string

	// Commit is the SHA produced by git-backed strategies. Empty for
	// filesystem strategy.
	Commit string

	// PRURL and PRNumber are populated by git-pr strategy when the
	// forge OpenPR call succeeds. Empty / zero otherwise.
	PRURL    string
	PRNumber int

	// Diff is a short human-readable summary suitable for CLI/UI/logs.
	Diff string

	// PublishedAt is when this receipt was produced.
	PublishedAt time.Time
}

// ErrMissingRequiredField is returned by Publish when PublishRequest
// is missing a required field.
var ErrMissingRequiredField = errors.New("publish: missing required field")

// ValidateRequest checks the fields every Publisher impl requires:
// Module, Version, SourceJSON, and Blob.Integrity. Impl-specific
// requirements (e.g. GitDirectPublisher's Requester) are validated
// by the impl in addition to calling this.
func ValidateRequest(req PublishRequest) error {
	switch {
	case req.Module == "":
		return fmt.Errorf("%w: module", ErrMissingRequiredField)
	case req.Version == "":
		return fmt.Errorf("%w: version", ErrMissingRequiredField)
	case len(req.SourceJSON) == 0:
		return fmt.Errorf("%w: source.json", ErrMissingRequiredField)
	case req.Blob.Integrity == "":
		return fmt.Errorf("%w: blob.integrity", ErrMissingRequiredField)
	}
	return nil
}
