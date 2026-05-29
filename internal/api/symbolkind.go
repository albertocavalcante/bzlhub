package api

// SymbolKind narrows Query.Kind to a closed set of values the
// searchByKind dispatcher knows how to walk. The wire shape stays
// string (no JSON enum) so we can extend the set without a breaking
// change; the constants give Go callers single-source-of-truth.
//
// The values match the prefixes accepted by the UI search bar
// (rule:NAME / provider:NAME / macro:NAME / repo_rule:NAME /
// module_extension:NAME), keeping the contract between parser and
// backend symmetric.
type SymbolKind = string

const (
	SymbolKindRule            SymbolKind = "rule"
	SymbolKindProvider        SymbolKind = "provider"
	SymbolKindMacro           SymbolKind = "macro"
	SymbolKindRepoRule        SymbolKind = "repo_rule"
	SymbolKindModuleExtension SymbolKind = "module_extension"
)

// MatchKind is the Hit.MatchKind taxonomy returned to clients. Distinct
// from SymbolKind because repo-rule hits surface as "repository_rule"
// to match the assay report nomenclature, and module-extension hits
// fold into the "module" view rather than introducing a new top-level
// variant.
type MatchKind = string

const (
	MatchKindModule         MatchKind = "module"
	MatchKindRule           MatchKind = "rule"
	MatchKindProvider       MatchKind = "provider"
	MatchKindMacro          MatchKind = "macro"
	MatchKindRepositoryRule MatchKind = "repository_rule"
)
