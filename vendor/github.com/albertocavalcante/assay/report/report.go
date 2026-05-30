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
	RepositoryRules  []RepoRuleSpec  `json:"repository_rules,omitempty"`
	ModuleExtensions []ModuleExtSpec `json:"module_extensions,omitempty"`

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
}

// ModuleKey identifies a module-version pair.
type ModuleKey struct {
	Name    string `json:"name"`
	Version string `json:"version"`
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
	Name      string   `json:"name"`
	Type      string   `json:"type,omitempty"` // "string", "label", "label_list", "int", "bool", etc.
	Doc       string   `json:"doc,omitempty"`
	Default   string   `json:"default,omitempty"` // textual literal; "" if not set or non-literal
	Mandatory bool     `json:"mandatory,omitempty"`
	Providers []string `json:"providers,omitempty"`
}

// AttrsExtractionMethod tags how a rule's attrs slice was produced.
// Lets consumers (UIs, audits) tell apart "we read this literally from a
// dict expression" from "we did module-local symbol folding" from
// "we interpreted the .bzl file." Empty string is treated as "literal"
// for backward compatibility with reports produced before this field
// existed.
type AttrsExtractionMethod string

const (
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
	Name              string     `json:"name"`
	Doc               string     `json:"doc,omitempty"`
	AttrAspects       []string   `json:"attr_aspects,omitempty"`
	RequiredProviders []string   `json:"required_providers,omitempty"`
	Private           bool       `json:"private,omitempty"`
	Provenance        Provenance `json:"provenance"`
}

// ToolchainSpec describes one toolchain_type() declaration.
type ToolchainSpec struct {
	Name       string     `json:"name"`
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
	Name       string     `json:"name"`
	Doc        string     `json:"doc,omitempty"`
	TagClasses []string   `json:"tag_classes,omitempty"`
	Private    bool       `json:"private,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// FileEntry records one source file in the module.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // "bzl" | "build" | "module" | "other"
}
