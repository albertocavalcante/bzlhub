package api

// DefaultPlatform is the sentinel platform string used when a ref or
// fork-error isn't tied to a specific (os, arch). Persisted as-is in
// the schema (external_refs.platform DEFAULT 'any') and surfaced
// through the API.
const DefaultPlatform = "any"

// ExternalRef is one URL the module's .bzl files would fetch,
// extracted by canopy's static analysis pipeline. JSON-shaped.
type ExternalRef struct {
	URL        string `json:"url"`
	Host       string `json:"host"`
	Class      string `json:"class"`
	Mutability string `json:"mutability"`
	SHA256     string `json:"sha256,omitempty"`
	Integrity  string `json:"integrity,omitempty"`
	APIName    string `json:"api_name,omitempty"`
	RuleName   string `json:"rule_name,omitempty"`
	Platform   string `json:"platform,omitempty"`
	Tainted    bool   `json:"tainted,omitempty"`
	File       string `json:"file,omitempty"`

	// Confidence is canopy's "how sure are we about this URL?" signal,
	// derived from the static analysis state. Orthogonal to Mutability
	// (which is about the URL's host); Confidence is about the
	// analyzer's resolution path. Computed at API-response time from
	// Tainted + Platform; not persisted.
	//
	// Values:
	//   "tainted"          — eval touched opaque state (ctx.execute,
	//                        unresolved external load); verify at
	//                        runtime before trusting.
	//   "platform-specific"— captured per-fork; applies only to the
	//                        Platform field's (os, arch).
	//   "resolved"         — known URL, no caveats from this layer.
	Confidence string `json:"confidence,omitempty"`

	// SourceModule names which closure node contributed this ref,
	// formatted "<module>@<version>". Populated ONLY in the closure-
	// wide airgap response; empty on per-module surface responses
	// (where the source is implicit in the URL).
	//
	// First-seen-wins semantics under closure dedupe: when the root
	// and a transitive dep both declare the same (URL, platform,
	// file) ref, the root's provenance takes precedence (matches the
	// existing dedupe convention in the closure walker).
	SourceModule string `json:"source_module,omitempty"`
}

// ExternalForkError is one (os, arch) platform's interpretation
// failure during analysis. The corresponding refs from other forks
// still ship.
type ExternalForkError struct {
	Platform string `json:"platform"`
	Message  string `json:"message"`
}

// ExternalSurfaceResponse is the per-module aggregate emitted by
// GET /api/modules/{module}/{version}/external.
type ExternalSurfaceResponse struct {
	Module      string              `json:"module"`
	Version     string              `json:"version"`
	Refs        []ExternalRef       `json:"refs"`
	ForkErrors  []ExternalForkError `json:"fork_errors,omitempty"`
	ClassCounts map[string]int      `json:"class_counts,omitempty"`
	// CorpusUsages is populated when the module declares module_extensions
	// AND canopy's indexed corpus contains consumer-side use_extension
	// calls for them. Each entry tells the operator "this extension is
	// used by these N consumer modules with these tag values" — the
	// data needed to drive the producer's extension impls with real
	// (non-default) tag instances for airgap URL inference.
	CorpusUsages []ExtensionCorpusUsage `json:"corpus_usages,omitempty"`
}

// ExtensionCorpusUsage is one row of the corpus-side index for a
// producer's extension: the canonical extension key + the list of
// consumer call sites that pin tag values on it.
type ExtensionCorpusUsage struct {
	ExtensionFile string                  `json:"extension_file"`
	ExtensionName string                  `json:"extension_name"`
	Consumers     []ExtensionConsumerCall `json:"consumers"`
}

// ExtensionConsumerCall is one consumer's tag invocation on an
// extension, decoded from the cross-module corpus index. TagAttrs is
// already JSON-deserialized for direct UI consumption.
type ExtensionConsumerCall struct {
	ConsumerModule  string         `json:"consumer_module"`
	ConsumerVersion string         `json:"consumer_version"`
	TagName         string         `json:"tag_name"`
	TagAttrs        map[string]any `json:"tag_attrs,omitempty"`
	DevDependency   bool           `json:"dev_dependency,omitempty"`
	Isolate         bool           `json:"isolate,omitempty"`
}

