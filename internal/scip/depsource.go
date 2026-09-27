package scip

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/moduledeps"
)

// DepsFunc answers "what does this module depend on", from wherever the caller
// can find out.
//
// A func type rather than an interface because the useful operation on these is
// composition (see FirstOf), and composing funcs needs no structs, no embedding
// and no adapter types.
type DepsFunc func(ctx context.Context, name, version string) ([]report.ModuleKey, error)

// ReportStore is the store capability DepsFromStore needs. *store.Store
// satisfies it.
type ReportStore interface {
	GetReport(ctx context.Context, name, version string) (*report.ModuleReport, error)
}

// ModuleBazelSource serves MODULE.bazel content for a (module, version).
// backend.Backend satisfies it, which means the mirror, an HTTP store and the
// upstream registry all do -- through the egress policy, via the cascade.
type ModuleBazelSource interface {
	GetModuleBazel(ctx context.Context, module, version string) (io.ReadCloser, error)
}

// DepsFromStore reads dependencies from an already-ingested report.
//
// The cheap source: the report is already parsed and local. It can only answer
// for modules that have been ingested, which is exactly why it is not enough on
// its own -- see FirstOf.
func DepsFromStore(s ReportStore) DepsFunc {
	return func(ctx context.Context, name, version string) ([]report.ModuleKey, error) {
		r, err := s.GetReport(ctx, name, version)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fmt.Errorf("scip: store returned no report for %s@%s", name, version)
		}
		return r.BazelDeps, nil
	}
}

// DepsFromModuleBazel reads dependencies from MODULE.bazel content.
//
// The authoritative source, and the one that makes ingest order irrelevant: a
// module's dependency declaration does not depend on what bzlhub happens to
// have ingested. Backed by the cascade, it reads the mirror first and only
// reaches upstream if the egress policy allows.
func DepsFromModuleBazel(src ModuleBazelSource) DepsFunc {
	return func(ctx context.Context, name, version string) ([]report.ModuleKey, error) {
		rc, err := src.GetModuleBazel(ctx, name, version)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()

		content, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("scip: read MODULE.bazel for %s@%s: %w", name, version, err)
		}
		return moduledeps.FromModuleBazel(content)
	}
}

// FirstOf tries each source in order and returns the first that answers.
//
// This is the whole ordering fix in one function. The closure used to be a
// function of ingest HISTORY, because the store was the only source and the
// store is whatever has been ingested so far. Chaining an authoritative source
// behind it makes the closure a function of the module's own declaration
// instead, which is what it should always have been.
//
// A source that errors is treated as "cannot answer" and the next is tried. The
// last error surfaces if none can, because the walk must be able to tell
// "unknown" from "no dependencies" -- an empty list would silently mean the
// latter.
func FirstOf(sources ...DepsFunc) DepsFunc {
	return func(ctx context.Context, name, version string) ([]report.ModuleKey, error) {
		if len(sources) == 0 {
			return nil, errors.New("scip: FirstOf has no sources and can answer nothing")
		}
		var lastErr error
		for _, src := range sources {
			deps, err := src(ctx, name, version)
			if err == nil {
				return deps, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("scip: no source could resolve %s@%s: %w", name, version, lastErr)
	}
}
