package bzl

// Mode selects the strictness of Bazel Starlark evaluation. The
// zero value is ModeStrict so existing callers (who pass Options{})
// see no behavior change.
//
// SCAFFOLD: M1 ships the surface; the loader-selection switch
// wires in M5. Until M5 lands, ModeLenient and ModeAnalysis
// evaluate identically to ModeStrict; the LenientLoad bool remains
// the effective control. Consumers can pin against the Mode
// constants today knowing the routing is reserved.
type Mode int

const (
	// ModeStrict matches Bazel's own semantics: unresolvable loads
	// error, unknown builtins error, no capture infrastructure.
	// Suitable for executing user code as Bazel would.
	ModeStrict Mode = iota

	// ModeLenient is reserved for stubbing unresolvable external
	// loads with permissive values (see stub.Permissive) so eval
	// proceeds even when loads from external modules can't be
	// resolved against the local workspace. M5 wires the dispatch;
	// today: evaluates as ModeStrict.
	ModeLenient

	// ModeAnalysis is reserved for the URL-extraction path:
	// composes ModeLenient with capture sinks active so
	// ctx.download / ctx.download_and_extract feed into
	// CaptureSinks.URLs and repository_rule instantiations are
	// recorded for replay. M5 wires the dispatch; today: evaluates
	// as ModeStrict.
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
