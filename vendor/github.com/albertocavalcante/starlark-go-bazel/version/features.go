package version

// Feature names a Bazel semantic capability that's gated on Version.
// Names mirror the upstream bazel_features module's dotted paths
// (github.com/bazel-contrib/bazel_features) where applicable, so a
// synthetic @bazel_features//:features.bzl built from this table is
// behavior-compatible for the surfaces we model.
//
// Sources: each row in HasFeature below cites the Bazel CHANGELOG.md
// release or git commit that introduced the capability — verified
// against the unshallowed Bazel checkout at ~/dev/refs/bazel.
type Feature string

const (
	// Bazel 7+ features.

	// FeatureBzlmodDefault: Bzlmod enabled by default.
	// CHANGELOG: Bazel 7.0.0 (issue #18958).
	FeatureBzlmodDefault Feature = "external_deps.bzlmod_default_on"

	// FeatureUseRepoRule: the use_repo_rule directive in MODULE.bazel.
	// CHANGELOG: Bazel 7.0.0.
	FeatureUseRepoRule Feature = "external_deps.use_repo_rule_directive"

	// FeatureModExtOsArchDependent: module_extension(os_dependent=,
	// arch_dependent=) kwargs.
	// git: c1165a9943 (2023-08-29), first release Bazel 7.0.0.
	FeatureModExtOsArchDependent Feature = "external_deps.module_extension_has_os_arch_dependent"

	// FeatureNativeRepoName: native.repo_name() function.
	// git: 73ed74ec5d (2022-09-05), first release Bazel 7.0.0.
	FeatureNativeRepoName Feature = "native.repo_name"

	// FeatureExtensionMetadataFunc: module_ctx.extension_metadata()
	// function exists at all. Landed in Bazel 6.2.0 (PR #18174) but
	// we report it true for all our supported (>=7) Versions.
	FeatureExtensionMetadataFunc Feature = "external_deps.module_extension_metadata_function"

	// Bazel 8+ features.

	// FeatureCtxGetenv: repository_ctx.getenv / module_ctx.getenv.
	// git: c230e39fb2 (2024-01-18), first release Bazel 8.0.0.
	// Paired with the deprecation of repository_rule(environ=) /
	// module_extension(environ=).
	FeatureCtxGetenv Feature = "external_deps.repository_ctx_getenv"

	// FeatureCtxWatch: ctx.watch / ctx.watch_tree.
	// git: a5376aa3e1 (2024-02-14), first release Bazel 8.0.0.
	FeatureCtxWatch Feature = "external_deps.repository_ctx_watch"

	// FeatureSymbolicMacros: the macro() builtin for symbolic macros.
	// CHANGELOG: Bazel 8.0.0.
	FeatureSymbolicMacros Feature = "rules.symbolic_macros"

	// Bazel 9+ features.

	// FeatureRepoMetadata: repository_ctx.repo_metadata(reproducible,
	// attrs_for_reproducibility).
	// CHANGELOG: Bazel 9.0.0 (9.0.0-pre.20250516.1).
	FeatureRepoMetadata Feature = "repo_rule.repo_metadata_function"

	// FeatureModExtMetadataFacts: module_ctx.extension_metadata(facts=)
	// + module_ctx.facts attribute. The "facts" mechanism for
	// cross-eval extension state.
	// CHANGELOG: Bazel 9.0.0 (9.0.0-pre.20251022.1).
	FeatureModExtMetadataFacts Feature = "external_deps.module_extension_metadata_facts"
)

// HasFeature reports whether the given feature is available at the
// given Bazel LTS. v is Resolved() first so VLatest works.
func (v Version) HasFeature(f Feature) bool {
	v = v.Resolved()
	switch f {
	case FeatureBzlmodDefault, FeatureUseRepoRule, FeatureModExtOsArchDependent,
		FeatureNativeRepoName, FeatureExtensionMetadataFunc:
		return v >= V7
	case FeatureCtxGetenv, FeatureCtxWatch, FeatureSymbolicMacros:
		return v >= V8
	case FeatureRepoMetadata, FeatureModExtMetadataFacts:
		return v >= V9
	}
	return false
}

// AllFeatures returns every Feature constant, useful for sanity
// loops + the bazel_features synthetic builder.
func AllFeatures() []Feature {
	return []Feature{
		FeatureBzlmodDefault,
		FeatureUseRepoRule,
		FeatureModExtOsArchDependent,
		FeatureNativeRepoName,
		FeatureExtensionMetadataFunc,
		FeatureCtxGetenv,
		FeatureCtxWatch,
		FeatureSymbolicMacros,
		FeatureRepoMetadata,
		FeatureModExtMetadataFacts,
	}
}

// ExperimentalFlag is a Bazel `--experimental_*` or `--incompatible_*`
// command-line flag name (without leading dashes). Orthogonal to
// Version — any version can have any flag on or off.
//
// The default-on state at a given Version is queried via
// DefaultAt(); consumers can override via bzl.Options.FeatureFlags.
type ExperimentalFlag string

const (
	// FlagRepoCtxExecuteWasm gates ctx.execute_wasm + ctx.load_wasm.
	// As of Bazel master, still experimental (default off).
	FlagRepoCtxExecuteWasm ExperimentalFlag = "experimental_repository_ctx_execute_wasm"

	// FlagRepoRemoteExec gates repository_rule(remotable=). Still
	// experimental as of Bazel master.
	FlagRepoRemoteExec ExperimentalFlag = "experimental_repo_remote_exec"

	// FlagIsolatedExtensionUse gates module_ctx.is_isolated and
	// isolated extension usages. Still experimental.
	FlagIsolatedExtensionUse ExperimentalFlag = "experimental_isolated_extension_usages"

	// FlagNoImplicitWatchLabel: --incompatible flag, defaults flip
	// in a future LTS.
	FlagNoImplicitWatchLabel ExperimentalFlag = "incompatible_no_implicit_watch_label"

	// FlagEnableDeprecatedLabelAPIs: --incompatible flag controlling
	// access to deprecated native.repository_name, Label.workspace_name,
	// Label.relative.
	FlagEnableDeprecatedLabelAPIs ExperimentalFlag = "incompatible_enable_deprecated_label_apis"
)

// DefaultAt returns the default value of an experimental/incompatible
// flag at the given Version. Consumers can override per-build via
// bzl.Options.FeatureFlags. As of the curation date, all experimental
// flags below default to off at every supported Version.
func (f ExperimentalFlag) DefaultAt(v Version) bool {
	switch f {
	case FlagRepoCtxExecuteWasm, FlagRepoRemoteExec, FlagIsolatedExtensionUse,
		FlagNoImplicitWatchLabel, FlagEnableDeprecatedLabelAPIs:
		return false
	}
	return false
}
