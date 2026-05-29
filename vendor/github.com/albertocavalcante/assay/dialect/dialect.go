// Package dialect defines the dialect abstraction so assay can be extended
// from Bazel to Buck2 (or other Starlark-based build systems) without
// rewriting the introspection layer.
//
// Bazel is the only dialect shipped today, via [Bazel]. A Buck2 dialect is
// a planned second implementation gated on adoption signal — see
// docs/roadmap.md (Phase C2) in the repository root.
package dialect

// Dialect describes the symbol vocabulary of a Starlark-based build system.
// The .bzl walker uses these symbol sets to recognize and classify calls.
type Dialect interface {
	// Name is the dialect identifier: "bazel" or "buck2".
	Name() string

	// IsRuleSymbol reports whether the given identifier names this dialect's
	// rule-definition primitive (e.g., "rule" for Bazel).
	IsRuleSymbol(sym string) bool

	// IsProviderSymbol reports whether the identifier names the
	// provider-definition primitive.
	IsProviderSymbol(sym string) bool

	// IsAspectSymbol reports whether the identifier names the aspect-definition primitive.
	IsAspectSymbol(sym string) bool

	// IsRepositoryRuleSymbol reports whether the identifier names the
	// repository_rule (or equivalent) primitive.
	IsRepositoryRuleSymbol(sym string) bool

	// IsModuleExtensionSymbol reports whether the identifier names the
	// module_extension primitive. May be empty for dialects without one.
	IsModuleExtensionSymbol(sym string) bool

	// IsToolchainTypeSymbol reports whether the identifier names the
	// toolchain_type primitive.
	IsToolchainTypeSymbol(sym string) bool

	// IsNetworkFetchAPI reports whether the identifier names an API that
	// fetches from the network (download_file, http_archive, etc.).
	IsNetworkFetchAPI(sym string) bool

	// IsSystemExecAPI reports whether the identifier names an API that
	// executes system tools or runs arbitrary commands (ctx.execute,
	// repo_ctx.execute, etc.).
	IsSystemExecAPI(sym string) bool

	// IsCompilationRuleSymbol reports whether the identifier names a
	// rule that compiles source at consumer build time — go_binary,
	// cc_library, java_binary, kt_jvm_binary, etc. When a module's
	// own BUILD files invoke one of these outside test/example/vendor
	// paths, the module ships source that gets compiled (rather than
	// just wrapping a downloaded binary); the hermeticity classifier
	// emits BuildFromSource for those calls.
	IsCompilationRuleSymbol(sym string) bool

	// AttrModuleSymbols returns the prefix(es) used for attribute constructors
	// (e.g., "attr" — as in attr.string(), attr.label_list()).
	AttrModuleSymbols() []string
}
