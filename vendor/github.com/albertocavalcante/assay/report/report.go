// Package report defines the public data types produced by assay.
//
// A ModuleReport is the single output of analyzing a Bazel module. Every
// downstream consumer (canopy UI, MCP tools, CI checks, IDE plugins) is a
// projection of this struct.
package report

// ModuleReport is the complete structured description of a Bazel module
// produced by static analysis of its source tree.
type ModuleReport struct {
	Name               string   `json:"name"`
	Version            string   `json:"version,omitempty"`
	CompatibilityLevel int      `json:"compatibility_level,omitempty"`
	BazelCompatibility []string `json:"bazel_compatibility,omitempty"`

	BazelDeps []ModuleKey `json:"bazel_deps,omitempty"`

	Rules            []RuleSpec      `json:"rules,omitempty"`
	Providers        []ProviderSpec  `json:"providers,omitempty"`
	Macros           []MacroSpec     `json:"macros,omitempty"`
	Aspects          []AspectSpec    `json:"aspects,omitempty"`
	Toolchains       []ToolchainSpec `json:"toolchains,omitempty"`
	ToolchainImpls   []ToolchainImpl `json:"toolchain_impls,omitempty"`
	RepositoryRules  []RepoRuleSpec  `json:"repository_rules,omitempty"`
	ModuleExtensions []ModuleExtSpec `json:"module_extensions,omitempty"`

	// Overrides captures every archive_override / git_override /
	// single_version_override / multiple_version_override /
	// local_path_override at MODULE.bazel top level. Source order
	// preserved.
	Overrides []ModuleOverride `json:"overrides,omitempty"`

	// UsedExtensions captures each `use_extension(...)` call
	// assigned to a top-level Ident, plus the associated use_repo
	// imports and `<local>.<tag>(...)` invocations.
	UsedExtensions []ExtensionUse `json:"used_extensions,omitempty"`

	// RegisteredToolchains lists labels passed positionally to
	// `register_toolchains(...)` at MODULE.bazel top level. Verbatim,
	// source order, no deduplication.
	RegisteredToolchains []string `json:"registered_toolchains,omitempty"`

	// RegisteredExecutionPlatforms is the analogous list for
	// `register_execution_platforms(...)`.
	RegisteredExecutionPlatforms []string `json:"registered_execution_platforms,omitempty"`

	// Includes captures `include("//path:fragment.MODULE.bazel")`
	// statements (Bazel 7.2+). Verbatim labels in source order.
	// Only root modules and modules with non-registry overrides
	// can use include(), but assay records them regardless.
	Includes []string `json:"includes,omitempty"`

	Hermeticity HermeticityProfile `json:"hermeticity"`

	FileInventory []FileEntry `json:"file_inventory,omitempty"`

	// Assets surfaces "registry-page-grade" supporting docs and
	// directories sitting alongside the .bzl sources. Populated by
	// assay/assets during Analyze. Empty when none are present.
	Assets ModuleAssets `json:"assets"`
}

