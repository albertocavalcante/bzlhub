package api

// CompatCheckOptions configures a CompatCheck call. Mirrors the
// compat package's internal Options struct so the cross-transport
// contract stays decoupled from internal/compat's exact shape.
type CompatCheckOptions struct {
	// IncludeDevDependencies, when true, surfaces dev_dependency = True
	// bazel_deps in the result. Default false matches the "is my prod
	// build going to break?" framing.
	IncludeDevDependencies bool
}
