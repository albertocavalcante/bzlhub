package gobzlmod

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/albertocavalcante/go-bzlmod/bazeltools"
	"github.com/albertocavalcante/go-bzlmod/graph"
)

// ModuleInfo represents the information extracted from a MODULE.bazel file.
// It contains the module's identity, version, and all its direct dependencies
// and overrides.
//
// Reference: This corresponds to Bazel's InterimModule, which represents a module
// during the dependency resolution phase before final selection.
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java
type ModuleInfo struct {
	// Name is the module name as declared in module(name = "...").
	Name string `json:"name"`

	// Version is the module version as declared in module(version = "...").
	Version string `json:"version"`

	// CompatibilityLevel indicates breaking changes. Modules with different
	// compatibility levels are considered incompatible.
	CompatibilityLevel int `json:"compatibility_level"`

	// BazelCompatibility specifies Bazel version constraints for this module.
	// Each entry must match the pattern: (>=|<=|>|<|-)X.Y.Z
	// Examples: ">=7.0.0", "<8.0.0", "-7.1.0" (excludes specific version)
	//
	// Reference: ModuleFileGlobals.java lines 65-66
	// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/ModuleFileGlobals.java
	BazelCompatibility []string `json:"bazel_compatibility,omitempty"`

	// Dependencies lists all bazel_dep declarations in the module file.
	Dependencies []Dependency `json:"dependencies"`

	// NodepDependencies lists dependencies that participate in version selection but
	// don't create transitive dependency edges. These come from use_extension declarations
	// that reference modules the extension depends on. Modules reachable only via nodep
	// deps are not included in the final dependency graph.
	//
	// Reference: Discovery.java lines 62-78 and InterimModule.java nodepDeps field
	// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Discovery.java
	NodepDependencies []Dependency `json:"nodep_dependencies,omitempty"`

	// Overrides lists all override declarations (single_version, git, etc.).
	Overrides []Override `json:"overrides"`
}

// Dependency represents a bazel_dep declaration in a MODULE.bazel file.
// It declares a direct dependency on another Bazel module.
//
// Reference: This corresponds to Bazel's InterimModule.DepSpec (lines 59-81),
// which represents a dependency specification before resolution.
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/InterimModule.java
//
// MaxCompatibilityLevel corresponds to DepSpec.maxCompatibilityLevel(), which
// constrains the maximum compatibility level allowed for the resolved version.
type Dependency struct {
	// Name is the module name being depended upon.
	Name string `json:"name"`

	// Version is the minimum required version.
	Version string `json:"version"`

	// MaxCompatibilityLevel specifies the maximum compatibility level allowed
	// for this dependency. If set (> 0), the resolved version must have a
	// compatibility_level <= this value. This allows depending on modules
	// that may upgrade within the same compatibility level range.
	// A value of 0 means no constraint (default).
	MaxCompatibilityLevel int `json:"max_compatibility_level,omitempty"`

	// RepoName overrides the default repository name for this dependency.
	// If empty, the module name is used as the repo name.
	RepoName string `json:"repo_name,omitempty"`

	// DevDependency indicates this is only needed for development/testing.
	DevDependency bool `json:"dev_dependency"`

	// IsNodepDep indicates this dependency participates in version selection but
	// doesn't create transitive dependency edges. Nodep dependencies are used by
	// module extensions (use_extension) to reference modules they depend on without
	// making them required dependencies. Modules reachable only via nodep deps are
	// not included in the final dependency graph.
	//
	// Reference: Discovery.java lines 62-78 and InterimModule.java nodepDeps field
	// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Discovery.java
	IsNodepDep bool `json:"is_nodep_dep,omitempty"`
}

// Override represents version or source overrides for a module dependency.
// Override types include: single_version, multiple_version, git, local_path, archive.
//
// Reference: Bazel's override hierarchy includes:
//   - SingleVersionOverride: Pins a module to a specific version within the registry.
//     See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/SingleVersionOverride.java
//   - NonRegistryOverride: Base for overrides that bypass the registry (git, local_path, archive).
//     See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/NonRegistryOverride.java
type Override struct {
	// Type is the override type: "single_version", "multiple_version", "git", "local_path", or "archive".
	Type string `json:"type"`

	// ModuleName is the name of the module being overridden.
	ModuleName string `json:"module_name"`

	// Version is the pinned version (for single_version overrides).
	Version string `json:"version,omitempty"`

	// Versions is the allowed version set (for multiple_version overrides).
	Versions []string `json:"versions,omitempty"`

	// Registry overrides the registry URL for this module.
	Registry string `json:"registry,omitempty"`

	// Path is the local filesystem path for local_path overrides.
	Path string `json:"path,omitempty"`
}