// ModuleAssets is the per-module bundle of human-facing supporting
// material: README, license, example directories. Surfacing these is
// what turns a registry page from "list of rules" into "place where
// you read about the module."
//
// All paths are workspace-relative (relative to the module's source
// root). Bytes are stored inline because (a) READMEs and licenses are
// typically <50KB, (b) the persistence layer already JSON-serializes
// the whole ModuleReport, and (c) keeping them inline avoids a second
// lookup when the UI renders the page. If a future BCR module ships
// a 5MB README, that's a useful problem to have and we can move to
// blob storage then.
type ModuleAssets struct {
	// Readme is the verbatim text of the module's README at root.
	// Order of preference: README.md, README.rst, README.txt, README.
	// Empty when no README file is present at the module root.
	Readme string `json:"readme,omitempty"`

	// ReadmePath is the relative path of the README file we picked
	// (e.g. "README.md"). Lets the UI link to the raw source via
	// code-nav. Empty iff Readme is empty.
	ReadmePath string `json:"readme_path,omitempty"`

	// License is the verbatim text of the LICENSE file at module root.
	// Truncated bytes — see assay/assets for the cap.
	License string `json:"license,omitempty"`

	// LicensePath is the relative path of the LICENSE file ("LICENSE",
	// "LICENSE.txt", "COPYING", etc.). Empty iff License is empty.
	LicensePath string `json:"license_path,omitempty"`

	// LicenseName is an SPDX-shaped name when the LICENSE header
	// matched a known pattern (e.g. "Apache-2.0", "MIT",
	// "BSD-3-Clause"). Empty when no known header matched — caller
	// can still link to the LICENSE file via LicensePath.
	//
	// EPISTEMIC STATUS — HEURISTIC. Narrow substring match against
	// the first 2KB of the file; not a full SPDX classifier. When
	// in doubt the detector returns "" rather than guessing. Treat
	// non-empty values as "header keywords matched X" rather than
	// "this file is X-licensed."
	LicenseName string `json:"license_name,omitempty"`

	// ExampleDirs lists relative paths to directories conventionally
	// holding usage examples — "example", "examples", "examples_*"
	// at any depth up to a sensible cap. Empty list, not nil, when
	// none are present.
	ExampleDirs []string `json:"example_dirs,omitempty"`

	// Changelog is the verbatim text of the module's changelog at
	// root. Order of preference: CHANGELOG.md, CHANGELOG.markdown,
	// CHANGELOG.rst, CHANGELOG.txt, CHANGELOG, CHANGES.md, CHANGES,
	// HISTORY.md, HISTORY. Truncated to 256KB.
	Changelog string `json:"changelog,omitempty"`

	// ChangelogPath is the relative path of the changelog file we
	// picked. Empty iff Changelog is empty.
	ChangelogPath string `json:"changelog_path,omitempty"`

	// HasCI is true when at least one provider's workflow directory
	// at the module root contains a non-hidden file. Pure
	// filesystem signal; doesn't validate the YAML or confirm the
	// workflows actually run.
	HasCI bool `json:"has_ci,omitempty"`

	// CIProviders lists the providers detected, alphabetized at
	// emit time. Recognized values: "bazelci", "forgejo", "github".
	// Empty when HasCI is false.
	CIProviders []string `json:"ci_providers,omitempty"`
}

// ModuleKey identifies a module-version pair, optionally carrying the
// dev-dependency flag and a repo_name alias when projected from a
// `bazel_dep(...)` call.
type ModuleKey struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	DevDependency bool   `json:"dev_dependency,omitempty"`
	RepoName      string `json:"repo_name,omitempty"`
}

