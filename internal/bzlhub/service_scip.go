package bzlhub

import (
	"context"

	canopyscip "github.com/albertocavalcante/bzlhub/internal/scip"
)

// GetScipBlob proxies to the store. Bytes are what scip-bazel
// produced + canopy persisted during the most recent ingest of
// (module, version).
func (s *Service) GetScipBlob(ctx context.Context, name, version string) ([]byte, error) {
	return s.store.GetScipBlob(ctx, name, version)
}

// LookupSymbol resolves a full SCIP symbol string to its definition
// site by handing the stored blob to understory. The Service satisfies
// canopyscip.BlobReader (it has GetScipBlob), so we can pass `s` as
// the reader without an adapter type.
func (s *Service) LookupSymbol(ctx context.Context, module, version, symbol string) (*canopyscip.SymbolLookupResult, error) {
	return canopyscip.LookupSymbol(ctx, s, module, version, symbol)
}

// LookupReferences proxies to internal/scip the same way LookupSymbol
// does — same blob reader, same understory.OpenBytes path.
func (s *Service) LookupReferences(ctx context.Context, module, version, symbol string, includeDefinition bool) (*canopyscip.SymbolReferencesResult, error) {
	return canopyscip.LookupReferences(ctx, s, module, version, symbol, includeDefinition)
}

// LookupXRefs walks every indexed (module, version), collecting
// occurrences of `symbol` across the whole catalogue. Service satisfies
// both halves of the dependency:
//   - canopyscip.BlobReader via its existing GetScipBlob method
//   - canopyscip.XRefsLister via the adapter below (which translates
//     store.ModuleVersion → canopyscip.ModuleVersion so the scip
//     package doesn't have to import store)
func (s *Service) LookupXRefs(ctx context.Context, symbol string, includeDefinition bool) (*canopyscip.XRefsResult, error) {
	return canopyscip.LookupXRefs(ctx, s, scipXRefsLister{s}, symbol, includeDefinition)
}

// scipXRefsLister adapts Service to canopyscip.XRefsLister by mapping
// store.ModuleVersion to the scip package's own ModuleVersion type.
// Tiny adapter type kept here (rather than as a method on Service)
// so api.Canopy doesn't accidentally take a dependency on the store
// package via shared interface coupling.
type scipXRefsLister struct{ s *Service }

func (a scipXRefsLister) ListScipVersions(ctx context.Context) ([]canopyscip.ModuleVersion, error) {
	raw, err := a.s.store.ListScipVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]canopyscip.ModuleVersion, len(raw))
	for i, mv := range raw {
		out[i] = canopyscip.ModuleVersion{Module: mv.Module, Version: mv.Version}
	}
	return out, nil
}
