// Package codenav lazily materializes canopy-stored module sources for
// per-(module, version) code navigation. Source bytes live as content-
// addressed tarball blobs under the mirror root; this package gunzips
// + untars them on demand into a stable cache directory, opens the
// resulting tree via os.Root for safe path-bounded reads, and pairs
// it with an in-memory understory.Index parsed from the matching SCIP
// blob.
//
// Designed for an HTTP handler hot path: parse + extract once per
// (module, version), then serve subsequent requests from cache. Bounded
// by a small LRU so a long-running canopy serving thousands of modules
// doesn't keep every source tree warm in memory.
package codenav

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/albertocavalcante/canopy/internal/archive"
)

// sourceDescriptor is the subset of source.json we need to locate the
// content-addressed blob and apply strip_prefix during extraction.
// Mirrors fetch.SourceJSON but kept local so this package has no inward
// dependency on internal/fetch (the layering keeps codenav reusable from
// places that don't speak BCR).
type sourceDescriptor struct {
	Integrity string `json:"integrity"`
	// StripPrefix is the leading directory inside the archive that
	// gets removed during extraction. Common for GitHub release
	// tarballs (e.g. "rules_go-0.50.1/").
	StripPrefix string `json:"strip_prefix"`
	// ArchiveType, when set, names the archive format authoritatively.
	// Empty falls back to URL-extension sniffing inside dispatch().
	// BCR convention: tar.gz / tgz / zip.
	ArchiveType string `json:"archive_type"`
	// URL is the upstream download URL; used as a fallback signal
	// for archive type when ArchiveType is empty (the typical case
	// for GitHub release archives whose extension is .tar.gz or .zip).
	URL string `json:"url"`
}

// unpackSource reads source.json at sourceJSONPath, decodes the SRI
// integrity into a sha256 hex name, locates the tarball under blobsDir,
// and extracts it (honoring strip_prefix) into destDir. Idempotent:
// when destDir already exists and is non-empty, returns nil without
// touching disk — the caller treats it as cache-hit.
//
// Patches declared in source.json.patches are intentionally NOT applied.
// V1 code-nav navigates raw upstream sources; patched MODULE.bazel
// differences would require a bigger plumbing story (canopy's mirror
// stores the patches under modules/<m>/<v>/patches/, but applying them
// post-extract isn't worth the complexity for navigation).
// completeSentinel marks a finished extract. Written as the LAST step
// of a successful unpackSource and consulted on entry — the "directory
// non-empty" heuristic the previous version used would wrongly trust a
// half-written tree from a crashed process. The sentinel name leads
// with a dot so it never collides with a real Bazel source path.
const completeSentinel = ".canopy-unpack-complete"

// MaterializeSource ensures the unpacked source tree for (module,
// version) exists under cacheDir/<module>/<version>/ and returns its
// absolute path. Idempotent — re-running on an already-materialized
// coordinate is a stat + early-return.
//
// Exported so canopy.Service.Summary (and any other future caller
// that needs a path to the unpacked source without going through the
// SCIP-loading Resolver) can trigger the same on-demand unpack code
// codenav uses for browse requests.
func MaterializeSource(mirrorDir, cacheDir, module, version string) (string, error) {
	sourceJSON := filepath.Join(mirrorDir, "modules", module, version, "source.json")
	blobsDir := filepath.Join(mirrorDir, "blobs")
	destDir := filepath.Join(cacheDir, module, version)
	if err := unpackSource(blobsDir, sourceJSON, destDir); err != nil {
		return "", err
	}
	return destDir, nil
}

func unpackSource(blobsDir, sourceJSONPath, destDir string) error {
	// Cache-hit only when the explicit completion sentinel is present.
	// A partial extract (some files written, sentinel missing) forces a
	// redo: we wipe destDir so the new extract isn't contaminated by
	// stale files from the crashed run.
	if _, err := os.Stat(filepath.Join(destDir, completeSentinel)); err == nil {
		return nil
	}
	if _, err := os.Stat(destDir); err == nil {
		// Partial state — clear it before re-extracting. RemoveAll is a
		// no-op when destDir doesn't exist.
		if err := os.RemoveAll(destDir); err != nil {
			return fmt.Errorf("codenav: clear partial extract: %w", err)
		}
	}

	src, err := readSourceDescriptor(sourceJSONPath)
	if err != nil {
		return err
	}
	if src.Integrity == "" {
		return errors.New("codenav: source.json missing integrity")
	}
	hexName, err := integrityToHex(src.Integrity)
	if err != nil {
		return fmt.Errorf("codenav: decode integrity: %w", err)
	}

	blobPath := filepath.Join(blobsDir, hexName)
	f, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("codenav: open blob %s: %w", hexName, err)
	}
	defer f.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("codenav: mkdir dest: %w", err)
	}

	if err := extractByKind(f, destDir, src); err != nil {
		return fmt.Errorf("codenav: extract: %w", err)
	}
	// Sentinel marks the extract as fully written. Anything reading
	// destDir later (cache probe, debugging) can rely on this as the
	// single source of truth for "did the extract finish?"
	sentinel := filepath.Join(destDir, completeSentinel)
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		return fmt.Errorf("codenav: write completion sentinel: %w", err)
	}
	return nil
}

