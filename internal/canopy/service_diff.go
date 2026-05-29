package canopy

import (
	"context"
	"errors"
	"fmt"

	"github.com/albertocavalcante/assay"
	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/closurediff"
	"github.com/albertocavalcante/canopy/internal/modulediff"
	"github.com/albertocavalcante/canopy/internal/resolve"
)

func (s *Service) Diff(ctx context.Context, opts api.DiffOptions) (*modulediff.Report, error) {
	if opts.Module == "" || opts.FromVersion == "" || opts.ToVersion == "" {
		return nil, errors.New("diff: module, from, and to are required")
	}
	from, fromSrc, err := s.fetchReportForDiff(ctx, opts.Module, opts.FromVersion, opts.Upstream)
	if err != nil {
		return nil, fmt.Errorf("from %s@%s: %w", opts.Module, opts.FromVersion, err)
	}
	to, toSrc, err := s.fetchReportForDiff(ctx, opts.Module, opts.ToVersion, opts.Upstream)
	if err != nil {
		return nil, fmt.Errorf("to %s@%s: %w", opts.Module, opts.ToVersion, err)
	}
	d := modulediff.Compute(from, to)
	d.FromSource = fromSrc
	d.ToSource = toSrc
	return d, nil
}

func (s *Service) DiffClosure(ctx context.Context, opts api.DiffOptions) (*closurediff.Report, error) {
	if opts.Upstream == "" {
		// Closure walking does MVS via gobzlmod, which needs a registry
		// URL. Force the caller to supply one rather than silently
		// defaulting — a closure walk against the "wrong" upstream
		// returns wildly different MVS results.
		return nil, errors.New("diff-closure: upstream registry URL is required")
	}
	return closurediff.Compute(ctx, closurediff.Options{
		Module:      opts.Module,
		FromVersion: opts.FromVersion,
		ToVersion:   opts.ToVersion,
		Upstream:    opts.Upstream,
		AnalyzeFunc: func(ctx context.Context, name, version string) (*report.ModuleReport, error) {
			r, _, err := s.fetchReportForDiff(ctx, name, version, opts.Upstream)
			return r, err
		},
	})
}

// fetchReportForDiff returns the report for (module, version) plus a
// source tag: "local" if served from the index, "upstream" if the index
// missed and the version was fetched+analyzed transiently. Returns an
// error only if the version is missing everywhere (or no upstream
// fallback was offered).
func (s *Service) fetchReportForDiff(ctx context.Context, module, version, upstream string) (*report.ModuleReport, string, error) {
	r, err := s.store.GetReport(ctx, module, version)
	if err == nil {
		return r, "local", nil
	}
	if upstream == "" {
		return nil, "", err
	}
	// Try the upstream fallback. Errors from this path describe the upstream
	// fetch — the original "not in index" is implied by reaching here.
	r, ferr := s.analyzeFromUpstream(ctx, upstream, module, version)
	if ferr != nil {
		return nil, "", fmt.Errorf("not in index and upstream fetch failed: %w", ferr)
	}
	return r, "upstream", nil
}

// analyzeFromUpstream materializes (module, version) from a BCR-shape
// upstream into a temp dir, runs assay over it, and returns the report
// without touching the store or mirror. The temp dir is cleaned up
// before returning.
//
// This is the "what-if" path: it gives consumers a structured preview
// of a version's surface without committing to ingesting it.
func (s *Service) analyzeFromUpstream(ctx context.Context, upstream, module, version string) (*report.ModuleReport, error) {
	mat, err := resolve.FromRegistry(ctx, upstream, module, version)
	if err != nil {
		return nil, fmt.Errorf("resolve %s@%s from %s: %w", module, version, upstream, err)
	}
	defer mat.Cleanup()
	r, err := assay.Analyze(ctx, mat.Dir)
	if err != nil {
		return nil, fmt.Errorf("analyze %s@%s: %w", module, version, err)
	}
	// Canonicalize coords — the BCR registry path is authoritative since
	// some real modules ship empty version strings in MODULE.bazel.
	r.Name = module
	r.Version = version
	return r, nil
}
