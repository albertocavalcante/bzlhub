// Package taint carries the capture infrastructure for evaluating
// Bazel Starlark in analysis mode. URL extraction, repository-rule
// instantiation logging, and per-fork error collection all funnel
// through types defined here.
//
// See docs/plans/01-bazel-builtins-emulation/04-permissive-and-taint.md
// for the full design.
package taint

import (
	"strings"

	"go.starlark.net/starlark"
)

// InstSinkKey is the thread-local key under which the
// InvokeModuleExtension driver stashes a *[]RuleInstantiation. When
// RepositoryRuleClass.CallInternal sees this set, it records the
// instantiation; otherwise the call is a silent no-op.
const InstSinkKey = "starlark-go-bazel:inst-sink"

// Marker is the textual sentinel injected by stub.Permissive.String()
// and Permissive.Binary into any string derived from a Permissive
// value. Capture sites substring-detect this to taint URLs that came
// through Permissive but landed as regular starlark.String. Public so
// consumers can render "<permissive>" segments specially in UIs;
// prefer Has() over direct comparison.
const Marker = "<permissive>"

// Has reports whether s contains the taint marker. Use this in any
// code path that consumes a URL or other Starlark string to decide
// whether the value is fully resolved or contains an unresolved
// portion.
func Has(s string) bool {
	return strings.Contains(s, Marker)
}

// Sinks aggregates capture outputs from a Mode=Analysis evaluation.
// Populated by repository_ctx / module_ctx download methods + (M5)
// repository_rule instantiation calls.
type Sinks struct {
	// URLs is the list of network-fetch calls observed during eval.
	URLs []CapturedURL

	// Instantiations is the list of repo_rule(...) calls captured
	// from inside module_extension impls. Populated by M5 wiring.
	Instantiations []RuleInstantiation

	// ForkErrors collects per-platform impl errors from
	// InvokeRepositoryRule's (os, arch) fork loop. Populated by M5.
	ForkErrors []ForkError
}

// CapturedURL is one network-fetch call observed during eval. Multiple
// URLs from a list arg become multiple records.
type CapturedURL struct {
	URL         string
	SHA256      string
	Integrity   string
	Platform    string // e.g. "linux/amd64" or "any"
	StripPrefix string
	APIName     string // "ctx.download" or "ctx.download_and_extract"
	RuleName    string // populated by InvokeRepositoryRule at M5
	Tainted     bool
}

// RuleInstantiation is one repo_rule(...) call captured while running
// a module_extension impl. Rule is the captured *types.RepositoryRuleClass
// (held as starlark.Value to keep taint independent of types).
type RuleInstantiation struct {
	Rule  starlark.Value
	Attrs map[string]starlark.Value
}

// ForkError records a per-platform impl-call failure that the caller
// surfaces rather than abort the whole eval.
type ForkError struct {
	Platform Platform
	Err      error
}

// Platform names one (os, arch) target.
type Platform struct {
	OS   string
	Arch string
}

// Label returns the canonical "os/arch" string for display + dedup.
// Empty OS+Arch returns "any".
func (p Platform) Label() string {
	if p.OS == "" && p.Arch == "" {
		return "any"
	}
	if p.Arch == "" {
		return p.OS
	}
	return p.OS + "/" + p.Arch
}

// DefaultPlatforms is the standard six-fork matrix for analysis: the
// common cross-product of supported OS × architecture.
var DefaultPlatforms = []Platform{
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"},
	{OS: "windows", Arch: "arm64"},
}
