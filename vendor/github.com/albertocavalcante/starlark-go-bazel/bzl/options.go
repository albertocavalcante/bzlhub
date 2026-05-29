// Package bzl provides the main user-facing API for evaluating Bazel Starlark files.
package bzl

import (
	"github.com/albertocavalcante/starlark-go-bazel/loader"
	"github.com/albertocavalcante/starlark-go-bazel/taint"
	"github.com/albertocavalcante/starlark-go-bazel/version"
	"go.starlark.net/starlark"
)

// LoadFunc is the canonical signature for a thread.Load handler.
// Matches starlark.Thread.Load. Exposed so consumers can name the
// type explicitly.
type LoadFunc = func(*starlark.Thread, string) (starlark.StringDict, error)

// Options configures the interpreter.
type Options struct {
	// WorkspaceRoot is the root of the Bazel workspace.
	WorkspaceRoot string

	// FileSystem for loading files (default: OS filesystem).
	FileSystem loader.FileSystem

	// ExternalRepos maps repository names to paths.
	ExternalRepos map[string]string

	// PrintHandler handles print() output.
	PrintHandler func(msg string)

	// LenientLoad makes load() calls for unresolvable external repos
	// (anything not in ExternalRepos) and for missing files return an
	// empty StringDict + nil error rather than aborting the eval.
	// Use this when extracting structural info (rule attrs, provider
	// fields, etc.) from .bzl files that pull from external Bazel
	// modules you don't want to materialize.
	//
	// Faithful execution mode (default) still errors on unresolvable
	// loads.
	//
	// Deprecated: use Mode = ModeLenient (or ModeAnalysis) instead.
	// When LenientLoad is true and Mode is zero, the interpreter
	// auto-promotes Mode to ModeLenient and preserves prior behavior.
	LenientLoad bool

	// Version pins the Bazel LTS major whose default builtin surface
	// is emulated. Zero value is VLatest. Orthogonal to the per-flag
	// FeatureFlags map below.
	Version version.Version

	// Mode selects strictness — see mode.go. Zero value is ModeStrict
	// which preserves prior default behavior.
	Mode Mode

	// FeatureFlags toggles individual Bazel experimental_*/incompatible_*
	// features, orthogonal to Version. Key = the flag's command-line
	// name without leading dashes (e.g. "experimental_repository_ctx_execute_wasm").
	// Unset keys fall back to the Version default; future M6 work
	// populates the per-Version default table.
	FeatureFlags map[string]bool

	// PredeclaredBzl adds or overrides predeclared globals for .bzl
	// evaluation. Merged on top of the Version-pinned default
	// universe. Use this to inject custom analysis-time builtins
	// (e.g., audit hooks).
	PredeclaredBzl starlark.StringDict

	// PredeclaredBuild adds or overrides predeclared globals for
	// BUILD file evaluation. Same merge semantics as PredeclaredBzl.
	PredeclaredBuild starlark.StringDict

	// CaptureSinks is the destination for analysis-mode capture
	// output. Must be non-nil when Mode == ModeAnalysis; ignored in
	// ModeStrict / ModeLenient. The Sinks type's field set grows
	// across M4-M5; the M1 surface accepts the pointer.
	CaptureSinks *taint.Sinks

	// LoadResolver, when non-nil, REPLACES the default thread.Load
	// handler for .bzl + BUILD evaluation. Use stub.LoaderFor to
	// wire Permissive fallbacks for unresolvable external symbols;
	// canopy ingest composes a tryReal callback against its mirror.
	LoadResolver func(*starlark.Thread, string) (starlark.StringDict, error)
}

// effectiveMode returns the Mode after applying the LenientLoad
// backward-compat promotion: if the legacy bool is true and Mode is
// zero (ModeStrict by iota), auto-promote to ModeLenient.
func (o Options) effectiveMode() Mode {
	if o.Mode != ModeStrict {
		return o.Mode
	}
	if o.LenientLoad {
		return ModeLenient
	}
	return ModeStrict
}
