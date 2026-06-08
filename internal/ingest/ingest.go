// Package ingest converts a module source directory into a stored
// ModuleReport via assay (the introspection engine).
package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/albertocavalcante/assay"
	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/external"
	"github.com/albertocavalcante/bzlhub/internal/fetch"
	"github.com/albertocavalcante/bzlhub/internal/resolve"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Analyze runs assay on the module rooted at dir and returns the
// report. No store side effect — callers that have a canonical
// coordinate (e.g. registry-mode ingest) must override Name/Version
// before writing, since BCR tarballs commonly ship a placeholder
// version in MODULE.bazel.
func Analyze(ctx context.Context, dir string) (*report.ModuleReport, error) {
	r, err := assay.Analyze(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("analyze %s: %w", dir, err)
	}
	if r.Name == "" {
		return nil, fmt.Errorf("analyze %s: module has no name (missing module(...) call?)", dir)
	}
	if r.Version == "" {
		r.Version = "HEAD"
	}
	return r, nil
}

// FromDir analyzes a module rooted at dir (containing MODULE.bazel) and
// writes the resulting report to s. Returns the report so callers can
// inspect / log / route on it.
func FromDir(ctx context.Context, s *store.Store, dir string) (*report.ModuleReport, error) {
	r, err := Analyze(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := s.WriteReport(ctx, r); err != nil {
		return nil, fmt.Errorf("write report %s@%s: %w", r.Name, r.Version, err)
	}
	// External URL surface — non-fatal. If analysis fails (e.g., a Bazel
	// feature the analyzer doesn't yet support), the rest of the ingest
	// is still useful; the module just gets no External tab data.
	if err := external.IngestModule(ctx, s, dir, r); err != nil {
		slog.Warn("external surface ingest failed",
			"module", r.Name, "version", r.Version, "dir", dir, "err", err)
	}
	return r, nil
}

// FromMirroredVersion re-ingests a module version from canopy's local
// BCR-shape mirror at worktreeDir/modules/<module>/<version>/. Reads
// source.json + MODULE.bazel from disk; fetches the tarball over HTTP
// with integrity verification; runs assay on the extracted tree; and
// writes the report to s. The canonical (module, version) overrides
// whatever placeholder coordinates assay reads from the in-tree
// MODULE.bazel — registry mirrors are authoritative on naming.
//
// The intended consumer is bzlhub watch's OnCommit handler: after a
// `git fetch + reset --hard` lands new commits, the changed
// modules/<m>/<v>/ paths get fed here to refresh the SQLite index.
// Idempotent — re-calling for the same (module, version) replaces
// the existing report.
func FromMirroredVersion(
	ctx context.Context,
	s *store.Store,
	worktreeDir, module, version string,
) (*report.ModuleReport, error) {
	if s == nil {
		return nil, fmt.Errorf("ingest.FromMirroredVersion: store is required")
	}
	mirrorDir := filepath.Join(worktreeDir, "modules", module, version)
	srcPath := filepath.Join(mirrorDir, "source.json")
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", srcPath, err)
	}
	src, err := fetch.ParseSourceJSON(srcBytes)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", srcPath, err)
	}

	// MODULE.bazel is optional on disk — older mirrored modules may
	// have it bundled in the tarball only. Read if present; pass nil
	// otherwise and let resolve.FromSource's modBytesFallback handle
	// the post-extract recovery. We don't pass a fallback function:
	// any caller of FromMirroredVersion has already snapshotted the
	// mirror, so a fallback HTTP fetch (which would be against the
	// upstream registry, not the local mirror) is the wrong recovery
	// for a watcher. If MODULE.bazel is missing from both disk and
	// tarball, the analyze step fails loudly — that's the right
	// signal for "your mirror is incomplete."
	var modBytes []byte
	if b, rerr := os.ReadFile(filepath.Join(mirrorDir, "MODULE.bazel")); rerr == nil {
		modBytes = b
	}

	m, err := resolve.FromSource(ctx, fetch.NewClient(), src, srcBytes, modBytes, nil, resolve.Options{})
	if err != nil {
		return nil, fmt.Errorf("resolve %s@%s: %w", module, version, err)
	}
	defer m.Cleanup()

	r, err := Analyze(ctx, m.Dir)
	if err != nil {
		return nil, err
	}
	// Override with canonical coordinates from the mirror layout — the
	// MODULE.bazel inside the tarball commonly carries a placeholder
	// (e.g., "0.0.0") that mismatches the registry's canonical version.
	r.Name = module
	r.Version = version

	if err := s.WriteReport(ctx, r); err != nil {
		return nil, fmt.Errorf("write report %s@%s: %w", module, version, err)
	}
	if err := external.IngestModule(ctx, s, m.Dir, r); err != nil {
		slog.Warn("external surface ingest failed",
			"module", module, "version", version, "err", err)
	}
	return r, nil
}
