package bzlhub

import (
	"context"

	bzlhubscip "github.com/albertocavalcante/bzlhub/internal/scip"
)

// GetScipBlob proxies to the store. Bytes are what scip-bazel
// produced + bzlhub persisted during the most recent ingest of
// (module, version).
func (s *Service) GetScipBlob(ctx context.Context, name, version string) ([]byte, error) {
	return s.store.GetScipBlob(ctx, name, version)
}

// LookupSymbol resolves a full SCIP symbol string to its definition
// site by handing the stored blob to understory. The Service satisfies
// bzlhubscip.BlobReader (it has GetScipBlob), so we can pass `s` as
// the reader without an adapter type.
func (s *Service) LookupSymbol(ctx context.Context, module, version, symbol string) (*bzlhubscip.SymbolLookupResult, error) {
	return bzlhubscip.LookupSymbol(ctx, s, module, version, symbol)
}

// LookupReferences proxies to internal/scip the same way LookupSymbol
// does — same blob reader, same understory.OpenBytes path.
func (s *Service) LookupReferences(ctx context.Context, module, version, symbol string, includeDefinition bool) (*bzlhubscip.SymbolReferencesResult, error) {
	return bzlhubscip.LookupReferences(ctx, s, module, version, symbol, includeDefinition)
}

// LookupXRefs walks every indexed (module, version), collecting
// occurrences of `symbol` across the whole catalogue. Service satisfies
// both halves of the dependency:
//   - bzlhubscip.BlobReader via its existing GetScipBlob method
//   - bzlhubscip.XRefsLister via the adapter below (which translates
//     store.ModuleVersion → bzlhubscip.ModuleVersion so the scip
//     package doesn't have to import store)
func (s *Service) LookupXRefs(ctx context.Context, symbol string, includeDefinition bool) (*bzlhubscip.XRefsResult, error) {
	return bzlhubscip.LookupXRefs(ctx, s, scipXRefsLister{s}, symbol, includeDefinition)
}

// scipXRefsLister adapts Service to bzlhubscip.XRefsLister by mapping
// store.ModuleVersion to the scip package's own ModuleVersion type.
// Tiny adapter type kept here (rather than as a method on Service)
// so api.Bzlhub doesn't accidentally take a dependency on the store
// package via shared interface coupling.
type scipXRefsLister struct{ s *Service }

func (a scipXRefsLister) ListScipVersions(ctx context.Context) ([]bzlhubscip.ModuleVersion, error) {
	raw, err := a.s.store.ListScipVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]bzlhubscip.ModuleVersion, len(raw))
	for i, mv := range raw {
		out[i] = bzlhubscip.ModuleVersion{Module: mv.Module, Version: mv.Version}
	}
	return out, nil
}