// ModuleOverride captures one MODULE.bazel-top-level override statement
// (archive_override, git_override, single_version_override,
// multiple_version_override, local_path_override). Kind is the
// discriminator; the remaining fields are populated only when relevant
// to that Kind, so consumers can switch on Kind and ignore the rest.
type ModuleOverride struct {
	// Kind names the specific override call:
	// "archive" / "git" / "single_version" / "multiple_version" /
	// "local_path".
	Kind string `json:"kind"`

	// ModuleName is the target module being overridden (the
	// `module_name = "..."` kwarg).
	ModuleName string `json:"module_name,omitempty"`

	// URLs is populated for archive_override (multi-URL list) and
	// git_override (the single `remote = "..."` wrapped in a slice for
	// shape consistency).
	URLs []string `json:"urls,omitempty"`

	// Integrity is the pinned hash for archive_override.
	Integrity string `json:"integrity,omitempty"`

	// Commit pins a git_override to a specific revision SHA.
	Commit string `json:"commit,omitempty"`

	// Path is the local_path_override's target directory.
	Path string `json:"path,omitempty"`

	// Version is single_version_override's pinned version.
	Version string `json:"version,omitempty"`

	// Versions is multiple_version_override's allowed set.
	Versions []string `json:"versions,omitempty"`

	// Patches is the list of patch-file paths (any override that
	// accepts patches).
	Patches []string `json:"patches,omitempty"`

	// PatchStrip is the `-p<N>` strip level for `patches`.
	PatchStrip int `json:"patch_strip,omitempty"`

	// PatchCmds is the list of shell commands run after applying
	// patches (any override that accepts patch_cmds).
	PatchCmds []string `json:"patch_cmds,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// ExtensionUse captures one `use_extension(...)` assignment and the
// `use_repo(...)` + `<local>.<tag>(...)` calls associated with it.
//
// Each top-level `<local> = use_extension(...)` produces one
// ExtensionUse; subsequent `use_repo(<local>, ...)` and
// `<local>.<tag>(...)` statements are merged into that entry.
type ExtensionUse struct {
	// LocalName is the LHS Ident the use_extension was assigned to
	// (e.g., `python = use_extension(...)` -> "python").
	LocalName string `json:"local_name"`

	// BzlFile is use_extension's first positional argument (the
	// .bzl file path verbatim).
	BzlFile string `json:"bzl_file"`

	// ExtensionName is use_extension's second positional argument
	// (the exported symbol name in BzlFile).
	ExtensionName string `json:"extension_name"`

	// DevDependency captures use_extension's `dev_dependency = True`
	// kwarg.
	DevDependency bool `json:"dev_dependency,omitempty"`

	// ImportedRepos lists every repo name passed positionally to
	// `use_repo(<local>, ...)` calls referencing this LocalName.
	ImportedRepos []string `json:"imported_repos,omitempty"`

	// RenamedRepos captures the `<alias> = "<remote_repo>"` kwarg
	// form of use_repo. Key is the local alias the importing module
	// uses; value is the upstream repo name. Empty when no kwargs
	// were used.
	RenamedRepos map[string]string `json:"renamed_repos,omitempty"`

	// TagInvocations records `<local>.<tag>(...)` calls in source
	// order.
	TagInvocations []ExtensionTagInvocation `json:"tag_invocations,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// ExtensionTagInvocation captures one `<local>.<tag>(...)` call
// alongside a use_extension. Kwarg values are split by Starlark type
// so canopy can render them with type-aware UI (badges for bools,
// chips for lists, etc.) without re-parsing strings.
type ExtensionTagInvocation struct {
	TagName string `json:"tag_name"`

	// Kwargs holds string-literal-valued kwargs.
	Kwargs map[string]string `json:"kwargs,omitempty"`

	// KwargLists holds list-of-string-literal-valued kwargs.
	KwargLists map[string][]string `json:"kwarg_lists,omitempty"`

	// KwargBools holds bool-literal-valued kwargs. Distinct from
	// "absent" so canopy can show a True/False badge.
	KwargBools map[string]bool `json:"kwarg_bools,omitempty"`

	// KwargInts holds int-literal-valued kwargs (rare in extension
	// tags but legal).
	KwargInts map[string]int64 `json:"kwarg_ints,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// Provenance records where in the source a finding originated.
type Provenance struct {
	File     string `json:"file"`
	StartCol int    `json:"start_col,omitempty"`
	StartRow int    `json:"start_row,omitempty"`
	EndCol   int    `json:"end_col,omitempty"`
	EndRow   int    `json:"end_row,omitempty"`
}

// AttrSpec describes one attribute of a rule or aspect.
type AttrSpec struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"` // "string", "label", "label_list", "int", "bool", etc.
	Doc       string `json:"doc,omitempty"`
	Default   string `json:"default,omitempty"` // textual literal; "" if not set or non-literal
	Mandatory bool   `json:"mandatory,omitempty"`

	// ProviderGroups encodes the disjunction-of-conjunctions shape
	// of `attr.label(providers = ...)` / `attr.label_list(providers
	// = ...)`. Each outer slice is an OR alternative; each inner
	// slice is the set of providers ALL of which must be present
	// to satisfy that alternative.
	//
	// Examples:
	//   providers = [GoInfo]        -> [[GoInfo]]
	//   providers = [A, B, C]       -> [[A, B, C]]            (conjunction)
	//   providers = [[A], [B, C]]   -> [[A], [B, C]]          (disjunction)
	//
	// Empty / nil when the attr declaration doesn't pass providers
	// or when the value isn't a statically-resolvable list.
	ProviderGroups [][]string `json:"provider_groups,omitempty"`
}

// AttrsExtractionMethod tags how a rule's attrs slice was produced.
// Lets consumers (UIs, audits) tell apart "we read this literally from a
// dict expression" from "we did module-local symbol folding" from
// "we interpreted the .bzl file." Empty string is treated as "literal"
// for backward compatibility with reports produced before this field
// existed.
type AttrsExtractionMethod string

const (
	// AttrsUnresolved is the zero value: no tier (literal, symbol-fold,
	// load-resolve, interpreted) was able to recover the attrs slice.
	// Consumers should treat the attrs as missing rather than empty.
	AttrsUnresolved AttrsExtractionMethod = ""

	// AttrsLiteral — attrs were read as a literal `attrs = {string-key:
	// attr.TYPE(...)}` DictExpr. Zero analysis, zero ambiguity.
	AttrsLiteral AttrsExtractionMethod = "literal"

	// AttrsSymbolFold — attrs expression involved binary union (`X | Y`)
	// or `dict(X, ...)` calls, and every operand resolved to a literal
	// dict via module-local symbol binding. Still pure AST work, still
	// deterministic — refuses on ambiguity rather than guessing.
	AttrsSymbolFold AttrsExtractionMethod = "symbol_fold"

	// AttrsLoadResolve — attrs expression required following `load()`
	// across files to resolve a referenced constant. Bounded depth,
	// cycle-detected. Still structural, not interpreted.
	AttrsLoadResolve AttrsExtractionMethod = "load_resolve"

	// AttrsInterpreted — attrs were obtained by actually executing the
	// .bzl file in a sandboxed Starlark interpreter and introspecting
	// the resulting RuleClass. Authoritative, but heaviest path.
	AttrsInterpreted AttrsExtractionMethod = "interpreted"
)

// RuleSpec describes one rule() definition.
type RuleSpec struct {
	Name                  string                `json:"name"` // symbol name in the .bzl file
	Doc                   string                `json:"doc,omitempty"`
	Attrs                 []AttrSpec            `json:"attrs,omitempty"`
	AttrsExtractionMethod AttrsExtractionMethod `json:"attrs_extraction_method,omitempty"`
	Executable            bool                  `json:"executable,omitempty"`
	Test                  bool                  `json:"test,omitempty"`
	Private               bool                  `json:"private,omitempty"` // underscore-prefixed symbol name
	Provenance            Provenance            `json:"provenance"`
}

// ProviderSpec describes one provider() definition.
type ProviderSpec struct {
	Name       string     `json:"name"`
	Fields     []string   `json:"fields,omitempty"`
	Doc        string     `json:"doc,omitempty"`
	Private    bool       `json:"private,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// MacroSpec describes a top-level def function classified as a macro.
//
// Epistemic status: heuristic. There is no AST-direct signal for
// "this def is a Bazel macro." The classifier requires the name to
// be exported (no leading underscore), the first parameter to not be
// "ctx" (rule/aspect impl convention), the file path to not be under
// a test/example/vendor segment, and the body to invoke a
// rule-instantiating symbol — directly (native.X / loaded name /
// same-file rule binding) or transitively (another same-file
// def-macro identified by the fixpoint pass).
//
// Known weaknesses (see bzlwalk/macros.go and
// docs/macro-detection-plan.md for detail):
//
//   - A utility helper that happens to call a load()-imported function
//     not actually a rule may false-positive.
//   - A public macro that only composes private (_-prefixed) defs is
//     missed; private defs aren't candidates.
//
// Consumers should treat MacroSpec entries as "exported defs likely
// to be macros," not "macros, period."
type MacroSpec struct {
	Name       string     `json:"name"`
	Doc        string     `json:"doc,omitempty"`
	Params     []string   `json:"params,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// AspectSpec describes one aspect() definition.
type AspectSpec struct {
	Name              string   `json:"name"`
	Doc               string   `json:"doc,omitempty"`
	AttrAspects       []string `json:"attr_aspects,omitempty"`
	RequiredProviders []string `json:"required_providers,omitempty"`

	// Attrs follow the same shape and extraction-tier ladder as
	// RuleSpec.Attrs (literal / symbol_fold / load_resolve /
	// interpreted). Per-aspect AttrsExtractionMethod records
	// which tier resolved them, mirroring RuleSpec's field.
	Attrs                 []AttrSpec            `json:"attrs,omitempty"`
	AttrsExtractionMethod AttrsExtractionMethod `json:"attrs_extraction_method,omitempty"`

	// Provides lists the provider names the aspect produces, taken
	// verbatim from `provides = [...]`. Provider symbols are
	// Idents in source; non-Ident entries are dropped.
	Provides []string `json:"provides,omitempty"`

	// Fragments lists configuration fragments the aspect declares
	// a dependency on (`fragments = ["cpp", "py"]`).
	Fragments []string `json:"fragments,omitempty"`

	// HostFragments mirrors Fragments for the host configuration.
	// Rare but Bazel-supported.
	HostFragments []string `json:"host_fragments,omitempty"`

	// Toolchains lists toolchain_type labels the aspect declares a
	// dependency on, verbatim from `toolchains = [...]`.
	Toolchains []string `json:"toolchains,omitempty"`

	// ApplyToGeneratingRules captures the `apply_to_generating_rules`
	// kwarg — when true the aspect runs on rules that generate
	// output files rather than the output files themselves.
	ApplyToGeneratingRules bool `json:"apply_to_generating_rules,omitempty"`

	Private    bool       `json:"private,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// ToolchainSpec describes one toolchain_type() declaration.
type ToolchainSpec struct {
	Name       string     `json:"name"`
	Provenance Provenance `json:"provenance"`
}

// ToolchainImpl describes one `toolchain(...)` registration in a BUILD
// file — the concrete pairing of a toolchain_type with an
// implementation target plus platform/setting constraints. Distinct
// from ToolchainSpec (the toolchain_type declaration) and from
// ModuleReport.RegisteredToolchains (MODULE.bazel's
// register_toolchains(...) labels).
type ToolchainImpl struct {
	// Name is the local target name (`name = "..."`).
	Name string `json:"name"`

	// ToolchainType is the typed-target reference
	// (`toolchain_type = ":x"`). Verbatim — canopy resolves the
	// label.
	ToolchainType string `json:"toolchain_type"`

	// ToolchainImpl is the concrete implementation target
	// (`toolchain = ":impl"`). Verbatim. May be empty when the
	// registration only sets constraints without a target body —
	// rare but legal.
	ToolchainImpl string `json:"toolchain_impl,omitempty"`

	// ExecCompatibleWith / TargetCompatibleWith list platform
	// constraint labels. Verbatim, string-literal entries only.
	ExecCompatibleWith   []string `json:"exec_compatible_with,omitempty"`
	TargetCompatibleWith []string `json:"target_compatible_with,omitempty"`

	// TargetSettings lists config_setting labels constraining when
	// this toolchain matches.
	TargetSettings []string `json:"target_settings,omitempty"`

	Provenance Provenance `json:"provenance"`
}

// RepoRuleSpec describes one repository_rule() definition.
type RepoRuleSpec struct {
	Name                  string                `json:"name"`
	Doc                   string                `json:"doc,omitempty"`
	Attrs                 []AttrSpec            `json:"attrs,omitempty"`
	AttrsExtractionMethod AttrsExtractionMethod `json:"attrs_extraction_method,omitempty"`
	Local                 bool                  `json:"local,omitempty"`
	Private               bool                  `json:"private,omitempty"`
	Provenance            Provenance            `json:"provenance"`
}

// ModuleExtSpec describes one module_extension() definition.
type ModuleExtSpec struct {
	Name       string         `json:"name"`
	Doc        string         `json:"doc,omitempty"`
	TagClasses []TagClassSpec `json:"tag_classes,omitempty"`
	Private    bool           `json:"private,omitempty"`
	Provenance Provenance     `json:"provenance"`
}

// TagClassSpec describes one tag_class() inside a module_extension's
// tag_classes dict — the registry-surface payload for what
// `use_extension(...).<name>(...)` invocations accept.
//
// Attrs follow the same extraction tier ladder as RuleSpec.Attrs
// (literal / symbol_fold / load_resolve / interpreted); the
// per-tag-class AttrsExtractionMethod records which tier resolved
// them, mirroring RuleSpec's field.
type TagClassSpec struct {
	// Name is the tag-class name as it appears in the extension's
	// tag_classes dict — what users type after the dot in
	// `use_extension(...).<name>(...)`.
	Name string `json:"name"`

	// Doc is the tag_class's `doc = "..."` kwarg, if present.
	Doc string `json:"doc,omitempty"`

	// Attrs follow the same shape as RuleSpec.Attrs.
	Attrs []AttrSpec `json:"attrs,omitempty"`

	// AttrsExtractionMethod tags how the attrs slice was resolved.
	AttrsExtractionMethod AttrsExtractionMethod `json:"attrs_extraction_method,omitempty"`

	// Provenance points to the tag_class(...) call site, not the
	// dict entry that referenced it.
	Provenance Provenance `json:"provenance"`
}

// FileEntry records one source file in the module.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // "bzl" | "build" | "module" | "other"
}