// ResolutionList contains the final resolved dependency set after MVS.
//
// Reference: This corresponds to Bazel's Selection.Result (lines 88-100),
// which holds the outcome of the module version selection algorithm.
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java
type ResolutionList struct {
	// Modules is the list of all resolved modules, sorted by name.
	Modules []ModuleToResolve `json:"modules"`

	// Summary provides aggregate statistics about the resolution.
	Summary ResolutionSummary `json:"summary"`

	// Warnings contains non-fatal issues encountered during resolution.
	// For example, yanked version warnings when YankedVersionWarn is used.
	Warnings []string `json:"warnings,omitempty"`

	// RegistryFileHashes records Bazel-style registry file accesses made during
	// resolution when TraceRegistryFiles is enabled.
	//
	// Keys are canonical registry URLs for MODULE.bazel and source.json files.
	// Values are SHA-256 hex digests of the fetched content.
	// A nil value means the file was probed but not found in that registry,
	// which matches Bazel's "not found" lockfile semantics for higher-priority
	// registries that missed before a lower-priority registry succeeded.
	RegistryFileHashes map[string]*string `json:"registry_file_hashes,omitempty"`

	// Graph is the dependency graph for advanced queries.
	// Use this for bazel mod graph/explain equivalent functionality.
	// Supports: Explain(), Path(), AllPaths(), ToJSON(), ToDOT(), ToText()
	Graph *graph.Graph `json:"-"`
}

// ModuleToResolve represents a module selected by dependency resolution.
// It includes the selected version and metadata about why it was included.
type ModuleToResolve struct {
	// Name is the module name.
	Name string `json:"name"`

	// Version is the selected version (highest required by any dependent).
	Version string `json:"version"`

	// Registry is the URL to fetch this module from.
	Registry string `json:"registry"`

	// Depth is the shortest path length from root to this module.
	// 0 = root (not in Modules list), 1 = direct dependency, 2+ = transitive.
	Depth int `json:"depth"`

	// DevDependency indicates if this module is only a dev dependency.
	DevDependency bool `json:"dev_dependency"`

	// Dependencies lists the modules this one depends on (by name).
	// These are the resolved dependency names, not versions.
	Dependencies []string `json:"dependencies,omitempty"`

	// DependencyKeys lists the modules this one depends on as fully qualified
	// module keys ("name@version"). This disambiguates visible duplicate module
	// names when IncludeUnusedModules is enabled.
	DependencyKeys []string `json:"dependency_keys,omitempty"`

	// RequiredBy lists the modules that depend on this one.
	RequiredBy []string `json:"required_by"`

	// Yanked indicates if this version has been yanked from the registry.
	// Check YankReason for details on why.
	Yanked bool `json:"yanked,omitempty"`

	// YankReason explains why the version was yanked.
	// Empty if not yanked.
	YankReason string `json:"yank_reason,omitempty"`

	// IsDeprecated indicates the module is deprecated.
	// Check DeprecationReason for details.
	IsDeprecated bool `json:"deprecated,omitempty"`

	// DeprecationReason explains why the module is deprecated.
	DeprecationReason string `json:"deprecation_reason,omitempty"`

	// BazelCompatibility contains the bazel_compatibility constraints for this module.
	// Empty if no constraints were declared.
	BazelCompatibility []string `json:"bazel_compatibility,omitempty"`

	// IsBazelIncompatible indicates the module is incompatible with the target Bazel version.
	// Check BazelIncompatibilityReason for details.
	IsBazelIncompatible bool `json:"bazel_incompatible,omitempty"`

	// BazelIncompatibilityReason explains why the module is incompatible.
	BazelIncompatibilityReason string `json:"bazel_incompatibility_reason,omitempty"`

	// Unused indicates the module is visible only because IncludeUnusedModules
	// was enabled for this resolution.
	Unused bool `json:"unused,omitempty"`

	// Source contains information about how to fetch this module's source code.
	// It is populated when TraceRegistryFiles is enabled.
	// It can describe archive, git_repository, or local_path sources.
	Source *SourceInfo `json:"source,omitempty"`
}

