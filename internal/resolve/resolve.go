// Package resolve materializes a Bazel module from a BCR-shape registry
// into a local temp directory, ready for assay introspection.
//
// The full flow:
//   1. Fetch source.json from the registry.
//   2. Stream-download the archive named by source.json.url.
//   3. Verify SHA256 integrity (sha256-<base64> SRI).
//   4. Extract honoring strip_prefix.
//   5. If the extracted root lacks a MODULE.bazel, fetch BCR's
//      standalone copy (modules/<m>/<v>/MODULE.bazel) and write it.
//      Older modules in BCR commonly don't bundle MODULE.bazel in
//      their source tarball — the registry-side copy is authoritative.
//   6. Return the destination directory.
//
// Callers MUST defer Materialized.Cleanup to remove the temp dir.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/albertocavalcante/bzlhub/internal/archive"
	"github.com/albertocavalcante/bzlhub/internal/fetch"
)

// Materialized describes the on-disk state of a freshly-resolved module.
type Materialized struct {
	Dir         string            // absolute path to the extracted module root
	Source      *fetch.SourceJSON // wire copy of source.json, useful for downstream
	SourceBytes []byte            // raw source.json bytes (verbatim), nil if not captured
	ModuleBytes []byte            // raw MODULE.bazel bytes (verbatim), nil if not captured
	Bytes       int64             // sum of file sizes written into Dir
	cleanup     func() error
}

// Cleanup removes the temp directory. Safe to call multiple times.
func (m *Materialized) Cleanup() error {
	if m == nil || m.cleanup == nil {
		return nil
	}
	err := m.cleanup()
	m.cleanup = nil
	return err
}

// Options configures resolution. Most callers want the zero value.
type Options struct {
	// Tee, if non-nil, receives the verified archive bytes alongside extraction.
	// Used by the mirror to capture an on-disk copy without buffering or
	// re-downloading. Tee receives only bytes that have been integrity-tracked;
	// the caller MUST treat the tee'd content as untrusted until resolve
	// returns successfully (a mid-stream SRI mismatch will abort, but the
	// already-tee'd prefix may be on disk — the mirror handles cleanup via
	// BlobSink.Abort on error paths).
	Tee io.Writer

	// CaptureBytes, if true, populates Materialized.SourceBytes and
	// ModuleBytes with the verbatim wire bytes of source.json and
	// MODULE.bazel. The mirror needs these to persist them under the
	// destination tree byte-for-byte.
	CaptureBytes bool
}

// archiveKind enumerates the source archive formats we know how to
// extract. BCR currently uses tar.gz and zip in practice; everything
// else (7z, rar, tar.bz2) is too rare to bother with.
type archiveKind int

const (
	archiveKindTarGz archiveKind = iota
	archiveKindZip
)

// detectArchiveKind picks an archive extractor based on source.json's
// declared archive_type if set, falling back to the URL extension. BCR
// modules typically leave archive_type empty and rely on the URL
// suffix (e.g., GitHub release URLs ending in .zip or .tar.gz).
func detectArchiveKind(declared, url string) (archiveKind, error) {
	switch strings.ToLower(declared) {
	case "tar.gz", "tgz":
		return archiveKindTarGz, nil
	case "zip":
		return archiveKindZip, nil
	case "":
		// Fall through to URL sniffing.
	default:
		return 0, fmt.Errorf("archive_type %q not supported (need tar.gz, tgz, or zip)", declared)
	}
	lower := strings.ToLower(url)
	// Strip any querystring/fragment before checking the extension —
	// GitHub release URLs sometimes carry tracking params.
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return archiveKindZip, nil
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveKindTarGz, nil
	default:
		// Unknown extension — default to tar.gz so the existing flow
		// stays preserved for older / non-extensioned URLs. The
		// extractor will surface a clear gzip error if that guess is
		// wrong.
		return archiveKindTarGz, nil
	}
}

// FromRegistry fetches and extracts a (module, version) from a BCR-shape
// HTTP registry. The returned Materialized is rooted in os.TempDir() under
// a "canopy-resolve-*" prefix; caller MUST defer Cleanup().
func FromRegistry(ctx context.Context, registryURL, module, version string) (*Materialized, error) {
	c := fetch.NewClient()
	return FromRegistryWithClient(ctx, c, registryURL, module, version, Options{})
}

