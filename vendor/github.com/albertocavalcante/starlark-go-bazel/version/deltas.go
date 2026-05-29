package version

// Deltas records the per-Version behavior toggles that load-bearing
// code branches on. Keep this consistent with HasFeature; Deltas is
// the imperative-shape view (boolean fields directly accessible) and
// HasFeature is the table-lookup-shape view (string-keyed).
//
// Both views resolve VLatest before answering.
type Deltas struct {
	// AllowWORKSPACE is true on Versions that still evaluate
	// WORKSPACE files. Bazel 9 ships with --enable_workspace defaulting
	// to false; we model that as AllowWORKSPACE=false at V9.
	AllowWORKSPACE bool

	// BzlmodDefault is true when Bzlmod is the default loader (i.e.,
	// MODULE.bazel-rooted resolution).
	BzlmodDefault bool

	// CtxHasGetenv: repository_ctx.getenv / module_ctx.getenv exists.
	CtxHasGetenv bool

	// CtxHasWatch: ctx.watch / ctx.watch_tree exists.
	CtxHasWatch bool

	// CtxHasRepoMetadata: repository_ctx.repo_metadata() exists.
	CtxHasRepoMetadata bool

	// ModuleExtensionTakesOsArch: module_extension() accepts
	// os_dependent= / arch_dependent= kwargs.
	ModuleExtensionTakesOsArch bool

	// ModuleCtxHasFacts: module_ctx.extension_metadata(facts=) +
	// module_ctx.facts exist.
	ModuleCtxHasFacts bool

	// SymbolicMacros: the macro() builtin exists.
	SymbolicMacros bool

	// EnvironKwargDeprecated: repository_rule(environ=) /
	// module_extension(environ=) emit a deprecation hint. True from
	// the Version that introduced getenv onwards (since getenv is
	// the migration target).
	EnvironKwargDeprecated bool

	// LabelRelativeDeprecated: Label.relative() is deprecated. (The
	// hard-removal version is V9 but the deprecation is older.)
	LabelRelativeDeprecated bool

	// NativeRepoName: native.repo_name() function exists.
	NativeRepoName bool
}

// AsDeltas projects the version's HasFeature answers into an
// imperative Deltas record. Useful for code that wants to branch on
// many features in one switch.
func (v Version) AsDeltas() Deltas {
	v = v.Resolved()
	return Deltas{
		AllowWORKSPACE:             v <= V8,
		BzlmodDefault:              v.HasFeature(FeatureBzlmodDefault),
		CtxHasGetenv:               v.HasFeature(FeatureCtxGetenv),
		CtxHasWatch:                v.HasFeature(FeatureCtxWatch),
		CtxHasRepoMetadata:         v.HasFeature(FeatureRepoMetadata),
		ModuleExtensionTakesOsArch: v.HasFeature(FeatureModExtOsArchDependent),
		ModuleCtxHasFacts:          v.HasFeature(FeatureModExtMetadataFacts),
		SymbolicMacros:             v.HasFeature(FeatureSymbolicMacros),
		EnvironKwargDeprecated:     v.HasFeature(FeatureCtxGetenv),
		LabelRelativeDeprecated:    v >= V8,
		NativeRepoName:             v.HasFeature(FeatureNativeRepoName),
	}
}
