package bzlhub

import (
	"context"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Search dispatches across three back-ends depending on the query
// shape: attr-name search (cross-corpus walk of rule attrs),
// symbol-kind search (exact-name match within a kind), or FTS5
// full-text. Defined on Service so it satisfies api.Canopy; the
// concrete walks below are private.
func (s *Service) Search(ctx context.Context, q api.Query) (*api.SearchResults, error) {
	// Attribute search bypasses the FTS5 path: attr names live in
	// nested ModuleReport structure (Rules[].Attrs[].Name) that the
	// indexed-text columns don't capture. Walk the corpus instead.
	// O(N) for now where N is module-version rows; documented as a
	// deferred-to-inverted-index when the corpus passes ~thousands.
	if q.Attr != "" {
		return s.searchByAttr(ctx, q)
	}
	// Symbol-kind search: rule:NAME / provider:NAME / macro:NAME /
	// repo_rule:NAME / module_extension:NAME. Same cross-corpus walk
	// pattern as attr search; matches the symbol's exact .Name within
	// the named kind. Falls through to FTS5 when Kind is empty.
	if q.Kind != "" && q.Text != "" {
		return s.searchByKind(ctx, q)
	}
	return s.store.Search(ctx, q)
}

// searchByKind walks the corpus for an exact-name match within the
// named symbol kind. Cross-corpus question — "every module that
// defines rule cc_binary" — that FTS5 can't filter by kind alone.
func (s *Service) searchByKind(ctx context.Context, q api.Query) (*api.SearchResults, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	target := q.Text
	out := &api.SearchResults{Hits: []api.Hit{}}
	push := func(mv store.ModuleVersion, kind, name, file string) {
		if len(out.Hits) >= limit {
			return
		}
		out.Hits = append(out.Hits, api.Hit{
			Module:    mv.Module,
			Version:   mv.Version,
			MatchKind: kind,
			MatchName: name,
			File:      file,
		})
	}
	for _, mv := range rows {
		if len(out.Hits) >= limit {
			break
		}
		rep, err := s.store.GetReport(ctx, mv.Module, mv.Version)
		if err != nil || rep == nil {
			continue
		}
		switch q.Kind {
		case api.SymbolKindRule:
			for _, r := range rep.Rules {
				if r.Name == target {
					push(mv, api.MatchKindRule, r.Name, r.Provenance.File)
				}
			}
		case api.SymbolKindProvider:
			for _, p := range rep.Providers {
				if p.Name == target {
					push(mv, api.MatchKindProvider, p.Name, p.Provenance.File)
				}
			}
		case api.SymbolKindMacro:
			for _, m := range rep.Macros {
				if m.Name == target {
					push(mv, api.MatchKindMacro, m.Name, m.Provenance.File)
				}
			}
		case api.SymbolKindRepoRule:
			for _, rr := range rep.RepositoryRules {
				if rr.Name == target {
					push(mv, api.MatchKindRepositoryRule, rr.Name, rr.Provenance.File)
				}
			}
		case api.SymbolKindModuleExtension:
			for _, me := range rep.ModuleExtensions {
				if me.Name == target {
					// MatchKind stays "module" rather than introducing
					// a new top-level Hit.MatchKind variant; the UI's
					// existing renderer falls through to the module
					// view for this kind.
					push(mv, api.MatchKindModule, me.Name, me.Provenance.File)
				}
			}
		}
	}
	out.Total = len(out.Hits)
	return out, nil
}

// searchByAttr returns rules / repository_rules whose attrs contain
// an attribute with the exact name in q.Attr. Exact match (not
// substring) because attribute names are short identifiers and a
// substring query would dilute results — "srcs" matching "test_srcs"
// would surprise more than help.
func (s *Service) searchByAttr(ctx context.Context, q api.Query) (*api.SearchResults, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	out := &api.SearchResults{Hits: []api.Hit{}}
	for _, mv := range rows {
		if len(out.Hits) >= limit {
			break
		}
		rep, err := s.store.GetReport(ctx, mv.Module, mv.Version)
		if err != nil || rep == nil {
			continue
		}
		for _, ru := range rep.Rules {
			for _, a := range ru.Attrs {
				if a.Name != q.Attr {
					continue
				}
				out.Hits = append(out.Hits, api.Hit{
					Module:    mv.Module,
					Version:   mv.Version,
					MatchKind: api.MatchKindRule,
					MatchName: ru.Name,
					File:      ru.Provenance.File,
					Attr:      a.Name,
				})
				if len(out.Hits) >= limit {
					break
				}
			}
			if len(out.Hits) >= limit {
				break
			}
		}
		for _, rr := range rep.RepositoryRules {
			if len(out.Hits) >= limit {
				break
			}
			for _, a := range rr.Attrs {
				if a.Name != q.Attr {
					continue
				}
				out.Hits = append(out.Hits, api.Hit{
					Module:    mv.Module,
					Version:   mv.Version,
					MatchKind: api.MatchKindRepositoryRule,
					MatchName: rr.Name,
					File:      rr.Provenance.File,
					Attr:      a.Name,
				})
				if len(out.Hits) >= limit {
					break
				}
			}
		}
	}
	out.Total = len(out.Hits)
	return out, nil
}
