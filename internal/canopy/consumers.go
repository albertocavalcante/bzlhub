package canopy

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
)

// LookupConsumers implements api.Canopy.LookupConsumers — Plan 07's
// cross-corpus consumer view. Resolves the user-facing identifier to
// a SCIP symbol via the defining module's ModuleReport, then runs
// LookupXRefs with includeDefinition=false and groups per consumer
// module. Defining-module occurrences are filtered out unless
// includeSelf is true.
//
// Returns ("", "<m>@<v>: name %q not found in any rule/provider/
// macro/repo_rule/module_extension") when the name doesn't resolve.
// The HTTP handler classifies this as 404.
func (s *Service) LookupConsumers(ctx context.Context, module, version, name string, includeSelf bool) (*api.ConsumersResult, error) {
	rep, err := s.store.GetReport(ctx, module, version)
	if err != nil {
		return nil, err
	}

	file, kind := resolveSymbolProvenance(rep, name)
	if file == "" {
		return nil, fmt.Errorf("%s@%s: %q not found in any rule/provider/macro/repo_rule/module_extension", module, version, name)
	}

	// SCIP symbol shape canopy emits via scip-bazel:
	//   bzlmod <module>@<version> <relpath>#<name>
	// See internal/scip/lookup.go for the format definition.
	symbol := fmt.Sprintf("bzlmod %s@%s %s#%s", module, version, file, name)

	xrefs, err := s.LookupXRefs(ctx, symbol, false /* includeDefinition */)
	if err != nil {
		return nil, fmt.Errorf("lookup xrefs: %w", err)
	}

	out := &api.ConsumersResult{
		Symbol:    symbol,
		Module:    module,
		Version:   version,
		Name:      name,
		Kind:      kind,
		File:      file,
		Skipped:   xrefs.Skipped,
		Consumers: []api.ConsumerEntry{},
	}

	for _, g := range xrefs.Groups {
		if !includeSelf && g.Module == module && g.Version == version {
			continue
		}
		entry := api.ConsumerEntry{
			Module:     g.Module,
			Version:    g.Version,
			ModuleHref: fmt.Sprintf("/modules/%s/%s", url.PathEscape(g.Module), url.PathEscape(g.Version)),
			CallSites:  make([]api.CallSite, 0, len(g.References)),
		}
		for _, ref := range g.References {
			entry.CallSites = append(entry.CallSites, api.CallSite{
				File:   ref.File,
				Line:   ref.StartLine,
				Column: ref.StartChar,
				Href:   codeNavHref(g.Module, g.Version, ref.File, ref.StartLine),
			})
		}
		out.Consumers = append(out.Consumers, entry)
		out.TotalCallSites += len(entry.CallSites)
	}

	// Defensive deterministic sort (LookupXRefs already orders
	// groups, but filtering may have left a different shape; cheap
	// to re-sort).
	sort.SliceStable(out.Consumers, func(i, j int) bool {
		if out.Consumers[i].Module != out.Consumers[j].Module {
			return out.Consumers[i].Module < out.Consumers[j].Module
		}
		return out.Consumers[i].Version < out.Consumers[j].Version
	})
	out.ConsumerCount = len(out.Consumers)
	return out, nil
}

// resolveSymbolProvenance walks the ModuleReport for any
// rule/provider/macro/repo_rule/module_extension whose Name matches
// the given identifier; returns its Provenance.File + a kind tag.
// First-match-wins; canopy's report doesn't enforce uniqueness
// across kinds (a name could theoretically be both a rule and a
// macro), but the file is what matters for SCIP symbol resolution.
//
// Returns ("", "") when no match is found.
func resolveSymbolProvenance(rep *report.ModuleReport, name string) (file, kind string) {
	if rep == nil {
		return "", ""
	}
	for _, x := range rep.Rules {
		if x.Name == name {
			return x.Provenance.File, "rule"
		}
	}
	for _, x := range rep.Providers {
		if x.Name == name {
			return x.Provenance.File, "provider"
		}
	}
	for _, x := range rep.Macros {
		if x.Name == name {
			return x.Provenance.File, "macro"
		}
	}
	for _, x := range rep.RepositoryRules {
		if x.Name == name {
			return x.Provenance.File, "repo_rule"
		}
	}
	for _, x := range rep.ModuleExtensions {
		if x.Name == name {
			return x.Provenance.File, "module_extension"
		}
	}
	return "", ""
}

// codeNavHref builds canopy's code-nav deep-link path for a (module,
// version, file, line) coordinate. Mirrors ui/src/lib/links.ts's
// codeNavFileHref so a Go-emitted href is interchangeable with one
// the UI would compose itself.
//
// Shape:
//
//	/modules/<m>/<v>/code-nav/file/<encoded-segments>?line=<n>
//
// Each path segment is URL-encoded independently so files with
// spaces / unicode / `#` / `?` work cleanly. The chi route is
// /modules/{m}/{v}/code-nav/* — the `file/` prefix is part of the
// understory-served path under that wildcard, not a route param.
func codeNavHref(module, version, file string, line int32) string {
	href := fmt.Sprintf("/modules/%s/%s/code-nav/file/%s",
		url.PathEscape(module),
		url.PathEscape(version),
		encodePathSegments(strings.TrimPrefix(file, "/")),
	)
	if line > 0 {
		href += fmt.Sprintf("?line=%d", line)
	}
	return href
}

// encodePathSegments URL-encodes each forward-slash-separated
// segment of a path, joining the encoded segments back with `/`.
// Mirrors the JS-side `file.split('/').map(encodeURIComponent).join('/')`
// idiom in ui/src/lib/links.ts so paths are byte-identical
// regardless of which side composes the URL.
func encodePathSegments(file string) string {
	if file == "" {
		return ""
	}
	parts := strings.Split(file, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