// SourceInfo describes how to fetch a module's source code.
// This is a simplified view of registry.Source for external use.
//
// Reference: IndexRegistry.java lines 264-295
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/IndexRegistry.java
type SourceInfo struct {
	// Type is the source type: "archive", "git_repository", or "local_path".
	Type string `json:"type"`

	// --- Archive fields ---

	// URL is the download URL for archive sources.
	URL string `json:"url,omitempty"`

	// Integrity is the SRI hash for archive sources (e.g., "sha256-...").
	Integrity string `json:"integrity,omitempty"`

	// StripPrefix is the directory prefix to strip from the archive or git repo.
	StripPrefix string `json:"strip_prefix,omitempty"`

	// --- Git repository fields ---

	// Remote is the Git repository URL.
	Remote string `json:"remote,omitempty"`

	// Commit is the Git commit hash to checkout.
	Commit string `json:"commit,omitempty"`

	// Tag is the Git tag to checkout (alternative to Commit).
	Tag string `json:"tag,omitempty"`

	// --- Local path fields ---

	// Path is the local filesystem path.
	Path string `json:"path,omitempty"`
}

// Key returns a unique identifier for this module in "name@version" format.
func (m ModuleToResolve) Key() string {
	return m.Name + "@" + m.Version
}

// ProductionModules returns all non-dev dependency modules.
func (r *ResolutionList) ProductionModules() []ModuleToResolve {
	var result []ModuleToResolve
	for _, m := range r.Modules {
		if !m.DevDependency {
			result = append(result, m)
		}
	}
	return result
}

// DevModules returns all dev dependency modules.
func (r *ResolutionList) DevModules() []ModuleToResolve {
	var result []ModuleToResolve
	for _, m := range r.Modules {
		if m.DevDependency {
			result = append(result, m)
		}
	}
	return result
}

// Module returns the module with the given name, or nil if not found.
func (r *ResolutionList) Module(name string) *ModuleToResolve {
	for i := range r.Modules {
		if r.Modules[i].Name == name {
			return &r.Modules[i]
		}
	}
	return nil
}

// DirectDeps returns all direct dependencies (modules at depth 1).
func (r *ResolutionList) DirectDeps() []ModuleToResolve {
	var result []ModuleToResolve
	for _, m := range r.Modules {
		if m.Depth == 1 {
			result = append(result, m)
		}
	}
	return result
}

// TransitiveDeps returns all transitive dependencies (modules at depth > 1).
func (r *ResolutionList) TransitiveDeps() []ModuleToResolve {
	var result []ModuleToResolve
	for _, m := range r.Modules {
		if m.Depth > 1 {
			result = append(result, m)
		}
	}
	return result
}

// HasModule returns true if a module with the given name is in the resolution.
func (r *ResolutionList) HasModule(name string) bool {
	return r.Module(name) != nil
}

// ResolutionSummary provides statistics about the dependency resolution result.
type ResolutionSummary struct {
	// TotalModules is the total count of resolved modules.
	TotalModules int `json:"total_modules"`

	// ProductionModules is the count of non-dev dependencies.
	ProductionModules int `json:"production_modules"`

	// DevModules is the count of dev-only dependencies.
	DevModules int `json:"dev_modules"`

	// YankedModules is the count of modules with yanked versions.
	YankedModules int `json:"yanked_modules,omitempty"`

	// DeprecatedModules is the count of deprecated modules.
	DeprecatedModules int `json:"deprecated_modules,omitempty"`

	// IncompatibleModules is the count of modules incompatible with the target Bazel version.
	IncompatibleModules int `json:"incompatible_modules,omitempty"`

	// FieldWarnings lists warnings about bzlmod fields that aren't supported
	// in the target Bazel version. These warnings are informational and don't
	// block resolution. Examples include mirror_urls (requires 7.7.0+) or
	// max_compatibility_level (requires 7.0.0+).
	FieldWarnings []string `json:"field_warnings,omitempty"`
}

// YankedVersionBehavior controls how yanked versions are handled during resolution.
//
// Reference: Bazel handles yanked versions through YankedVersionsUtil, which provides
// utilities for checking and substituting yanked module versions.
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/YankedVersionsUtil.java
type YankedVersionBehavior int

const (
	// YankedVersionAllow allows yanked versions without error (default for backwards compatibility).
	// Yanked info is still populated in the result.
	YankedVersionAllow YankedVersionBehavior = iota

	// YankedVersionWarn allows yanked versions but includes warnings in the result.
	YankedVersionWarn

	// YankedVersionError rejects resolution if any yanked version is selected.
	YankedVersionError
)

