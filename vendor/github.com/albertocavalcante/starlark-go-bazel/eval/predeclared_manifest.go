package eval

// ManifestEntry is one row of the predeclared-globals contract. The
// manifest is the executable form of plan 03's builtins-surface table
// (docs/plans/01-bazel-builtins-emulation/03-builtins-surface.md):
// every symbol that's reachable as a bare identifier in a .bzl file
// has an entry, and the entry's Status field records the level of
// fidelity the library claims (implemented / stubbed / missing).
//
// The drift-detection tests in predeclared_documented_test.go and
// predeclared_evals_test.go cross-check the manifest against the
// runtime predeclared dict and against a "universe" .bzl that
// exercises every implemented symbol. When M1 introduces
// Predeclared(Version), the AddedIn/RemovedIn fields gate per-version
// visibility; pre-M1 the version fields are advisory only.
type ManifestEntry struct {
	// Name is the bare identifier as it appears in .bzl source
	// (e.g. "rule", "aspect", "attr"). For sub-manifests (native.*,
	// attr.*) the name is dotted.
	Name string

	// Kind classifies the symbol for documentation only:
	//   "builtin"  - a callable returned by starlark.NewBuiltin
	//   "module"   - a HasAttrs container (attr, native, json, proto)
	//   "constant" - True / False / None
	Kind string

	// AddedIn / RemovedIn are advisory pre-M1; load-bearing once
	// Predeclared(Version) lands. RemovedIn=0 means "never removed".
	AddedIn   string
	RemovedIn string

	// Status records the level of fidelity:
	//   "implemented" - real semantics matching Bazel
	//   "stubbed"     - accepts the call but returns a placeholder
	//                   (e.g. native.* members that no-op)
	//   "missing"     - Bazel has it, we don't
	Status string

	// BazelDocsURL is the source-of-truth pointer for this symbol's
	// Bazel-side behavior. Used in test failure messages so an
	// engineer chasing a regression can find the spec quickly.
	BazelDocsURL string
}

// Status constants for ManifestEntry.Status.
const (
	StatusImplemented = "implemented"
	StatusStubbed     = "stubbed"
	StatusMissing     = "missing"
)

// Manifest is the top-level predeclared-globals contract. Order is
// stable: tests iterate this slice and reasonable failure ordering
// matters for diagnosing drift. Group by kind, then alphabetical.
var Manifest = []ManifestEntry{
	// ── Callables ───────────────────────────────────────────────────
	{Name: "Label", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#Label"},
	{Name: "aspect", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#aspect"},
	{Name: "depset", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#depset"},
	{Name: "module_extension", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#module_extension"},
	{Name: "provider", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#provider"},
	{Name: "repository_rule", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#repository_rule"},
	{Name: "rule", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#rule"},
	{Name: "struct", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/builtins/struct"},
	{Name: "tag_class", Kind: "builtin", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#tag_class"},

	// ── Modules ─────────────────────────────────────────────────────
	{Name: "attr", Kind: "module", AddedIn: "v7", Status: StatusImplemented,
		BazelDocsURL: "https://bazel.build/rules/lib/toplevel/attr"},
	{Name: "native", Kind: "module", AddedIn: "v7", Status: StatusStubbed,
		BazelDocsURL: "https://bazel.build/rules/lib/toplevel/native"},

	// ── Constants ───────────────────────────────────────────────────
	{Name: "True", Kind: "constant", AddedIn: "v7", Status: StatusImplemented},
	{Name: "False", Kind: "constant", AddedIn: "v7", Status: StatusImplemented},
	{Name: "None", Kind: "constant", AddedIn: "v7", Status: StatusImplemented},

	// ── Known gaps (Status=missing) ─────────────────────────────────
	// Bazel has these; the library does not. Documented here so plan
	// 03's surface table and runtime behavior stay cross-checkable;
	// TestPredeclared_MissingListIsFailingAtVLatest enforces that
	// missing entries don't silently get added later without flipping
	// Status=implemented.
	{Name: "exec_group", Kind: "builtin", AddedIn: "v7", Status: StatusMissing,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#exec_group"},
	{Name: "json", Kind: "module", AddedIn: "v7", Status: StatusMissing,
		BazelDocsURL: "https://bazel.build/rules/lib/toplevel/json"},
	{Name: "proto", Kind: "module", AddedIn: "v7", Status: StatusMissing,
		BazelDocsURL: "https://bazel.build/rules/lib/toplevel/proto"},
	{Name: "subrule", Kind: "builtin", AddedIn: "v8", Status: StatusMissing,
		BazelDocsURL: "https://bazel.build/rules/lib/globals/bzl#subrule"},
}

// UniverseSrc is the .bzl source the smoke test
// TestPredeclared_UniverseEvalsAtVLatest evaluates. It must mention
// every manifest entry whose Status is implemented;
// TestPredeclared_ManifestExercisedByUniverse enforces that.
//
// The source is exported so the smoke test can live in package
// eval_test (where it can import bzl without creating an import
// cycle) while the drift-detection test that reads the manifest
// stays in package eval.
const UniverseSrc = `
# Exercise every Status=implemented manifest entry.

_provider  = provider(fields = ["x"])
_rule      = rule(implementation = lambda ctx: None, attrs = {"src": attr.label()})
_aspect    = aspect(implementation = lambda target, ctx: None)
_repo_rule = repository_rule(implementation = lambda ctx: None, attrs = {"url": attr.string()})
_mod_ext   = module_extension(implementation = lambda mctx: None)
_tag       = tag_class(attrs = {"name": attr.string()})
_l         = Label("//foo:bar")
_d         = depset(["a", "b"])
_s         = struct(x = 1)

# Module references (held but not called — calling native.* would be
# legal only in BUILD context).
_attr_mod   = attr
_native_mod = native

# Constants
_t = True
_f = False
_n = None
`
