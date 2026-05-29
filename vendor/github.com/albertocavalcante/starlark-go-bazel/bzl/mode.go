package bzl

// Mode selects the strictness of Bazel Starlark evaluation. The
// zero value is ModeStrict so existing callers (who pass Options{})
// see no behavior change.
type Mode int

const (
	// ModeStrict matches Bazel's own semantics: unresolvable loads
	// error, unknown builtins error, no capture infrastructure.
	// Suitable for executing user code as Bazel would.
	ModeStrict Mode = iota

	// ModeLenient stubs unresolvable external loads with permissive
	// values (see stub.Permissive) so eval proceeds even when loads
	// from external modules can't be resolved against the local
	// workspace. Useful for analysis where you don't want to mirror
	// every transitive dependency.
	ModeLenient

	// ModeAnalysis composes ModeLenient with capture sinks active:
	// ctx.download / ctx.download_and_extract calls feed into
	// CaptureSinks.URLs; repository_rule instantiations are recorded
	// for replay; per-fork errors are collected rather than aborting
	// the whole eval. The URL-extraction path used by canopy airgap
	// and similar tools.
	ModeAnalysis
)

// String returns the mode name for diagnostics.
func (m Mode) String() string {
	switch m {
	case ModeStrict:
		return "strict"
	case ModeLenient:
		return "lenient"
	case ModeAnalysis:
		return "analysis"
	}
	return "unknown"
}