// DirectDepsCheckMode controls how direct dependency version mismatches are handled.
type DirectDepsCheckMode int

const (
	// DirectDepsOff disables direct dependency checking (default).
	DirectDepsOff DirectDepsCheckMode = iota

	// DirectDepsWarn includes warnings when direct deps don't match resolved versions.
	DirectDepsWarn

	// DirectDepsError fails resolution if direct deps don't match resolved versions.
	DirectDepsError
)

// BazelCompatibilityMode controls how Bazel compatibility constraints are validated.
//
// Reference: BazelModuleResolutionFunction.java lines 298-333
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/BazelModuleResolutionFunction.java
type BazelCompatibilityMode int

const (
	// BazelCompatibilityOff disables Bazel compatibility checking (default).
	BazelCompatibilityOff BazelCompatibilityMode = iota

	// BazelCompatibilityWarn includes warnings when modules are incompatible with the Bazel version.
	BazelCompatibilityWarn

	// BazelCompatibilityError fails resolution if any module is incompatible with the Bazel version.
	BazelCompatibilityError
)

// LockfileMode controls how the lockfile is handled during resolution.
//
// Reference: BazelModuleResolutionFunction.java lines 113-147
// See: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/BazelModuleResolutionFunction.java
type LockfileMode int

const (
	// LockfileOff disables lockfile handling (default).
	// The lockfile is neither read nor written.
	LockfileOff LockfileMode = iota

	// LockfileUpdate reads the existing lockfile and updates it after resolution.
	// If no lockfile exists, a new one is created.
	// This is equivalent to Bazel's --lockfile_mode=update.
	LockfileUpdate

	// LockfileError fails if resolution differs from the lockfile.
	// This ensures reproducible builds by requiring the lockfile to match.
	// This is equivalent to Bazel's --lockfile_mode=error.
	LockfileError

	// LockfileRefresh ignores the existing lockfile and creates a fresh one.
	// All registry files are re-fetched and new hashes are computed.
	// This is equivalent to Bazel's --lockfile_mode=refresh.
	LockfileRefresh
)

// ProgressEventType identifies the type of progress event.
type ProgressEventType string

const (
	// ProgressResolveStart is emitted when resolution begins.
	ProgressResolveStart ProgressEventType = "resolve_start"

	// ProgressResolveEnd is emitted when resolution completes.
	ProgressResolveEnd ProgressEventType = "resolve_end"

	// ProgressModuleFetchStart is emitted when fetching a module begins.
	ProgressModuleFetchStart ProgressEventType = "module_fetch_start"

	// ProgressModuleFetchEnd is emitted when fetching a module completes.
	ProgressModuleFetchEnd ProgressEventType = "module_fetch_end"
)

// ProgressEvent contains information about resolution progress.
type ProgressEvent struct {
	// Type identifies the event type.
	Type ProgressEventType `json:"type"`

	// Module is the module name (for module_fetch_* events).
	Module string `json:"module,omitempty"`

	// Version is the module version (for module_fetch_* events).
	Version string `json:"version,omitempty"`

	// Message provides additional context about the event.
	Message string `json:"message,omitempty"`
}

