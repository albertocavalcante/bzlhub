package report

// HermeticityClass is the top-level classification of a module's hermeticity.
// Multiple classes can apply (a module may both build from source AND fetch
// pinned binaries). The profile records all that apply with provenance.
type HermeticityClass string

const (
	// PureStarlark: no repository_rule execs, no binary downloads, no
	// runtime resolution. The cleanest profile.
	PureStarlark HermeticityClass = "pure-starlark"

	// PrebuiltBinariesPinned: fetches prebuilt binaries with SHA pinned
	// and platforms declared.
	PrebuiltBinariesPinned HermeticityClass = "prebuilt-binaries-pinned"

	// BuildFromSource: provides cc_binary/equivalent rules; toolchains
	// compile sources rather than downloading binaries.
	BuildFromSource HermeticityClass = "build-from-source"

	// NetworkFetchPinned: fetches at build time with integrity hashes.
	// Reproducible but requires network access.
	NetworkFetchPinned HermeticityClass = "network-fetch-pinned"

	// NetworkFetchUnpinned: fetches at build time WITHOUT integrity hashes.
	// Loud warning case.
	NetworkFetchUnpinned HermeticityClass = "network-fetch-unpinned"

	// RequiresSystemTools: runs docker, system git/python, etc. — relies
	// on tools outside the build sandbox.
	RequiresSystemTools HermeticityClass = "requires-system-tools"

	// RepositoryRuleArbitraryCode: has repository_rule with ctx.execute
	// of unbounded scope. Security review warranted.
	RepositoryRuleArbitraryCode HermeticityClass = "repository-rule-arbitrary-code"
)

// HermeticityProfile is the complete hermeticity classification of a module.
type HermeticityProfile struct {
	// Classes lists every class that applies. A pure-starlark-only module
	// has exactly one entry.
	Classes []HermeticityClass `json:"classes"`

	// Findings record what triggered each classification, with provenance.
	Findings []HermeticityFinding `json:"findings,omitempty"`
}

// Confidence tags each finding with how reliable its classification is.
//
// Definitive: extracted from an unambiguous AST shape — a literal
// integrity hash, a literal `executable = True`, a known API name.
// Re-running the classifier on the same input would emit the same
// finding, and the finding accurately describes the underlying source.
//
// Heuristic: pattern-matched against curated lists, conservative
// fallbacks, or multi-step inference (path filtering, URL substring
// matches). The CLASS may be wrong (e.g., NetworkFetchUnpinned for a
// dict-subscript sha256 that's effectively pinned), but the finding
// is what static analysis can determine without runtime evaluation.
//
// Consumers (canopy registry pages, audit scripts) should render
// Heuristic findings with a "best-effort" marker so users know the
// signal isn't authoritative.
type Confidence string

const (
	ConfidenceDefinitive Confidence = "definitive"
	ConfidenceHeuristic  Confidence = "heuristic"
)

// HermeticityFinding is one piece of evidence that contributed to the profile.
type HermeticityFinding struct {
	Class      HermeticityClass `json:"class"`
	Symbol     string           `json:"symbol"` // the Starlark identifier that triggered (e.g., "download_file")
	Reason     string           `json:"reason"` // human-readable
	Confidence Confidence       `json:"confidence"`
	Provenance Provenance       `json:"provenance"`
}