// ClosureSurfaceModule is one (module, version) row in a closure-wide
// aggregate, with that module's own ref count + class breakdown.
type ClosureSurfaceModule struct {
	Module      string         `json:"module"`
	Version     string         `json:"version"`
	RefCount    int            `json:"ref_count"`
	ClassCounts map[string]int `json:"class_counts,omitempty"`
	External    bool           `json:"external,omitempty"` // bazel_dep target not in canopy's index
}

// DownloaderConfigOptions configures the downloader-config emitter.
// Defaults: per-module scope, http://mirror.internal/ prefix.
type DownloaderConfigOptions struct {
	// MirrorBase is the URL prefix the rewrite rules redirect to. The
	// final mirror URL is `<MirrorBase>/<host>/<rest>`. Must end in
	// "/"; the renderer adds one if missing.
	MirrorBase string
	// Recursive includes the full bazel_dep closure in the surface
	// sourced for the rewrite map. Default (false) is per-module.
	Recursive bool
}

// DownloaderConfig is the rendered text file content suitable for
// Bazel's `--experimental_downloader_config <path>` flag.
//
// One `rewrite` line per unique host observed in the captured URL
// surface; tainted/unresolved entries are surfaced as comments so the
// operator knows they need manual handling.
type DownloaderConfig struct {
	Module     string `json:"module"`
	Version    string `json:"version"`
	MirrorBase string `json:"mirror_base"`
	Recursive  bool   `json:"recursive"`
	// Text is the ready-to-write config payload. The HTTP endpoint
	// serves this with Content-Type: text/plain.
	Text string `json:"text"`
	// HostCount is the number of unique source hosts the rewrite
	// rules cover.
	HostCount int `json:"host_count"`
	// URLCount is the number of distinct URLs the config translates.
	URLCount int `json:"url_count"`
}

// ModuleMirrorsOptions configures the --module_mirrors emitter.
// Defaults: http://mirror.internal/ prefix.
type ModuleMirrorsOptions struct {
	// MirrorBase is the URL prefix that registry-sourced URLs should be
	// rewritten to. Must end in "/"; the renderer adds one if missing.
	MirrorBase string
	// Registry is the upstream registry to scope the mirror to. Empty
	// emits an unscoped --module_mirrors line (Bazel >= 8.4 syntax);
	// non-empty emits the per-registry form (Bazel >= 8.5). Defaults
	// to https://bcr.bazel.build/ when empty in canopy's emitter.
	Registry string
}

// ModuleMirrors is the rendered .bazelrc snippet for the
// --module_mirrors flag. Sibling to DownloaderConfig — covers the
// registry slice only, vs. DownloaderConfig which covers every URL.
type ModuleMirrors struct {
	Module     string `json:"module"`
	Version    string `json:"version"`
	MirrorBase string `json:"mirror_base"`
	Registry   string `json:"registry"`
	// Text is the ready-to-write .bazelrc snippet (single `common`
	// line with explanatory header comments). The HTTP endpoint serves
	// this with Content-Type: text/plain.
	Text string `json:"text"`
}

// ClosureSurfaceResponse is the closure-wide URL aggregate emitted by
// GET /api/modules/{module}/{version}/airgap-surface. It's the airgap
// question: "every URL the entire dependency closure of <m>@<v> would
// fetch."
type ClosureSurfaceResponse struct {
	Root            string                 `json:"root"`     // "<module>@<version>"
	Modules         []ClosureSurfaceModule `json:"modules"`  // dep-closure walk, root first
	Refs            []ExternalRef          `json:"refs"`     // unioned across closure, deduplicated
	ForkErrors      []ExternalForkError    `json:"fork_errors,omitempty"`
	ClassCounts     map[string]int         `json:"class_counts,omitempty"`
	MaxDepthReached bool                   `json:"max_depth_reached,omitempty"`
	MissingModules  []string               `json:"missing_modules,omitempty"` // closure references not in canopy
}