// ResolutionOptions configures the dependency resolution behavior.
type ResolutionOptions struct {
	// IncludeDevDeps includes dev_dependency=True modules in resolution.
	IncludeDevDeps bool

	// YankedBehavior controls how yanked versions are handled.
	// Default is YankedVersionAllow.
	YankedBehavior YankedVersionBehavior

	// CheckYanked enables fetching metadata to detect yanked versions.
	// When false, Yanked/YankReason fields will not be populated.
	// Default is false for backwards compatibility.
	CheckYanked bool

	// AllowYankedVersions lists specific module@version pairs that are allowed
	// even if yanked. Use "all" as special keyword to allow all yanked versions.
	// Format: []string{"module@version", "other@1.0.0"} or []string{"all"}
	// Mirrors Bazel's --allow_yanked_versions flag.
	AllowYankedVersions []string

	// WarnDeprecated enables warnings when deprecated modules are used.
	// Default is false.
	WarnDeprecated bool

	// TraceRegistryFiles enables Bazel-style registry tracing.
	// When enabled, ResolutionList.RegistryFileHashes is populated with the
	// MODULE.bazel and source.json files touched during resolution, and
	// ModuleToResolve.Source is hydrated for registry-backed modules.
	TraceRegistryFiles bool

	// DirectDepsMode controls validation of direct dependency versions.
	// When enabled, checks if declared versions match resolved versions.
	// Default is DirectDepsOff for backwards compatibility.
	DirectDepsMode DirectDepsCheckMode

	// SubstituteYanked enables automatic substitution of yanked versions
	// with the next non-yanked version in the same compatibility level.
	// This matches Bazel's default behavior.
	// Default is false for backwards compatibility.
	SubstituteYanked bool

	// BazelCompatibilityMode controls validation of bazel_compatibility constraints.
	// When set to BazelCompatibilityWarn or BazelCompatibilityError, modules with
	// bazel_compatibility constraints that don't match BazelVersion will be flagged.
	// Requires BazelVersion to be set for validation to occur.
	// Default is BazelCompatibilityOff for backwards compatibility.
	BazelCompatibilityMode BazelCompatibilityMode

	// BazelVersion specifies which Bazel version's behavior to emulate.
	// When set, includes that version's MODULE.tools dependencies in resolution.
	// Format: "7.0.0", "8.0.0", etc.
	// Default is empty (no MODULE.tools deps included).
	BazelVersion string

	// IncludeBuiltinModules exposes Bazel built-in MODULE.tools dependencies in the
	// visible resolution result and graph. When false, built-ins still participate
	// in selection but remain hidden by default, matching `bazel mod graph`.
	//
	// This mirrors Bazel's --include_builtin flag for graph visibility.
	IncludeBuiltinModules bool

	// IncludeUnusedModules exposes modules from the unpruned post-selection
	// graph, including versions made unused by MVS or override rules.
	//
	// This mirrors Bazel's --include_unused flag for graph visibility.
	IncludeUnusedModules bool

	// BazelToolsLookup resolves the MODULE.tools dependencies for a Bazel version.
	// When nil, the built-in bazeltools package data is used.
	//
	// This allows callers to support Bazel versions newer than the library knows
	// about, custom Bazel forks, or HEAD/prerelease builds without modifying this
	// library. The function receives the raw BazelVersion string.
	//
	// Callers that want to extend, rather than replace, the built-in data can
	// fall back to bazeltools.LookupDeps(version) from inside the callback.
	BazelToolsLookup BazelToolsLookup

	// BazelToolsTransformer post-processes the implicit MODULE.tools dependencies
	// for a Bazel version after BazelToolsLookup is applied.
	//
	// The resolver passes a cloned slice, so the transformer can safely mutate
	// or replace it without affecting lookup-owned state. Returning nil means
	// "no implicit MODULE.tools dependencies" for that version.
	BazelToolsTransformer BazelToolsTransformer

	// Registries is an ordered list of registry URLs to search for modules.
	// When multiple registries are specified, modules are looked up in order.
	// The first registry where a module is found is used for ALL versions of that module.
	// This matches Bazel's --registry flag behavior.
	// If empty or nil, uses DefaultRegistries (BCR + GitHub mirror).
	//
	// Supported URL schemes:
	//   - https:// - Remote registry (e.g., "https://bcr.bazel.build")
	//   - http://  - Remote registry (not recommended for production)
	//   - file://  - Local registry (e.g., "file:///path/to/registry")
	//
	// Example: []string{"https://registry.example.com", "https://bcr.bazel.build"}
	// Airgap:  []string{"file:///opt/bazel-registry"}
	Registries []string

	// VendorDir specifies a directory containing vendored module files.
	// When set, modules are first looked up in this directory before
	// checking registries. This enables offline/airgap workflows.
	//
	// The vendor directory should have the same structure as a registry:
	//   vendor/modules/{name}/{version}/MODULE.bazel
	//
	// This mirrors Bazel's --vendor_dir flag behavior.
	VendorDir string

	// LockfileMode controls how the lockfile is handled during resolution.
	// Default is LockfileOff for backwards compatibility.
	//
	// Reference: BazelModuleResolutionFunction.java lines 113-147
	LockfileMode LockfileMode

	// LockfilePath specifies the path to the lockfile.
	// If empty and LockfileMode is not Off, defaults to "MODULE.bazel.lock"
	// in the same directory as the MODULE.bazel file.
	LockfilePath string

	// Timeout specifies the HTTP request timeout for registry requests.
	// When set to a positive value, overrides the default 15 second timeout.
	// Zero or negative values use the default timeout.
	// This is useful for slow networks or testing scenarios.
	//
	// Example: 30 * time.Second for slower networks
	Timeout time.Duration

	// OnProgress is called with progress updates during resolution.
	// This can be used for logging, progress bars, or debugging.
	//
	// The callback must be thread-safe as it may be called concurrently
	// from multiple goroutines during parallel module fetching.
	// The callback should return quickly to avoid blocking resolution.
	//
	// If nil, no progress events are emitted.
	OnProgress func(event ProgressEvent)

	// HTTPClient allows providing a custom HTTP client for registry requests.
	// Use this to configure authentication, custom TLS, proxies, or middleware.
	// If nil, a default client with connection pooling is used.
	//
	// Example with bearer token:
	//
	//	type bearerTransport struct {
	//	    token string
	//	    base  http.RoundTripper
	//	}
	//
	//	func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	//	    req = req.Clone(req.Context())
	//	    req.Header.Set("Authorization", "Bearer "+t.token)
	//	    return t.base.RoundTrip(req)
	//	}
	//
	//	client := &http.Client{
	//	    Transport: &bearerTransport{token: os.Getenv("TOKEN"), base: http.DefaultTransport},
	//	}
	//	result, _ := Resolve(ctx, content, ResolutionOptions{HTTPClient: client})
	//
	HTTPClient *http.Client

	// Cache provides external caching for MODULE.bazel file contents.
	//
	// When set, the resolver checks the cache before fetching from registries
	// and stores fetched content in the cache for future use. This enables:
	//   - Faster repeated resolutions (avoid redundant network requests)
	//   - Offline resolution after initial cache population
	//   - Shared caching across multiple processes or machines
	//
	// Cache errors are handled gracefully: if Get fails, the resolver fetches
	// from the registry; if Put fails, resolution continues without caching.
	// This ensures caching issues never break dependency resolution.
	//
	// If nil, no external caching is used. Note that in-memory caching within
	// a single resolution is always active regardless of this setting.
	//
	// Implementations must be safe for concurrent use from multiple goroutines.
	Cache ModuleCache

	// Logger is the structured logger for resolution diagnostics.
	// If nil, logging is disabled. Uses log/slog for backend flexibility.
	Logger *slog.Logger
}

