// Package bzl provides the main user-facing API for evaluating Bazel Starlark files.
package bzl

import (
	"go.starlark.net/starlark"

	"github.com/albertocavalcante/starlark-go-bazel/loader"
	"github.com/albertocavalcante/starlark-go-bazel/taint"
	"github.com/albertocavalcante/starlark-go-bazel/version"
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

	// SCAFFOLD: the per-Version delta table wires in M6 (see
	// docs/plans/02-pre-m1-cleanup-and-publish.md §1). Today the
	// field is read and stored — but every Version value behaves
	// identically (Latest()'s surface) because the table is empty.
	// Pin against this field knowing behavior is reserved.
	//
	// Version pins the Bazel LTS major whose default builtin surface
	// is emulated. Zero value is VLatest. Orthogonal to the per-flag
	// FeatureFlags map below.
	Version version.Version

	// SCAFFOLD: the loader-selection dispatch wires in M5. Today
	// Mode is read but ModeLenient and ModeAnalysis evaluate
	// identically to ModeStrict; the LenientLoad bool is the
	// effective control until M5. See effectiveMode().
	//
	// Mode selects strictness — see mode.go. Zero value is ModeStrict
	// which preserves prior default behavior.
	Mode Mode

	// SCAFFOLD: per-flag behavior dispatch wires in M6. Today the
	// map is accepted and stored but no flag has runtime effect.
	// Useful for forward-compat consumer pinning.
	//
	// FeatureFlags toggles individual Bazel experimental_*/incompatible_*
	// features, orthogonal to Version. Key = the flag's command-line
	// name without leading dashes (e.g. "experimental_repository_ctx_execute_wasm").
	// Unset keys fall back to the Version default; M6 populates the
	// per-Version default table.
	FeatureFlags map[string]bool

	// PredeclaredBzl adds or overrides predeclared globals for .bzl
	// evaluation. Merged on top of the Version-pinned default
	// universe. Use this to inject custom analysis-time builtins
	// (e.g., audit hooks).
	PredeclaredBzl starlark.StringDict

	// PredeclaredBuild adds or overrides predeclared globals for
	// BUILD file evaluation. Same merge semantics as PredeclaredBzl.
	PredeclaredBuild starlark.StringDict

	// SCAFFOLD: capture-population wires in M5 alongside Mode. Today
	// the pointer is accepted and stored but no eval path writes to
	// it. CaptureSinks remains the zero value across runs until M5
	// activates the capture path.
	//
	// CaptureSinks is the destination for analysis-mode capture
	// output. Must be non-nil when Mode == ModeAnalysis (once M5
	// wires the dispatch); ignored in ModeStrict / ModeLenient. The
	// Sinks type's field set grows across M4-M5.
	CaptureSinks *taint.Sinks

	// LoadResolver, when non-nil, REPLACES the default thread.Load
	// handler for .bzl + BUILD evaluation. Use stub.LoaderFor to
	// wire Permissive fallbacks for unresolvable external symbols;
	// canopy ingest composes a tryReal callback against its mirror.
	LoadResolver LoadFunc
}

// SCAFFOLD: the loader-selection switch that consumes this lives in
// M5 (see docs/plans/02-pre-m1-cleanup-and-publish.md §1 for the
// dead-vs-scaffold reflection). Today effectiveMode pins the
// auto-promotion contract — the unit tests
// (TestOptions_ZeroValueDefaults, TestOptions_LenientLoadAutoPromotes,
// TestOptions_ExplicitModeWins) capture what M5 will honor when it
// wires Mode into the loader dispatch.
//
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
