package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/albertocavalcante/go-bcr-httpstore"
)

// BackendSource implements Source over a live httpstore.Backend.
// Designed for HQ-side workflows where canopy hasn't pre-synced
// the mirror to disk — bundle directly from the upstream HTTP
// store via the Backend's configured auth + cache.
//
// Unlike FilesystemSource, BackendSource is BCR-aware: List
// walks Backend.ListModules / ListVersions / ReadSourceJSON to
// build the path set, since plain HTTP exposes no "list files
// under modules/<m>/<v>/" primitive. Open dispatches relPath
// to the appropriate typed Backend.ReadX method.
//
// Reads route through the Backend's configured Cache (when set),
// so re-bundling immediately after a previous bundle is cheap on
// the upstream. Operators bundling repeatedly from the same
// source should configure a MemoryCache on the Backend to avoid
// re-fetching unchanged content.
type BackendSource struct {
	backend *httpstore.Backend
}

// Compile-time guard.
var _ Source = (*BackendSource)(nil)

// NewBackendSource wraps a httpstore.Backend. The Backend MUST be
// fully configured (BaseURL + Auth + HTTP + Layout); see
// httpstore.New. Nil Backend returns ErrInvalidBundle.
//
// The Backend's Layout is what List walks for module enumeration —
// for a canopy-shape store, pair with a canopyindex.Layout; for
// nginx-fronted stores, pair with httpstore.HTMLAutoindex; for
// Artifactory, pair with go-bcr-artifactory.Layout.
func NewBackendSource(backend *httpstore.Backend) (*BackendSource, error) {
	if backend == nil {
		return nil, fmt.Errorf("%w: backend is required", ErrInvalidBundle)
	}
	return &BackendSource{backend: backend}, nil
}

// Backend exposes the underlying httpstore.Backend. Useful for
// diagnostics; production code rarely needs it after construction.
func (s *BackendSource) Backend() *httpstore.Backend { return s.backend }

// sourceJSON is a partial parse of BCR's source.json. We only care
// about the fields that surface additional bundle-eligible paths.
// Other fields (url, archive_type, strip_prefix, etc.) pass through
// unread — the bundle archives the raw bytes; consumers parse on
// their own.
type sourceJSON struct {
	// Integrity is the SRI-format hash of the source archive
	// ("sha256-base64..."). The blob's bundle key is derived from
	// this — base64-encoded SRI is what canopy and BCR consumers
	// already pass to ReadBlob.
	Integrity string `json:"integrity"`

	// Patches is a map of patch-name → SRI hash. Each key becomes
	// a bundle-eligible path under modules/<m>/<v>/patches/.
	Patches map[string]string `json:"patches"`

	// Overlay is a map of relative-path → SRI hash. Each key
	// becomes a bundle-eligible path under modules/<m>/<v>/overlay/.
	Overlay map[string]string `json:"overlay"`
}

// List walks the Backend's surface and returns every BCR-shape
// relative path:
//
//   - bazel_registry.json (always)
//   - modules/<m>/metadata.json (per Backend.ListModules)
//   - modules/<m>/<v>/source.json + MODULE.bazel (per ListVersions)
//   - modules/<m>/<v>/patches/<p> (per source.json patches)
//   - modules/<m>/<v>/overlay/<o> (per source.json overlay)
//   - blobs/sha256-... (deduplicated across versions, derived
//     from source.json integrity fields)
//
// Performs one ListModules + one ListVersions per module + one
// ReadSourceJSON per version. For a real-sized BCR mirror this is
// thousands of HTTP requests — configure the Backend with a
// MemoryCache to amortise repeated bundle runs.
//
// Returns whatever Backend errors surface. A 404 on a specific
// version's source.json bubbles up as httpstore.ErrVersionNotFound;
// callers can decide whether to skip-and-warn or fail-hard at a
// higher layer.
func (s *BackendSource) List(ctx context.Context) ([]string, error) {
	paths := []string{"bazel_registry.json"}
	blobKeys := map[string]struct{}{}

	modules, err := s.backend.ListModules(ctx)
	if err != nil {
		return nil, fmt.Errorf("bundle: list modules: %w", err)
	}

	for _, module := range modules {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		paths = append(paths, path.Join("modules", module, "metadata.json"))

		versions, err := s.backend.ListVersions(ctx, module)
		if err != nil {
			return nil, fmt.Errorf("bundle: list versions %s: %w", module, err)
		}
		for _, version := range versions {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			paths = append(paths,
				path.Join("modules", module, version, "source.json"),
				path.Join("modules", module, version, "MODULE.bazel"),
			)
			// Parse source.json to find patches + overlay + blob ref.
			srcBytes, err := s.backend.ReadSourceJSON(ctx, module, version)
			if err != nil {
				return nil, fmt.Errorf("bundle: read source.json for %s@%s: %w",
					module, version, err)
			}
			var srcDoc sourceJSON
			if err := json.Unmarshal(srcBytes, &srcDoc); err != nil {
				return nil, fmt.Errorf("%w: parse source.json %s@%s: %v",
					ErrInvalidBundle, module, version, err)
			}
			// Patches.
			for patchName := range srcDoc.Patches {
				paths = append(paths,
					path.Join("modules", module, version, "patches", patchName))
			}
			// Overlay.
			for overlayPath := range srcDoc.Overlay {
				paths = append(paths,
					path.Join("modules", module, version, "overlay", overlayPath))
			}
			// Blob.
			if key := blobKeyFromIntegrity(srcDoc.Integrity); key != "" {
				blobKeys[key] = struct{}{}
			}
		}
	}

	// Append deduplicated blob paths.
	for key := range blobKeys {
		paths = append(paths, "blobs/"+key)
	}

	sort.Strings(paths)
	return paths, nil
}

