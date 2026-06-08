// Package api defines the canopy cohesion interface: a single Go contract
// that every transport (REST+SSE, MCP, CLI, future TUI/Wails) calls into.
// Borrowed from recall (~/dev/ws/sy/recall/internal/api/recall.go).
//
// REST handlers and MCP tools both compile down to method calls here plus a
// marshaling shim — typically ~15 LOC per operation per transport. Swapping
// transports is an isolated change inside their respective packages; the
// methods on Canopy don't move.
package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/albertocavalcante/assay/report"
	bzlsummary "github.com/albertocavalcante/bazel-module-summary-go"

	"github.com/albertocavalcante/bzlhub/internal/closurediff"
	"github.com/albertocavalcante/bzlhub/internal/compat"
	"github.com/albertocavalcante/bzlhub/internal/drift"
	"github.com/albertocavalcante/bzlhub/internal/modulediff"
	canopyscip "github.com/albertocavalcante/bzlhub/internal/scip"
)

// ErrModuleNotFound is returned by Canopy.GetModule when the named
// module isn't indexed in this canopy. HTTP handlers should map this
// to 404 so callers (hover cards, single-module pages) can render a
// "not indexed here" hint cleanly without parsing error strings.
var ErrModuleNotFound = errors.New("module not indexed")

// ScipSymbolLookup is re-exported from internal/scip so consumers
// outside the canopy module don't need to import an internal package
// just to type-assert the return value of Canopy.LookupSymbol.
type ScipSymbolLookup = canopyscip.SymbolLookupResult

// ScipSymbolReferences is the wire shape for Canopy.LookupReferences —
// every occurrence of a symbol in (module, version)'s SCIP index.
type ScipSymbolReferences = canopyscip.SymbolReferencesResult

// ScipXRefs is the wire shape for Canopy.LookupXRefs — every
// occurrence of a symbol across EVERY indexed (module, version),
// grouped by module. Empty groups are omitted.
type ScipXRefs = canopyscip.XRefsResult

// ModuleSummary is one row of the /api/modules listing — a flat
// view of the corpus the UI uses to render its browse page. We
// surface the latest version + the count rather than the full list
// to keep the listing payload bounded; users click through to
// /modules/<name> for the per-version detail.
//
// HasSourceIndex distinguishes "we ran scip-bazel and it produced
// indexable Starlark documents" from "we ran scip-bazel and it
// emitted an empty index" (a real outcome for C-library wrappers
// like zlib whose tarballs ship zero .bzl files). The UI hides
// "Code →" surfaces for modules where this is false so the user
// never clicks into a misleading empty file tree.
type ModuleSummary struct {
	Name           string `json:"name"`
	LatestVersion  string `json:"latest_version"`
	VersionCount   int    `json:"version_count"`
	HasSourceIndex bool   `json:"has_source_index"`
	// Homepage + MaintainerCount come from the mirror's per-module
	// metadata.json (lifted from upstream BCR during Bump). Empty
	// for modules whose mirror hasn't yet been enriched — re-bump
	// to populate. Counts not full lists: keeps the listing payload
	// flat across hundreds of modules.
	Homepage        string `json:"homepage,omitempty"`
	MaintainerCount int    `json:"maintainer_count,omitempty"`
	// LatestIngestedAt is the wall-clock time the latest version
	// was first written to canopy's index, formatted as RFC3339.
	// Drives the "X ago" freshness badge on /modules cards.
	LatestIngestedAt string `json:"latest_ingested_at,omitempty"`
	// IsNew flags first-version-only modules ingested within the
	// last 7 days. Drives the "NEW" badge on /modules cards.
	IsNew bool `json:"is_new,omitempty"`
	// LatestDiffHref is the pre-shaped URL for the structured
	// diff from the next-older non-stub version to the latest
	// non-stub version. Empty when there's no meaningful pair
	// (single-version modules, or every other version is a stub).
	// Drives the "diff" action on /modules cards.
	LatestDiffHref string `json:"latest_diff_href,omitempty"`
	// RepoLabel is a display-ready short label for the source
	// repository ("owner/repo" form when we can extract it).
	// Computed server-side from metadata.repository[0] when
	// present, or by parsing a github.com homepage URL. Empty
	// when no recognizable repo identity is available — UI
	// falls back to the homepage hostname.
	RepoLabel string `json:"repo_label,omitempty"`
	// UsageCount is the number of OTHER indexed module-versions whose
	// bazel_deps reference this module (by name, any version). A
	// rough popularity signal that complements VersionCount — modules
	// at the bottom of the dep graph (bazel_skylib, platforms) score
	// high, leaf modules score 0.
	UsageCount int `json:"usage_count,omitempty"`

	// Drift surfaces the cached drift signal for the latest version.
	// Populated by ListModules/GetModule from versions.drift_summary_json
	// (Plan 22 PR 3); empty struct = no drift data (unknown). Drives
	// the inline drift chip on /modules cards + module-detail title
	// strip (Plan 19 Idea A). See internal/api/drift.go for the
	// status taxonomy.
	Drift DriftSummary `json:"drift"`
}

