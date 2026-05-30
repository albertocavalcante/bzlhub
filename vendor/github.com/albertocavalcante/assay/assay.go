// Package assay analyzes a Bazel module's source tree statically and produces
// a structured [report.ModuleReport] describing its rules, providers, macros,
// repository rules, module extensions, and hermeticity profile.
//
// # Quick start
//
// See [ExampleAnalyze] for the canonical invocation. The CLI in
// cmd/assay wraps the same entry point.
//
// # Tier ladder
//
// Attrs extraction runs through up to three tiers, each tagged in the
// emitted [report.AttrSpec] slice's [report.AttrsExtractionMethod] so
// consumers can reason about confidence:
//
//   - Tier 0 — literal dict, exact AST shape (AttrsLiteral).
//   - Tier 1 — same-file symbol fold over `BASE | {...}` and `dict(...)` (AttrsSymbolFold).
//   - Tier 2 — cross-file load() resolution (AttrsLoadResolve).
//   - Tier 3 — sandboxed Starlark interpreter; opt-in via [WithInterpreterFallback] (AttrsInterpreted).
//
// # Determinism
//
// Analyze is byte-identical-output deterministic for byte-identical
// input. See docs/epistemic-status.md for the per-field order sources
// and the contributor rules that keep it that way.
//
// # Documentation map
//
//   - docs/validation.md — current correctness audit against real rulesets.
//   - docs/epistemic-status.md — heuristic vs deterministic per output field.
//   - docs/roadmap.md — forward-looking work; phase status.
//   - docs/benchmarks.md — perf baseline + benchstat workflow.
package assay

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/albertocavalcante/assay/assets"
	"github.com/albertocavalcante/assay/bzlwalk"
	"github.com/albertocavalcante/assay/dialect"
	"github.com/albertocavalcante/assay/hermetic"
	"github.com/albertocavalcante/assay/internal/walkparse"
	"github.com/albertocavalcante/assay/interp"
	"github.com/albertocavalcante/assay/modulefile"
	"github.com/albertocavalcante/assay/report"
)

// Options configures Analyze.
type Options struct {
	// Dialect selects the build system Starlark dialect. Defaults to Bazel.
	Dialect dialect.Dialect

	// InterpreterFallback enables the Tier-3 path: for any rule whose
	// attrs Tier 0-2 couldn't resolve, run the .bzl file in a sandboxed
	// Starlark interpreter and read the resulting RuleClass globals.
	// Off by default — the interpreter is significantly slower than
	// AST-only extraction and pulls in starlark-go-bazel's dependency
	// surface. Enable for batch jobs (canopy indexing) where attrs
	// coverage matters more than latency.
	InterpreterFallback bool

	// ParseErrorHandler receives per-file Starlark parse errors as the
	// walker encounters them. Nil — the default — sends them to
	// os.Stderr via bzlwalk's built-in handler, which suits CLI use
	// but produces noise when assay is embedded as a library.
	// Library callers (canopy, MCP servers, benchmarks) should pass a
	// silent or routing handler:
	//
	//	assay.Analyze(ctx, dir,
	//	    assay.WithParseErrorHandler(func(_ string, _ error) {}))
	//
	// The handler is called once per file with a parse failure;
	// the walk continues past the failure regardless.
	ParseErrorHandler func(path string, err error)
}

// Option configures [Analyze]. Use the [WithDialect],
// [WithInterpreterFallback], and [WithParseErrorHandler] constructors
// rather than constructing Option values directly — that future-proofs
// against new fields on [Options].
type Option func(*Options)

// WithDialect overrides the dialect used to recognize Starlark symbols.
func WithDialect(d dialect.Dialect) Option {
	return func(o *Options) { o.Dialect = d }
}

// WithInterpreterFallback enables the Tier-3 interp path. See
// Options.InterpreterFallback for the cost / coverage tradeoff.
func WithInterpreterFallback() Option {
	return func(o *Options) { o.InterpreterFallback = true }
}

// WithParseErrorHandler installs a per-file parse-error callback.
// Use this to silence the default stderr noise when embedding assay
// as a library, or to route the errors into a structured log.
func WithParseErrorHandler(fn func(path string, err error)) Option {
	return func(o *Options) { o.ParseErrorHandler = fn }
}

// Analyze inspects the module source tree rooted at moduleDir and returns a
// ModuleReport. moduleDir must contain a MODULE.bazel at its root.
//
// ctx bounds the analysis: cancellation is checked at file-traversal
// granularity inside the walker and classifier. Callers running this
// across many modules (e.g. canopy batch jobs) should pass a deadline.
func Analyze(ctx context.Context, moduleDir string, opts ...Option) (*report.ModuleReport, error) {
	o := Options{Dialect: dialect.Bazel()}
	for _, opt := range opts {
		opt(&o)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	modPath := filepath.Join(moduleDir, "MODULE.bazel")
	// modulefile.ParseFile already names the path (via the underlying
	// *fs.PathError on I/O failure) and tags parse-time failures with
	// "parse MODULE.bazel:" — wrapping again here would double-name
	// the path or shadow the parse-vs-read distinction.
	r, err := modulefile.ParseFile(modPath)
	if err != nil {
		return nil, err
	}

	// Single shared walk + parse. Both bzlwalk and hermetic consume
	// this slice so each .bzl/BUILD file is parsed exactly once per
	// Analyze() — eliminates the historical 4-walk / 4-parse layout.
	files, err := walkparse.Walk(ctx, moduleDir)
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", moduleDir, err)
	}

	var walkOpts []bzlwalk.WalkOption
	if o.ParseErrorHandler != nil {
		walkOpts = append(walkOpts, bzlwalk.WithFileErrorHandler(o.ParseErrorHandler))
	}
	if err := bzlwalk.WalkParsed(ctx, o.Dialect, files, r, walkOpts...); err != nil {
		return nil, fmt.Errorf("walk %s: %w", moduleDir, err)
	}

	if err := hermetic.ClassifyParsed(ctx, o.Dialect, files, r); err != nil {
		return nil, fmt.Errorf("classify hermeticity: %w", err)
	}

	// Tier-3 fallback runs AFTER Tier 0-2 have populated the report so
	// it only touches rules that need it (AttrsExtractionMethod == "").
	// Hydrate is a no-op when the option is off; the import is kept
	// unconditional so callers don't pay a different build cost based
	// on whether they enable interpretation.
	if o.InterpreterFallback {
		interp.Hydrate(ctx, moduleDir, r)
	}

	assets.Extract(moduleDir, r)

	return r, nil
}