// BazelToolsLookup resolves Bazel's implicit MODULE.tools dependencies for a Bazel version.
//
// The lookup receives the raw Bazel version string provided by the caller. Returning
// nil means "no implicit MODULE.tools dependencies" for that version.
type BazelToolsLookup func(version string) []bazeltools.ToolDep

// BazelToolsTransformer modifies the implicit MODULE.tools dependencies for a Bazel version.
//
// The transformer receives the raw Bazel version string plus a cloned dependency slice.
// Returning nil means "no implicit MODULE.tools dependencies" for that version.
type BazelToolsTransformer func(version string, deps []bazeltools.ToolDep) []bazeltools.ToolDep

// ModuleCache provides external caching for MODULE.bazel file contents.
//
// This interface enables persistent caching across resolutions. Common
// implementations include file-based caches, Redis, memcached, or any
// key-value store. The library does not provide a built-in implementation;
// users should implement this interface based on their infrastructure.
//
// # Thread Safety
//
// Implementations MUST be safe for concurrent use. Multiple goroutines
// will call Get and Put simultaneously during parallel module fetching.
//
// # Error Handling
//
// Cache operations should not fail resolution. The resolver treats cache
// errors as cache misses and continues with registry fetches. Implementations
// may log errors internally but should avoid returning errors for transient
// issues like network timeouts to cache backends.
//
// # Content Format
//
// The content parameter in Get/Put is the raw MODULE.bazel file content
// as fetched from the registry (UTF-8 text). Implementations should store
// this content verbatim without modification.
//
// # Example Implementation
//
// A minimal file-based cache:
//
//	type FileCache struct {
//	    dir string
//	}
//
//	func (c *FileCache) Get(ctx context.Context, name, version string) ([]byte, bool, error) {
//	    path := filepath.Join(c.dir, name, version, "MODULE.bazel")
//	    data, err := os.ReadFile(path)
//	    if os.IsNotExist(err) {
//	        return nil, false, nil // Cache miss, not an error
//	    }
//	    if err != nil {
//	        return nil, false, err
//	    }
//	    return data, true, nil
//	}
//
//	func (c *FileCache) Put(ctx context.Context, name, version string, content []byte) error {
//	    path := filepath.Join(c.dir, name, version, "MODULE.bazel")
//	    if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
//	        return err
//	    }
//	    return os.WriteFile(path, content, 0644)
//	}
type ModuleCache interface {
	// Get retrieves cached MODULE.bazel content for a module version.
	//
	// Returns:
	//   - (content, true, nil)  - Cache hit: content found
	//   - (nil, false, nil)     - Cache miss: content not in cache (normal condition)
	//   - (nil, false, err)     - Cache error: failed to access cache
	//
	// A cache miss (found=false) is a normal condition, not an error.
	// Implementations should only return errors for actual failures like
	// I/O errors or connection failures to cache backends.
	Get(ctx context.Context, name, version string) (content []byte, found bool, err error)

	// Put stores MODULE.bazel content for a module version.
	//
	// The operation should be idempotent: storing the same content multiple
	// times for the same module version should succeed without error.
	//
	// Implementations should handle the content as opaque bytes and store
	// it verbatim. The content is the raw MODULE.bazel file as fetched
	// from the registry.
	Put(ctx context.Context, name, version string, content []byte) error
}

