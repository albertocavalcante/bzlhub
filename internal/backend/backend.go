// Package backend defines the storage interface that canopy speaks to. The
// HTTP server is a thin BCR-protocol projection of this interface. Operators
// pick a Backend impl based on their deployment shape — filesystem, S3,
// SQLite-embedded, git, OCI Distribution, Postgres — without changes to the
// server, CLI, or UI.
package backend

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Backend methods when the requested artifact
// does not exist. Handlers map this to HTTP 404.
var ErrNotFound = errors.New("not found")

// ErrUpstreamUnavailable is returned by federation-aware Backend impls
// (Cascade) when every configured upstream failed for non-404 reasons
// (timeout, 5xx, network error) and we can't authoritatively say the
// artifact doesn't exist. Handlers map this to HTTP 503 + Retry-After,
// which Bazel honors (it retries on 5xx; it doesn't retry on 404, so
// 503 is the correct "try again later" signal).
var ErrUpstreamUnavailable = errors.New("all upstreams unavailable")

// Backend is the storage abstraction. All methods are read-only for Phase 0;
// write operations will be added when canopy starts publishing.
//
// Returned ReadClosers must be Closed by the caller.
type Backend interface {
	// GetBazelRegistryJSON returns the bytes of /bazel_registry.json.
	GetBazelRegistryJSON(ctx context.Context) (io.ReadCloser, error)

	// GetMetadata returns /modules/<name>/metadata.json.
	GetMetadata(ctx context.Context, module string) (io.ReadCloser, error)

	// GetModuleBazel returns /modules/<name>/<version>/MODULE.bazel.
	GetModuleBazel(ctx context.Context, module, version string) (io.ReadCloser, error)

	// GetSourceJSON returns /modules/<name>/<version>/source.json.
	GetSourceJSON(ctx context.Context, module, version string) (io.ReadCloser, error)

	// GetPatch returns /modules/<name>/<version>/patches/<filename>.
	GetPatch(ctx context.Context, module, version, filename string) (io.ReadCloser, error)

	// GetOverlay returns /modules/<name>/<version>/overlay/<path>.
	GetOverlay(ctx context.Context, module, version, path string) (io.ReadCloser, error)

	// GetBlob returns an opaque blob (typically a source tarball) by key.
	// The HTTP server exposes these at /blobs/<key> for source.json's
	// "url" field to reference.
	GetBlob(ctx context.Context, key string) (io.ReadCloser, error)
}