// ExampleFile is one inlined file from a module's example directory.
// Bytes is omitted (and Truncated set) when the file exceeds the
// per-file size cap — UI falls back to the code-nav link in that case.
type ExampleFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Bytes     string `json:"bytes,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ExampleDirContents is what ExampleFiles returns: the file
// enumeration of one example/ or examples/ root directory, capped
// to keep the response small. Truncated signals the caller should
// link to code-nav for the full tree.
type ExampleDirContents struct {
	Dir       string        `json:"dir"`
	Files     []ExampleFile `json:"files"`
	Truncated bool          `json:"truncated,omitempty"`
}

// ReverseDep is one entry in a module's reverse-closure list: a
// (name, version) coordinate that has this module in its bazel_deps.
// Used by the module page's "Used by" surface — "what depends on
// this?" is the symmetric counterpart of "what does this depend on?"
// and the question Bazel maintainers ask before bumping.
type ReverseDep struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ReverseDeps is the wire shape of GET /api/modules/{m}/{v}/reverse-deps.
// Empty Deps when nothing in the index uses this module-version yet
// — distinguishes "we looked" (the field is present, just empty)
// from "we didn't look" (the response wouldn't have it).
type ReverseDeps struct {
	Module  string       `json:"module"`
	Version string       `json:"version"`
	Deps    []ReverseDep `json:"deps"`
}

// IngestClosureResult summarizes a "bump everything missing from the
// closure" run. Per-coordinate errors are collected, not propagated
// — a single broken transitive dep shouldn't abort the rest. The
// caller can show which coordinates failed and let the user retry.
type IngestClosureResult struct {
	Bumped int                  `json:"bumped"`
	Failed int                  `json:"failed"`
	Errors []IngestClosureError `json:"errors,omitempty"`
}

// IngestClosureError pairs a coordinate with the error message it
// produced during ingest. Useful for diagnostics + a future
// "retry this one" UI affordance.
type IngestClosureError struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// ClosureNode is one entry in a transitive bazel_dep graph.
// External=true marks nodes we couldn't resolve through the local
// store — they appear in some parent's bazel_deps but haven't been
// ingested. UI typically renders these in a muted style so readers
// can see "the closure reaches further than canopy knows."
type ClosureNode struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	External bool   `json:"external,omitempty"`
}

// ClosureEdge is one parent→child dep declaration.
// Names are NodeKey strings ("name@version") so the UI can de-dup
// without re-keying. Direction is always parent → child.
type ClosureEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ClosureGraph is the transitive bazel_dep graph rooted at one
// (module, version). Computed by walking the persisted reports
// breadth-first from the root. Bounded by the actual closure size
// — a module's transitive closure is finite and typically ~30-50
// nodes at most (Bazel's MVS resolution).
type ClosureGraph struct {
	Root    string        `json:"root"`
	Nodes   []ClosureNode `json:"nodes"`
	Edges   []ClosureEdge `json:"edges"`
	// MaxDepthReached signals the BFS hit the depth cap and stopped
	// expanding — anything past that is in the graph as a leaf but
	// without its OWN children. Renderers can show a "+N more"
	// hint at those nodes.
	MaxDepthReached bool `json:"max_depth_reached,omitempty"`
}

// Canopy is the operations interface implemented by the canopy backend service.
type Canopy interface {
	// Search runs a full-text + faceted query against the index.
	Search(ctx context.Context, q Query) (*SearchResults, error)

	// GetModuleVersion returns the full ModuleReport for one (name, version).
	GetModuleVersion(ctx context.Context, name, version string) (*report.ModuleReport, error)

	// ReverseDeps returns indexed modules whose bazel_deps include
	// the given (name, version). Symmetric counterpart to Closure:
	// "what depends on this?" vs "what does this depend on?"
	// Useful for impact analysis ("if I bump rules_go, who needs
	// attention?") and for understanding a module's role in the
	// corpus.
	ReverseDeps(ctx context.Context, name, version string) (*ReverseDeps, error)

	// IngestClosureMissing walks the closure of (name, version) and
	// runs Bump for every coordinate that's referenced as a bazel_dep
	// somewhere in the closure but isn't currently in the index. Used
	// to fill in External=true nodes the closure graph reveals — the
	// one-click "make canopy know about the rest of my dep tree"
	// affordance.
	IngestClosureMissing(ctx context.Context, name, version string) (*IngestClosureResult, error)

	// Closure walks the bazel_dep graph of (name, version)
	// breadth-first via persisted ModuleReports and returns a
	// (nodes, edges) shape suitable for graph rendering. Modules
	// referenced by some report's bazel_deps but missing from the
	// store are returned as External=true nodes so the renderer
	// can show "the closure extends beyond canopy's index" cleanly.
	Closure(ctx context.Context, name, version string) (*ClosureGraph, error)

	// ExampleFiles enumerates inlined contents of a module's
	// example directory. Caps total bytes + file count so the
	// response stays small; the UI falls back to code-nav for
	// the full tree. dir must be one of the directory names
	// reported via ModuleReport.Assets.ExampleDirs.
	ExampleFiles(ctx context.Context, name, version, dir string) (*ExampleDirContents, error)

	// Summary returns the bazel-module-summary-go view of one
	// (name, version): MODULE.bazel essentials + README/LICENSE/
	// examples from the unpacked source + maintainers/homepage/etc.
	// from the registry's metadata.json. This is the "first impression"
	// shape used by the MCP tool, future CLIs, and any agent flow
	// asking "tell me about <module>".
	//
	// Requires that the requested (m, v) is already in canopy's
	// sources cache + mirror — usually true after a Bump or after
	// IngestRecursive against the standard upstream. Returns a clear
	// error otherwise so callers can suggest re-ingesting.
	Summary(ctx context.Context, name, version string) (*bzlsummary.Summary, error)

	// ListVersions returns the known versions of a module, newest first.
	ListVersions(ctx context.Context, name string) ([]string, error)

	// ListModules returns one summary per indexed module — name +
	// version count + latest version. Powers the /modules overview
	// page where users browse the corpus without knowing what to
	// search for. Sorted by module name ASC for stable wire output.
	ListModules(ctx context.Context) ([]ModuleSummary, error)

	// GetModule returns the same shape as one entry from ListModules
	// for a single named module. Powers the cross-module hover card,
	// which needs lightweight (latest version + version count + repo
	// label + hermeticity hint) data per hovered link without
	// requiring the UI to fetch the whole catalogue. Returns
	// ErrModuleNotFound when the name isn't indexed — callers map
	// that to HTTP 404 so the hover card can render a "not indexed
	// here" hint cleanly.
	GetModule(ctx context.Context, name string) (*ModuleSummary, error)

	// IngestDir reads a module source directory (containing MODULE.bazel),
	// runs the introspection pipeline, and writes the resulting report to the
	// index. Used by `bzlhub ingest` and by publish-time hooks later.
	IngestDir(ctx context.Context, dir string) (*report.ModuleReport, error)

	// Drift compares the canopy mirror's contents (whatever the configured
	// Backend serves) against an upstream BCR-shape registry. Returns a
	// per-module status report. Requires that the running serve was started
	// with --root pointing at a real filesystem mirror; returns an error
	// otherwise.
	Drift(ctx context.Context, opts DriftOptions) (*drift.Report, error)

	// Bump fetches one (module, version) from an upstream BCR-shape registry,
	// mirrors it into the local tree (modules/<n>/<v>/{MODULE.bazel,source.json},
	// blobs/<basename>, metadata.json merge), runs assay on the extracted
	// source, and writes the resulting ModuleReport to the search index.
	// Idempotent: re-bumping the same version overwrites the existing entry
	// byte-for-byte. Requires a MirrorRoot like Drift does.
	Bump(ctx context.Context, opts BumpOptions) (*report.ModuleReport, error)

	// CompatCheck runs the analyzer against a MODULE.bazel text blob,
	// returning per-dep diff against the latest indexed version.
	// Read-only and stateless from the operator's perspective; doesn't
	// hit the network. See internal/compat for the analyzer body.
	CompatCheck(ctx context.Context, body string, opts CompatCheckOptions) (*compat.Result, error)

	// IngestRecursive walks the bazel_dep closure starting from
	// (opts.Module, opts.Version), mirroring every reached version into
	// the configured MirrorRoot. Each successfully-processed module
	// publishes a module_indexed event so subscribed UIs/agents can
	// render a live progress feed. Returns the walker's Result; the
	// call blocks until the closure is fully walked.
	//
	// Errors during the walk are recorded in Result.Errors and don't
	// abort sibling fetches (partial closures are useful as drift
	// inputs). A nil Result + non-nil error indicates a hard precondition
	// failure (e.g., no MirrorRoot).
	IngestRecursive(ctx context.Context, opts IngestRecursiveOptions) (*IngestRecursiveResult, error)

	// Diff compares two ModuleReports of the same module and returns a
	// structured delta: bazel_deps added/removed/changed,
	// compatibility_level changes, rule/provider/macro/aspect/toolchain/
	// repository_rule/module_extension additions and removals, attribute
	// schema deltas inside changed rules, tag_class deltas inside
	// changed module_extensions, and hermeticity-class differences.
	//
	// When DiffOptions.Upstream is empty, both versions must already be
	// in the local index; a missing version yields an error.
	// When Upstream is provided, any side missing locally is fetched and
	// analyzed on-the-fly without persisting (a "what-if" diff). The
	// returned Report's FromSource/ToSource fields record which side
	// came from where.
	Diff(ctx context.Context, opts DiffOptions) (*modulediff.Report, error)

	// DiffClosure extends Diff into a full bazel_dep closure walk. It
	// resolves the closure for both sides via MVS, surfaces dep
	// added/removed/version-changed at the closure level, runs a
	// modulediff for every module whose version changed (including the
	// root), and rolls up the per-module breaking findings into a
	// closure-wide total. The killer field is ClosureBreakingTotal — a
	// migration's true blast radius isn't just the root, it's every
	// transitive dep that gets dragged along by the MVS bump.
	//
	// Requires Upstream (closure walking needs a registry). Each module
	// pair is analyzed via the same local-or-upstream fallback as Diff.
	DiffClosure(ctx context.Context, opts DiffOptions) (*closurediff.Report, error)

	// GetScipBlob returns the binary protobuf SCIP index for
	// (module, version), produced by canopy's ingest pipeline via
	// scip-bazel. Bytes are exactly what an external SCIP consumer
	// (Sourcegraph CLI, IDE plugin, custom navigator) expects —
	// canopy serves them verbatim with content-type
	// application/vnd.sourcegraph.scip+protobuf.
	//
	// Returns a sentinel "not found" error when no index has been
	// generated for that pair yet (e.g. ingest predates scip-bazel
	// wiring, or scip generation failed for that module). Callers
	// should map that to HTTP 404.
	GetScipBlob(ctx context.Context, module, version string) ([]byte, error)

	// LookupSymbol resolves a full SCIP symbol string against the
	// stored index for (module, version). Returns the symbol's
	// definition site (file + range + documentation), or Found=false
	// when the symbol isn't defined in this index (canonical for
	// external references). The Symbol string is the canopy scheme
	// "bzlmod <module>@<version> <relpath>#<name>".
	//
	// Backed by understory v0.1.0 — generic SCIP query library that
	// fills the post-Sourcegraph-OSS gap. Future canopy iterations
	// may expand this to References / Hover / SymbolAtPos as
	// understory exposes them.
	LookupSymbol(ctx context.Context, module, version, symbol string) (*ScipSymbolLookup, error)

	// LookupReferences returns every occurrence of the symbol in
	// (module, version)'s SCIP index — call sites, variable reads,
	// plus the definition iff includeDefinition is true. Powers
	// agent / UI questions like "where is this rule used in this
	// module?". Returns Count=0 + empty array for both "symbol not
	// in index" and "symbol present but unreferenced".
	LookupReferences(ctx context.Context, module, version, symbol string, includeDefinition bool) (*ScipSymbolReferences, error)

	// LookupXRefs walks every (module, version) canopy has a SCIP
	// index for, returning every occurrence of `symbol` grouped by
	// module. Powers the cross-module "Used by" panel in the embedded
	// code-nav UI: without it the panel only sees the symbol's own
	// module, missing every consumer.
	//
	// Cost is O(modules) blob loads; for small indexes (≤ a few
	// hundred modules) this stays well under 100 ms. A denormalized
	// symbol→occurrence index is the next-iteration target.
	LookupXRefs(ctx context.Context, symbol string, includeDefinition bool) (*ScipXRefs, error)

	// LookupConsumers wraps LookupXRefs with name-resolution + a
	// consumer-shaped response (Plan 07). The caller passes a
	// user-facing identifier (rule / provider / macro / repo_rule /
	// module_extension name) for (module, version); the server
	// resolves it to a SCIP symbol via the stored ModuleReport's
	// Provenance.File and runs the xref pass with
	// includeDefinition=false. The defining module's own occurrences
	// are filtered out by default — pass includeSelf=true to keep
	// them (useful for "where is this used INCLUDING in the source
	// repo's own examples?").
	//
	// Returns ("", "not found") when the name doesn't resolve to any
	// symbol in (module, version)'s report. Empty Consumers means
	// the symbol resolved but no other indexed module references
	// it — distinct from a not-found error.
	LookupConsumers(ctx context.Context, module, version, name string, includeSelf bool) (*ConsumersResult, error)

	// History returns recent audit events, newest-first. The audit log
	// captures every write operation (bump, ingest, recursive ingest)
	// with its source surface (drift-ui / cli / mcp / rest), success
	// flag, and a structured payload. Read ops are intentionally NOT
	// logged — too noisy and the absence of an audit trail isn't a
	// "what happened?" question.
	History(ctx context.Context, opts HistoryOptions) ([]AuditEvent, error)

	// ExternalSurface returns the per-module URL surface — every URL
	// the module's repository_rule / module_extension implementations
	// would fetch, classified by ecosystem and labeled with the per-
	// fork tainted flag where interpretation depended on opaque state.
	// Populated by canopy/internal/external.IngestModule during ingest.
	ExternalSurface(ctx context.Context, name, version string) (*ExternalSurfaceResponse, error)

	// AirgapSurface unions ExternalSurface across the full transitive
	// closure of <name, version>. Returns one row per closure node + a
	// deduplicated ref list spanning every captured URL across the
	// closure. Empty Refs from a node (not yet ingested or with no
	// rules) still produce a Module entry so the UI can show what's
	// missing from the airgap inventory.
	AirgapSurface(ctx context.Context, name, version string) (*ClosureSurfaceResponse, error)

	// AirgapDownloaderConfig renders a Bazel
	// `--experimental_downloader_config`-shaped text file that
	// rewrites every URL canopy knows about for (module, version) to
	// a mirror-prefix the operator supplies. Per-module by default;
	// recursive=true unions the bazel_dep closure first.
	AirgapDownloaderConfig(ctx context.Context, name, version string, opts DownloaderConfigOptions) (*DownloaderConfig, error)

	// AirgapModuleMirrors renders a Bazel `--module_mirrors`-shaped
	// .bazelrc snippet. Targets Bazel >= 8.4; sibling to
	// AirgapDownloaderConfig — covers the registry slice only (modules
	// pulled through a Bazel registry), while AirgapDownloaderConfig
	// covers every URL. The (module, version) identifies the surface
	// for which the mirror is being configured, but the rendered line
	// is registry-scoped, not module-scoped, so the same output applies
	// to any module sourced from the same registry.
	AirgapModuleMirrors(ctx context.Context, name, version string, opts ModuleMirrorsOptions) (*ModuleMirrors, error)
}

// HistoryOptions filters a History call.
type HistoryOptions struct {
	// Kinds limits to specific event kinds (e.g. "bump_success",
	// "ingest_recursive_failure"). Empty → all kinds.
	Kinds []string

	// Source filters by surface tag (e.g. "drift-ui", "mcp"). Empty → any.
	Source string

	// Module filters by module name. Empty → any.
	Module string

	// Limit caps the response. 0 → 100; max 10000.
	Limit int
}

// AuditEvent is the wire shape of one history entry. Fields map 1:1
// with store.AuditEvent; we re-declare it here so the api package
// remains the cohesion seam (server / mcp / canopy all import api).
type AuditEvent struct {
	ID         int64           `json:"id"`
	Timestamp  string          `json:"timestamp"`         // RFC3339Nano
	Kind       string          `json:"kind"`
	Source     string          `json:"source"`
	Module     string          `json:"module,omitempty"`
	Version    string          `json:"version,omitempty"`
	OK         bool            `json:"ok"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	Error      string          `json:"error,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// IngestRecursiveOptions configures a closure walk.
type IngestRecursiveOptions struct {
	Module               string // root module name (required)
	Version              string // root version (required)
	Upstream             string // registry URL; "" → DefaultUpstream
	IncludeBazelTools    bool   // seed with bazeltools.LookupDeps(BazelVersion)
	BazelVersion         string // used when IncludeBazelTools=true; default "9.1.0"
	Workers              int    // concurrent fetchers; 0 → 8
	Source               string // audit tag ("drift-ui" | "cli" | "mcp" | ...)
}

// IngestRecursiveResult is the walker's summary, projected from
// ingest.Result to keep the api package free of ingest-internal types.
type IngestRecursiveResult struct {
	Visited  int                  `json:"visited"`
	Mirrored int                  `json:"mirrored"`
	Errors   []RecursiveIngestErr `json:"errors,omitempty"`
}

// RecursiveIngestErr is one failed module in a closure walk.
type RecursiveIngestErr struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// DiffOptions configures a Diff call.
type DiffOptions struct {
	// Module name. Required.
	Module string

	// FromVersion / ToVersion: the two coordinates being compared. Required.
	FromVersion string
	ToVersion   string

	// Upstream, if non-empty, lets the service fall back to fetching+
	// analyzing missing versions from this registry without persisting.
	// Empty → fail with a "not in index" error if either side is missing.
	Upstream string
}

// BumpOptions configures a one-shot ingest of a specific (module, version)
// from upstream into the mirror + index.
type BumpOptions struct {
	// Module name (e.g., "rules_go").
	Module string

	// Version to ingest (e.g., "0.52.0", or a 4-component canopy variant
	// like "0.52.0.1"). Must be present in upstream's metadata.json.
	Version string

	// Upstream registry URL. Empty → service.DefaultUpstream.
	Upstream string

	// Source tags the audit log entry written for this call: "drift-ui",
	// "cli", "mcp", "rest", "unknown". Pass-through to the store; never
	// used for control flow.
	Source string
}

// EventSubscriber is the slice of the Canopy contract used by the SSE
// stream. Implementations expose a Subscribe(buf) returning a channel of
// JSON-serializable events plus an unsubscribe function. The server
// type-asserts on this to decide whether real events are available;
// nil implementations still get a plain keep-alive stream.
type EventSubscriber interface {
	Subscribe(buf int) (<-chan SSEEvent, func())
}

// SSEEvent is the wire shape of one server-sent event. Kind becomes the
// SSE "event:" field; Data is JSON-marshaled into the "data:" field.
type SSEEvent struct {
	Kind string
	Data any
}

// DriftOptions configures a drift run.
type DriftOptions struct {
	// Upstream registry URL to diff against. If empty, the service uses its
	// configured default (typically https://bcr.bazel.build).
	Upstream string

	// Module, if non-empty, limits the scan to that single module name.
	Module string

	// Workers caps concurrent upstream fetches. 0 → service default.
	Workers int
}

// Query is the input to Search.
type Query struct {
	// Text is the free-text query (matched against module names, rule/provider
	// names, doc strings via FTS5).
	Text string

	// Hermeticity filters results to modules whose hermeticity profile
	// contains AT LEAST ONE of the listed classes.
	Hermeticity []report.HermeticityClass

	// Attr filters to rules / repository_rules whose attrs contain an
	// attribute with this exact name. Cross-corpus question — "every
	// rule taking `srcs`" — that FTS5 can't naturally express because
	// attr names live in nested ModuleReport structure, not in the
	// indexed-text columns.
	Attr string

	// Kind narrows the search to a specific symbol kind: "rule",
	// "provider", "macro", "repo_rule", "module_extension". When set,
	// Text is interpreted as the exact symbol name to match. Powers
	// the rule:NAME / provider:NAME / macro:NAME / repo_rule:NAME
	// prefixes from the search bar. Same cross-corpus walk pattern as
	// Attr — defers to inverted-index when corpus crosses thousands.
	Kind string

	// Limit caps the number of hits returned. 0 = default (e.g., 50).
	Limit int
}

// SearchResults is the output of Search.
type SearchResults struct {
	Hits  []Hit `json:"hits"`
	Total int   `json:"total"`
}

// Hit is one search result, with enough context to render a row in the UI
// without a follow-up RPC. Heavier detail (full rule attribute schemas, the
// whole hermeticity findings list) requires a GetModuleVersion call.
type Hit struct {
	Module   string                    `json:"module"`
	Version  string                    `json:"version"`
	Snippet  string                    `json:"snippet,omitempty"`
	MatchKind string                   `json:"match_kind"` // "module" | "rule" | "provider" | "macro"
	MatchName string                   `json:"match_name,omitempty"`
	// File is the source path the matched definition was declared in
	// (relative to the module root). Lets the UI disambiguate same-named
	// symbols (rules_cc defines `cc_toolchain_config` once per platform
	// in separate .bzl files). Empty for "module"-kind hits and for
	// macros without provenance.
	File        string                    `json:"file,omitempty"`
	Hermeticity []report.HermeticityClass `json:"hermeticity,omitempty"`
	// Attr is the attribute name that matched when the hit came from
	// an attribute-search (Query.Attr). Empty for free-text and
	// hermeticity-only hits.
	Attr string `json:"attr,omitempty"`
	// HasSourceIndex is true when (Module, Version)'s SCIP blob has
	// at least one indexed Starlark document. The UI gates the
	// per-result "Code →" link on this — modules with empty SCIP
	// (e.g. C-library wrappers like zlib that ship zero .bzl files)
	// would otherwise land the user on a friendly 404. Read from the
	// cached versions.has_source_index column; populated by the
	// ingest path after each WriteScipBlob and reconciled at boot
	// for pre-migration rows.
	HasSourceIndex bool `json:"has_source_index"`
}