// YankedVersionsError is returned when resolution selects yanked versions
// and YankedVersionError behavior is configured.
type YankedVersionsError struct {
	// Modules contains the yanked modules that were selected.
	Modules []ModuleToResolve
}

func (e *YankedVersionsError) Error() string {
	if len(e.Modules) == 1 {
		m := e.Modules[0]
		return "selected yanked version " + m.Name + "@" + m.Version + ": " + m.YankReason
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "selected %d yanked versions:", len(e.Modules))
	for _, m := range e.Modules {
		sb.WriteString("\n  - ")
		sb.WriteString(m.Name)
		sb.WriteByte('@')
		sb.WriteString(m.Version)
		sb.WriteString(": ")
		sb.WriteString(m.YankReason)
	}
	return sb.String()
}

// DirectDepMismatch represents a mismatch between declared and resolved versions.
type DirectDepMismatch struct {
	// Name is the module name.
	Name string
	// DeclaredVersion is the version declared in the root MODULE.bazel.
	DeclaredVersion string
	// ResolvedVersion is the version selected by resolution.
	ResolvedVersion string
}

// DirectDepsMismatchError is returned when direct dependencies don't match resolved versions
// and DirectDepsError mode is configured.
type DirectDepsMismatchError struct {
	// Mismatches contains the direct dependencies that don't match.
	Mismatches []DirectDepMismatch
}

func (e *DirectDepsMismatchError) Error() string {
	if len(e.Mismatches) == 1 {
		m := e.Mismatches[0]
		return fmt.Sprintf("direct dependency %s declared as %s but resolved to %s",
			m.Name, m.DeclaredVersion, m.ResolvedVersion)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d direct dependencies don't match resolved versions:", len(e.Mismatches))
	for _, m := range e.Mismatches {
		sb.WriteString("\n  - ")
		sb.WriteString(m.Name)
		sb.WriteString(": declared ")
		sb.WriteString(m.DeclaredVersion)
		sb.WriteString(", resolved ")
		sb.WriteString(m.ResolvedVersion)
	}
	return sb.String()
}


// BazelIncompatibilityError is returned when resolution selects modules that are
// incompatible with the specified Bazel version and BazelCompatibilityError mode is configured.
type BazelIncompatibilityError struct {
	// BazelVersion is the Bazel version being checked against.
	BazelVersion string
	// Modules contains the modules that are incompatible.
	Modules []ModuleToResolve
}

func (e *BazelIncompatibilityError) Error() string {
	if len(e.Modules) == 1 {
		m := e.Modules[0]
		return fmt.Sprintf("module %s@%s is incompatible with Bazel %s: %s",
			m.Name, m.Version, e.BazelVersion, m.BazelIncompatibilityReason)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d modules are incompatible with Bazel %s:", len(e.Modules), e.BazelVersion)
	for _, m := range e.Modules {
		sb.WriteString("\n  - ")
		sb.WriteString(m.Name)
		sb.WriteByte('@')
		sb.WriteString(m.Version)
		sb.WriteString(": ")
		sb.WriteString(m.BazelIncompatibilityReason)
	}
	return sb.String()
}