// FromRegistryWithClient is FromRegistry with an injectable HTTP client
// (for tests against an httptest.Server) and optional Options.
func FromRegistryWithClient(ctx context.Context, c *fetch.Client, registryURL, module, version string, opts Options) (*Materialized, error) {
	srcBytes, err := c.GetSourceJSONBytes(ctx, registryURL, module, version)
	if err != nil {
		return nil, err
	}
	src, err := fetch.ParseSourceJSON(srcBytes)
	if err != nil {
		return nil, fmt.Errorf("parse source.json %s@%s: %w", module, version, err)
	}

	var modBytes []byte
	if opts.CaptureBytes {
		modBytes, err = c.GetModuleBazel(ctx, registryURL, module, version)
		if err != nil {
			return nil, err
		}
	}

	// Lazy MODULE.bazel fallback — fetched only if the tarball doesn't
	// bundle one. Closes over (registryURL, module, version) so callers
	// of FromSource that hit the same metadata path don't have to wire
	// a separate fetcher.
	fallback := func(ctx context.Context) ([]byte, error) {
		return c.GetModuleBazel(ctx, registryURL, module, version)
	}
	return FromSource(ctx, c, src, srcBytes, modBytes, fallback, opts)
}

// FromSource materializes a module given a pre-loaded source.json
// (parsed + raw bytes) and optional MODULE.bazel bytes. Used by
// callers that already have the metadata on hand (e.g., the worktree
// watcher reading from disk) and want to skip the HTTP round-trip
// for those small files.
//
// modBytesFallback is invoked only if the extracted tarball lacks a
// MODULE.bazel AND modBytes is nil. It exists because many older
// BCR modules ship MODULE.bazel as a sibling file on the registry,
// not inside the archive — callers without that fallback get
// "MODULE.bazel: no such file" failures on assay. Pass nil to
// disable the fallback (the caller will have to handle the missing-
// file case themselves).
func FromSource(
	ctx context.Context,
	c *fetch.Client,
	src *fetch.SourceJSON,
	srcBytes []byte,
	modBytes []byte,
	modBytesFallback func(ctx context.Context) ([]byte, error),
	opts Options,
) (*Materialized, error) {
	if src == nil {
		return nil, errors.New("resolve: src (source.json) is required")
	}
	if src.Type != "" && src.Type != "archive" {
		return nil, fmt.Errorf("source.json type %q not supported (need 'archive')", src.Type)
	}
	if src.URL == "" {
		return nil, errors.New("source.json missing url")
	}
	kind, err := detectArchiveKind(src.ArchiveType, src.URL)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "canopy-resolve-")
	if err != nil {
		return nil, fmt.Errorf("mkdir temp: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(dir) }

	body, vr, err := c.FetchArchive(ctx, src)
	if err != nil {
		_ = cleanup()
		return nil, err
	}
	defer body.Close()

	// If teeing, every byte read goes to opts.Tee too. ExtractTarGz reads
	// from the resulting reader, so the tee sees the same stream the
	// extractor sees, post-integrity-tracking.
	var src2 io.Reader = body
	if opts.Tee != nil {
		src2 = io.TeeReader(body, opts.Tee)
	}

	var n int64
	switch kind {
	case archiveKindZip:
		n, err = archive.ExtractZip(src2, dir, src.StripPrefix, archive.MaxExtractBytes)
	default:
		n, err = archive.ExtractTarGz(src2, dir, src.StripPrefix, archive.MaxExtractBytes)
	}
	if err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("extract: %w", err)
	}
	if err := vr.Verify(); err != nil {
		_ = cleanup()
		return nil, fmt.Errorf("integrity: %w", err)
	}

	// MODULE.bazel fallback (see Why above).
	modPath := filepath.Join(dir, "MODULE.bazel")
	if _, statErr := os.Stat(modPath); errors.Is(statErr, fs.ErrNotExist) {
		if modBytes == nil && modBytesFallback != nil {
			modBytes, err = modBytesFallback(ctx)
			if err != nil {
				_ = cleanup()
				return nil, fmt.Errorf("MODULE.bazel fallback fetch: %w", err)
			}
		}
		if modBytes != nil {
			if err := os.WriteFile(modPath, modBytes, 0o644); err != nil {
				_ = cleanup()
				return nil, fmt.Errorf("MODULE.bazel fallback write: %w", err)
			}
		}
	}

	m := &Materialized{
		Dir:     dir,
		Source:  src,
		Bytes:   n,
		cleanup: cleanup,
	}
	if opts.CaptureBytes {
		m.SourceBytes = srcBytes
		m.ModuleBytes = modBytes
	}
	return m, nil
}
