package api

// ConsumersResult is the wire shape for the cross-corpus consumer
// view (Plan 07): every call site of a symbol defined by some
// (module, version) across the entire indexed canopy corpus.
//
// "Consumers" intentionally excludes the defining module's own
// references — operators investigating "who uses my rule?" don't
// want their own examples drowning the list. Pass
// ?include_self=true to override.
//
// Backend pipeline:
//
//  1. Resolve (module, version, name) → SCIP symbol via the stored
//     ModuleReport's Rule/Provider/Macro/RepoRule/ModuleExtension
//     provenance.
//  2. LookupXRefs(symbol, includeDefinition=false).
//  3. Group occurrences by (consumer_module, consumer_version),
//     filter the defining module by default, attach pre-shaped
//     code-nav hrefs.
//
// Empty Consumers means "the symbol is defined in this module but
// no other indexed module calls it." Distinguish from "symbol not
// found in this module" (which surfaces as HTTP 404).
type ConsumersResult struct {
	// Symbol is the resolved SCIP symbol string the lookup ran for.
	// Useful for debugging "why didn't I get the consumers I
	// expected" — operators can curl /api/v1/xrefs with this exact
	// value to inspect the raw occurrence list.
	Symbol string `json:"symbol"`
	// Module + Version + Name echo the request inputs for clients
	// that hold the result without the request URL handy.
	Module  string `json:"module"`
	Version string `json:"version"`
	Name    string `json:"name"`
	// Kind tags whether the resolved symbol came from a rule /
	// provider / macro / repo_rule / module_extension. Empty when
	// the resolver couldn't pin one (multiple matches in different
	// kinds — rare, but possible).
	Kind string `json:"kind,omitempty"`
	// File is the resolved Provenance.File (the .bzl path inside
	// the defining module). Surfaces "we found your symbol HERE"
	// without the client re-walking the report.
	File string `json:"file,omitempty"`
	// TotalCallSites is the cross-corpus count after filtering out
	// the defining module's own references.
	TotalCallSites int `json:"total_call_sites"`
	// ConsumerCount is the number of distinct (consumer_module,
	// consumer_version) pairs in Consumers. Equals len(Consumers).
	ConsumerCount int `json:"consumer_count"`
	// Skipped counts indexed coordinates whose SCIP blob couldn't
	// be loaded or parsed. Surfaced so the operator can correlate a
	// suspiciously low count with a partial walk.
	Skipped int `json:"skipped"`
	// Consumers, grouped per (module, version). Sorted by module
	// ASC then version ASC for deterministic wire output.
	Consumers []ConsumerEntry `json:"consumers"`
}

// ConsumerEntry is one (consumer_module, consumer_version) row with
// its call sites of the queried symbol.
type ConsumerEntry struct {
	Module      string     `json:"module"`
	Version     string     `json:"version"`
	ModuleHref  string     `json:"module_href"`
	CallSites   []CallSite `json:"call_sites"`
}

// CallSite is one occurrence of the symbol inside a consumer's
// indexed source.
type CallSite struct {
	File   string `json:"file"`
	Line   int32  `json:"line"`
	Column int32  `json:"column,omitempty"`
	// Href is the pre-shaped code-nav deep link to the file at the
	// occurrence line. The server emits it so the UI doesn't have
	// to reconstruct the path-encoding rules.
	Href string `json:"href"`
}