// Open returns the bytes at relPath as a streaming reader.
// Pattern-matches relPath to the appropriate Backend.ReadX method.
// For non-blob paths (small JSON / MODULE.bazel / patches /
// overlay), wraps the []byte return value in an io.NopCloser. For
// blobs, returns the streaming io.ReadCloser from
// Backend.ReadBlob unchanged.
//
// Returns ErrNotFound on path-pattern mismatch (relPath isn't a
// BCR-shape path) — distinct from a "the resource doesn't exist
// at upstream" 404 (which bubbles up wrapped as the appropriate
// httpstore.ErrXNotFound).
func (s *BackendSource) Open(ctx context.Context, relPath string) (io.ReadCloser, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// bazel_registry.json
	if relPath == "bazel_registry.json" {
		body, err := s.backend.ReadBazelRegistryJSON(ctx)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	// blobs/<key>
	if strings.HasPrefix(relPath, "blobs/") {
		key := strings.TrimPrefix(relPath, "blobs/")
		return s.backend.ReadBlob(ctx, key)
	}

	// modules/<m>/...
	if strings.HasPrefix(relPath, "modules/") {
		rest := strings.TrimPrefix(relPath, "modules/")
		segs := strings.SplitN(rest, "/", 4)
		// segs layouts:
		// ["m", "metadata.json"]                       → ReadMetadataJSON
		// ["m", "v", "source.json"]                    → ReadSourceJSON
		// ["m", "v", "MODULE.bazel"]                   → ReadModuleBazel
		// ["m", "v", "patches", "<patchName>"]         → ReadPatch
		// ["m", "v", "overlay", "<overlay-rel-path>"]  → ReadOverlay
		switch {
		case len(segs) == 2 && segs[1] == "metadata.json":
			body, err := s.backend.ReadMetadataJSON(ctx, segs[0])
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(body)), nil
		case len(segs) == 3 && segs[2] == "source.json":
			body, err := s.backend.ReadSourceJSON(ctx, segs[0], segs[1])
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(body)), nil
		case len(segs) == 3 && segs[2] == "MODULE.bazel":
			body, err := s.backend.ReadModuleBazel(ctx, segs[0], segs[1])
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(body)), nil
		case len(segs) == 4 && segs[2] == "patches":
			body, err := s.backend.ReadPatch(ctx, segs[0], segs[1], segs[3])
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(body)), nil
		case len(segs) == 4 && segs[2] == "overlay":
			body, err := s.backend.ReadOverlay(ctx, segs[0], segs[1], segs[3])
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}

	return nil, fmt.Errorf("%w: %s (unrecognised BCR-shape path)", ErrNotFound, relPath)
}

// blobKeyFromIntegrity translates BCR's SRI-format integrity hash
// into a bundle blob key. BCR's integrity field is e.g.
// "sha256-Q3Hh3z4f...=" (algorithm-hyphen-base64). The blob's
// canonical key — the path under blobs/ in both BCR mirror and
// bundle — uses the same SRI form verbatim.
//
// Returns empty when integrity is empty or the algorithm prefix
// isn't recognised. Currently we accept only "sha256-..." (BCR's
// universal default); future versions might add sha384/sha512.
func blobKeyFromIntegrity(integrity string) string {
	if integrity == "" {
		return ""
	}
	if !strings.HasPrefix(integrity, "sha256-") {
		// Unknown / unsupported integrity algorithm — skip rather
		// than fail, since the manifest's checksums layer covers
		// integrity at the bundle level anyway. Operators with
		// non-sha256 source.json fields are already off the BCR
		// happy path.
		return ""
	}
	return integrity
}

// ensureErrorsImport keeps the errors import grounded for future
// errors.Is dispatch logic that v0.1.x extensions are likely to
// add. (No-op today; the var prevents the unused-import lint
// from fighting a future refactor.)
var _ = errors.Is