func readSourceDescriptor(path string) (*sourceDescriptor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("codenav: read source.json: %w", err)
	}
	var s sourceDescriptor
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("codenav: parse source.json: %w", err)
	}
	return &s, nil
}

// integrityToHex converts an SRI integrity string ("sha256-<base64>")
// into the hex-encoded sha256 used as canopy's content-addressed blob
// name. Only sha256 is supported — every BCR module in the wild uses
// it, and supporting alternates here would mean teaching the mirror
// writer about them too.
func integrityToHex(integrity string) (string, error) {
	const prefix = "sha256-"
	if !strings.HasPrefix(integrity, prefix) {
		return "", fmt.Errorf("integrity %q: only sha256- supported", integrity)
	}
	b, err := base64.StdEncoding.DecodeString(integrity[len(prefix):])
	if err != nil {
		return "", fmt.Errorf("integrity base64: %w", err)
	}
	if len(b) != 32 {
		return "", fmt.Errorf("integrity sha256 has %d bytes, want 32", len(b))
	}
	return hex.EncodeToString(b), nil
}

// extractByKind picks the right extractor based on source.json's
// archive_type field, falling back to URL extension sniffing when
// archive_type is empty (the typical BCR shape — release tarballs
// from GitHub usually only have a .tar.gz or .zip suffix and no
// explicit declaration). Defers to internal/archive for actual
// extraction so codenav and the rest of canopy share one
// implementation per format.
//
// Recognized formats: tar.gz / tgz (default) and zip. Anything
// else surfaces as an error so an operator can see exactly what
// went wrong instead of getting a misleading "gzip: invalid
// header" from a wrong-format attempt.
func extractByKind(r io.Reader, destDir string, src *sourceDescriptor) error {
	kind := detectArchiveKind(src.ArchiveType, src.URL)
	switch kind {
	case archiveZip:
		_, err := archive.ExtractZip(r, destDir, src.StripPrefix, archive.MaxExtractBytes)
		return err
	case archiveTarGz:
		_, err := archive.ExtractTarGz(r, destDir, src.StripPrefix, archive.MaxExtractBytes)
		return err
	}
	return fmt.Errorf("codenav: unsupported archive_type %q (url=%q); supported: tar.gz, tgz, zip", src.ArchiveType, src.URL)
}

type archiveKind int

const (
	archiveUnknown archiveKind = iota
	archiveTarGz
	archiveZip
)

// detectArchiveKind mirrors internal/resolve's detection so codenav
// agrees with the ingest-time decision about each module's format.
// archive_type takes precedence; URL suffix is the fallback. Unknown
// declarations bubble back to extractByKind as an error.
func detectArchiveKind(declared, url string) archiveKind {
	switch strings.ToLower(declared) {
	case "tar.gz", "tgz":
		return archiveTarGz
	case "zip":
		return archiveZip
	case "":
		// Fall through to URL sniffing.
	default:
		return archiveUnknown
	}
	lower := strings.ToLower(url)
	// Strip query string / fragment — GitHub release URLs sometimes
	// carry tracking params that would defeat a naïve HasSuffix.
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return archiveZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveTarGz
	default:
		// Default to tar.gz: the format BCR uses most often. The
		// extractor will surface a clear gzip-header error if the
		// guess turns out wrong.
		return archiveTarGz
	}
}

// Note: codenav's extractTarGz was retired in favor of the shared
// internal/archive package, which now handles both tar.gz and zip
// behind extractByKind() above. The legacy implementation enforced
// path-safety rules; those have been folded into archive's own
// hardening so the centralized extractors are the single source of
// truth.
