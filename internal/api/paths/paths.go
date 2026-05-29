// Package paths is canopy's single source of truth for HTTP API path
// shape. Every site that registers a route, composes a URL, or tests an
// endpoint goes through here.
//
// Two surfaces:
//
//   - chi-pattern CONSTANTS — strings like "/{module}/versions/{version}"
//     used in r.Get/r.Post registrations. Carry the chi placeholder
//     syntax.
//   - COMPOSER FUNCTIONS — like External(module, version) → "/api/v1/
//     modules/<m>/versions/<v>/external" — for tests, Go HTTP clients,
//     and any other call site that needs a concrete URL.
//
// The two surfaces are intentional: chi can't accept a function value
// for route registration (must be a string), and tests/clients can't
// use the chi placeholder pattern (it doesn't URL-escape user input).
// Keeping both in one file means a future rename touches one place.
//
// Mirrored by ui/src/lib/api/paths.ts. Manual sync — both files small,
// editor-visible, convention-driven. If drift becomes an incident,
// adopt codegen; don't add presence-check theater.
package paths

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// Prefix is the API version root. Adding a /api/v2 means adding
// constants/functions next to these without altering the v1 surface.
const Prefix = "/api/v1"

// ----- chi route patterns ---------------------------------------------------
// These are passed to r.Get/r.Post in server.go. They carry the chi
// placeholder syntax ({module}, {version}). Use chi.URLParam(r, "module")
// to extract values inside handlers, or paths.ModuleVersion(r) for the
// common pair.

const (
	PatModule        = "/{module}"
	PatVersion       = "/versions/{version}"
	PatModuleVersion = PatModule + PatVersion
)

// ----- composer functions ---------------------------------------------------
// Use these in tests, in Go HTTP clients, and anywhere else that needs a
// concrete URL. URL-escape user input via url.PathEscape.

// moduleVersion builds the /api/v1/modules/<m>/versions/<v> prefix used
// by every per-version endpoint. Internal helper; call sites use the
// public composers below.
func moduleVersion(m, v string) string {
	return Prefix + "/modules/" + url.PathEscape(m) + "/versions/" + url.PathEscape(v)
}

// Top-level + collections.

func Search() string         { return Prefix + "/search" }
func XRefs() string          { return Prefix + "/xrefs" }
func Drift() string          { return Prefix + "/drift" }
func Upstreams() string      { return Prefix + "/upstreams" }
func ModulesIndex() string   { return Prefix + "/modules" }
func ModuleVersions(m string) string {
	return Prefix + "/modules/" + url.PathEscape(m) + "/versions"
}

// ModuleDetail is the per-module slice of ModulesIndex — single
// module summary lookup. Powers the cross-module HoverCard.
// Returns the same ModuleSummary shape one entry of ListModules
// would carry. HTTP 404 for unindexed names.
func ModuleDetail(m string) string {
	return Prefix + "/modules/" + url.PathEscape(m)
}

// Module-scoped (no version pinned).

func ModuleDiff(m string) string {
	return Prefix + "/modules/" + url.PathEscape(m) + "/diff"
}
func ModuleDiffClosure(m string) string { return ModuleDiff(m) + "/closure" }

// Version-scoped.

func ModuleVersionDetail(m, v string) string    { return moduleVersion(m, v) }
func External(m, v string) string               { return moduleVersion(m, v) + "/external" }
func Scip(m, v string) string                   { return moduleVersion(m, v) + "/scip" }
func Docs(m, v string) string                   { return moduleVersion(m, v) + "/docs" }
func ExampleFiles(m, v string) string           { return moduleVersion(m, v) + "/example-files" }
func ClosureGraph(m, v string) string           { return moduleVersion(m, v) + "/closure/graph" }
func ClosureReverseDeps(m, v string) string     { return moduleVersion(m, v) + "/closure/reverse-deps" }
// Consumers is Plan 07's cross-corpus consumer view — every call
// site of the named rule/provider/macro/repo_rule/module_extension
// across canopy's indexed corpus.
func Consumers(m, v, name string) string {
	return moduleVersion(m, v) + "/consumers/" + url.PathEscape(name)
}
func AirgapSurface(m, v string) string          { return moduleVersion(m, v) + "/airgap/surface" }
func AirgapDownloaderConfig(m, v string) string { return moduleVersion(m, v) + "/airgap/downloader-config" }
func AirgapModuleMirrors(m, v string) string    { return moduleVersion(m, v) + "/airgap/module-mirrors" }

// Actions (RPC writes).

func ActionBump() string            { return Prefix + "/actions/bump" }
func ActionCompatCheck() string     { return Prefix + "/actions/compat-check" }
func ActionIngestRecursive() string { return Prefix + "/actions/ingest/recursive" }
func ActionIngestMissing(m, v string) string {
	return Prefix + "/actions/modules/" + url.PathEscape(m) +
		"/versions/" + url.PathEscape(v) + "/ingest-missing"
}

// Activity (observability).

func ActivityHistory() string { return Prefix + "/activity/history" }
func ActivityEvents() string  { return Prefix + "/activity/events" }

// System (admin / introspection).

func SystemVersion() string  { return Prefix + "/system/version" }
func SystemFeatures() string { return Prefix + "/system/features" }
func SystemBCRProbe() string { return Prefix + "/system/bcr-probe" }

// ----- handler helpers ------------------------------------------------------

// ModuleVersion extracts the {module} and {version} chi params from an
// incoming request. Returns empty strings when a param isn't set —
// matches the raw chi.URLParam behavior, which downstream service-layer
// calls already handle as a graceful 404. A "must"-style variant was
// considered and rejected (panicking on misconfig is worse than today's
// behavior).
func ModuleVersion(r *http.Request) (module, version string) {
	return chi.URLParam(r, "module"), chi.URLParam(r, "version")
}
