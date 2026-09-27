package scip

import (
	"fmt"
	"sort"

	"github.com/albertocavalcante/scip-kit/scipio"
	scipstarlark "github.com/albertocavalcante/scip-starlark/pkg/index"
	scip "github.com/scip-code/scip/bindings/go/scip"
)

// UnresolvedRepos reports the load() targets in an index that no closure entry
// could place, as the raw labels they were written as.
//
// An index with unresolved cross-module references is not broken: it parses, and
// navigation within the module works. It is INCOMPLETE -- "go to definition" on
// those symbols reaches nothing -- and until now the only way to notice was to
// read symbols by hand. That made an incompletely mirrored deployment
// indistinguishable from a fully resolved one.
//
// Nothing new is recorded to find this out. scip-starlark already marks a load()
// it could not place by putting UnresolvedManager in the symbol's package
// manager field, which is exactly so the marker survives parsing. Reading it
// back needs no schema change, no extra return value from Generate, and no
// second source of truth that could disagree with the index.
//
// The result is sorted and deduplicated: one missing dependency loaded from six
// files is one problem to act on, not six.
func UnresolvedRepos(blob []byte) ([]string, error) {
	idx, err := scipio.UnmarshalIndex(blob)
	if err != nil {
		// Not an empty result. An empty slice reads as "this index is fully
		// resolved", which is the opposite of what unparseable bytes mean.
		return nil, fmt.Errorf("scip: read index to find unresolved refs: %w", err)
	}

	seen := map[string]struct{}{}
	note := func(symbolStr string) {
		if symbolStr == "" || scip.IsLocalSymbol(symbolStr) {
			return
		}
		parsed, err := scip.ParseSymbol(symbolStr)
		if err != nil {
			// A symbol the reference parser rejects is a different defect, and
			// one the producers' own conformance gates cover. Not this
			// function's business to re-report.
			return
		}
		if parsed.Package.GetManager() != scipstarlark.UnresolvedManager {
			return
		}
		// The raw load target was stored as the package NAME, which is what an
		// operator needs to see: it names the dependency to mirror.
		if raw := parsed.Package.GetName(); raw != "" {
			seen[raw] = struct{}{}
		}
	}

	for _, doc := range idx.Documents {
		for _, occ := range doc.Occurrences {
			note(occ.Symbol)
		}
		for _, si := range doc.Symbols {
			note(si.Symbol)
		}
	}
	for _, si := range idx.ExternalSymbols {
		note(si.Symbol)
	}

	out := make([]string, 0, len(seen))
	for raw := range seen {
		out = append(out, raw)
	}
	sort.Strings(out)
	return out, nil
}
