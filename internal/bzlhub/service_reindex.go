package bzlhub

import (
	"context"
	"errors"
	"fmt"

	"github.com/albertocavalcante/bzlhub/internal/codenav"
	bzlhubscip "github.com/albertocavalcante/bzlhub/internal/scip"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// ReindexOptions configures a SCIP reindex.
type ReindexOptions struct {
	// Only restricts the run to these coordinates. Empty means every module
	// that already has a stored index.
	Only []store.ModuleVersion

	// DryRun reports what would change and writes nothing.
	DryRun bool
}

// ReindexSkip records one module the run could not reindex, and why.
//
// Reason is not optional. A reindex that silently skips a module reports
// success for a module it never touched, and the operator has no way to tell
// "nothing needed doing" from "half of it failed".
type ReindexSkip struct {
	Module  string
	Version string
	Reason  string
}

// ReindexUnresolved names the load() targets one module could not place.
//
// A reindex that resolves everything and one that leaves half the closure
// unplaced otherwise print the same "reindexed N of N", so an incompletely
// mirrored deployment reads as finished. Repos holds the raw load targets,
// because that is what an operator mirrors to fix it.
type ReindexUnresolved struct {
	Module  string
	Version string
	Repos   []string
}

// ReindexResult is the accounting for one run.
type ReindexResult struct {
	// Considered is how many coordinates the run looked at.
	Considered int

	// Reindexed counts successful regenerations. Under DryRun it counts what
	// WOULD have been written, so a dry run's number is directly comparable to
	// the real run's.
	Reindexed int

	// Skipped is every coordinate that could not be reindexed, with a reason.
	Skipped []ReindexSkip

	// Unresolved lists modules that WERE reindexed but whose cross-module
	// references could not all be placed. Not a failure -- the index parses and
	// navigates within itself -- but incomplete, and invisible without this.
	Unresolved []ReindexUnresolved
}

// ReindexScip regenerates stored SCIP indexes from mirrored sources.
//
// Every blob currently stored predates two changes that alter its contents: the
// symbol grammar fix (old symbols are rejected by scip.ParseSymbol, so nothing
// resolves them) and the transitive closure (references to a dependency-of-a-
// dependency used to resolve to nothing). Regenerating is the only way to get
// those; an index is a snapshot of what the indexer knew when it ran.
//
// Fully offline. Sources come from the mirror via codenav.MaterializeSource and
// dependency coordinates from the store and mirror, so a reindex adds no
// outbound traffic and no egress-policy surface.
//
// One module's failure never aborts the run: a partially mirrored deployment
// should reindex what it can and say what it could not. The error return is
// reserved for a misconfiguration that makes the whole run meaningless.
func (s *Service) ReindexScip(ctx context.Context, opts ReindexOptions) (*ReindexResult, error) {
	// Refuse rather than report an empty success. "0 reindexed" because the
	// mirror was never configured is indistinguishable from "nothing needed
	// reindexing", and the second is a perfectly normal outcome.
	if s.MirrorRoot == "" {
		return nil, errors.New("reindex: MirrorRoot is required; sources are read from the mirror")
	}
	if s.SourcesCacheDir == "" {
		return nil, errors.New("reindex: SourcesCacheDir is required; sources are unpacked there")
	}

	targets := opts.Only
	if len(targets) == 0 {
		stored, err := s.store.ListScipVersions(ctx)
		if err != nil {
			return nil, fmt.Errorf("reindex: list stored indexes: %w", err)
		}
		targets = stored
	}

	deps := s.scipDepSources()
	res := &ReindexResult{Considered: len(targets)}

	for _, mv := range targets {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		unresolved, err := s.reindexOne(ctx, deps, mv, opts.DryRun)
		if err != nil {
			res.Skipped = append(res.Skipped, ReindexSkip{
				Module: mv.Module, Version: mv.Version, Reason: err.Error(),
			})
			continue
		}
		res.Reindexed++
		if len(unresolved) > 0 {
			res.Unresolved = append(res.Unresolved, ReindexUnresolved{
				Module: mv.Module, Version: mv.Version, Repos: unresolved,
			})
		}
	}
	return res, nil
}

// reindexOne regenerates a single coordinate. Its error is the skip reason, so
// it is written to be read by an operator rather than wrapped for a caller.
func (s *Service) reindexOne(ctx context.Context, deps bzlhubscip.DepsFunc, mv store.ModuleVersion, dryRun bool) ([]string, error) {
	r, err := s.store.GetReport(ctx, mv.Module, mv.Version)
	if err != nil {
		return nil, fmt.Errorf("no stored report: %w", err)
	}

	sourceDir, err := codenav.MaterializeSource(s.MirrorRoot, s.SourcesCacheDir, mv.Module, mv.Version)
	if err != nil {
		return nil, fmt.Errorf("source not available from the mirror: %w", err)
	}

	closure, err := bzlhubscip.TransitiveClosure(ctx, deps, r)
	if err != nil {
		return nil, fmt.Errorf("resolve dependency closure: %w", err)
	}

	blob, err := bzlhubscip.Generate(sourceDir, mv.Module, mv.Version, closure)
	if err != nil {
		return nil, fmt.Errorf("generate index: %w", err)
	}

	// Read back from the blob rather than tracking it during generation: the
	// index is the single source of truth for what it resolved, so the two
	// cannot drift.
	unresolved, err := bzlhubscip.UnresolvedRepos(blob)
	if err != nil {
		return nil, fmt.Errorf("inspect generated index: %w", err)
	}

	if dryRun {
		return unresolved, nil
	}
	if err := s.store.WriteScipBlob(ctx, mv.Module, mv.Version, blob); err != nil {
		return nil, fmt.Errorf("write index: %w", err)
	}
	// Kept in step with the ingest path: the flag drives whether the UI offers
	// code navigation at all, so a reindex that leaves it stale makes a working
	// index invisible.
	if err := s.store.SetHasSourceIndex(ctx, mv.Module, mv.Version, scipBlobHasFiles(blob)); err != nil {
		return nil, fmt.Errorf("update has_source_index: %w", err)
	}
	return unresolved, nil
}
