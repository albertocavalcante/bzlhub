package httpstore

import "errors"

// Sentinel errors. Callers compare with errors.Is. The library
// wraps these with operator-meaningful context (URL, module,
// version) at the call site, so the unwrapped sentinel is the
// stable predicate even when the wrapped message changes.
var (
	// ErrInvalidOptions is returned by New when NewOptions is
	// missing a required field (BaseURL, Auth, HTTP) or has an
	// invalid value (malformed URL).
	ErrInvalidOptions = errors.New("httpstore: invalid options")

	// ErrModuleNotFound is returned by reads + lists when the
	// requested module's metadata.json is absent from the store.
	// Distinguished from a transport error so a caller can decide
	// "this module is genuinely not here" vs "the store is down".
	ErrModuleNotFound = errors.New("httpstore: module not found")

	// ErrVersionNotFound is returned by version-scoped reads when
	// the specific version directory is absent under an existing
	// module. metadata.json present, source.json/MODULE.bazel for
	// this version missing.
	ErrVersionNotFound = errors.New("httpstore: version not found")

	// ErrPatchNotFound is returned by ReadPatch when the named
	// patch is absent under an existing version directory.
	// Distinct from ErrVersionNotFound — operators want to
	// distinguish "the whole version is gone" from "this one
	// patch was removed or renamed upstream".
	ErrPatchNotFound = errors.New("httpstore: patch not found")

	// ErrOverlayNotFound is returned by ReadOverlay when the
	// requested overlay path is absent under an existing
	// version. Overlays are BCR's mechanism for patching files
	// the upstream archive doesn't expose; the path can be any
	// nested relative path under modules/<m>/<v>/overlay/.
	ErrOverlayNotFound = errors.New("httpstore: overlay not found")

	// ErrBlobNotFound is returned by ReadBlob when the named
	// blob key is absent under <root>/blobs/. Separate from
	// ErrModuleNotFound so consumers can route blob-vs-metadata
	// misses to different recovery paths.
	ErrBlobNotFound = errors.New("httpstore: blob not found")

	// ErrRegistryJSONNotFound is returned by ReadBazelRegistryJSON
	// when the BCR root marker file is absent. Almost certainly
	// indicates a misconfigured BaseURL (pointing at a non-BCR
	// tree) rather than a transient condition.
	ErrRegistryJSONNotFound = errors.New("httpstore: bazel_registry.json not found")

	// ErrIndexUnreadable is returned by Layout implementations
	// when the index discovery mechanism itself fails (parse
	// error, malformed _canopy_index.json, etc.). Distinct from
	// "the module isn't in the index" — that's ErrModuleNotFound.
	ErrIndexUnreadable = errors.New("httpstore: index unreadable")

	// ErrUpstreamStatus is returned when the upstream HTTP store
	// responds with a non-OK status that isn't a recognised
	// not-found shape (404). Wraps the status code in the error
	// message for diagnostics.
	ErrUpstreamStatus = errors.New("httpstore: upstream non-ok status")

	// ErrConflict is returned by Write* methods when the upstream
	// responds 409 Conflict or 412 Precondition Failed — typically
	// triggered by an If-Match header that didn't match the
	// upstream's current ETag (concurrent publisher won the race).
	// Callers should re-read the current version and decide whether
	// to retry the write against the new ETag.
	ErrConflict = errors.New("httpstore: write conflict (412/409)")

	// ErrUnauthorized is returned by any method when the upstream
	// responds 401 Unauthorized — the configured Auth's credential
	// was rejected. Callers should rotate the credential rather
	// than retry.
	ErrUnauthorized = errors.New("httpstore: upstream rejected credential (401)")

	// ErrForbidden is returned by any method when the upstream
	// responds 403 Forbidden — credential was accepted but the
	// identity lacks permission for this path. Distinct from 401:
	// 401 says "who are you?", 403 says "I know you and no."
	ErrForbidden = errors.New("httpstore: upstream forbade access (403)")
)
