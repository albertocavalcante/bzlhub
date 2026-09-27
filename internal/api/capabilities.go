package api

import (
	"context"

	"github.com/albertocavalcante/assay/report"
	bzlsummary "github.com/albertocavalcante/bazel-module-summary-go"

	"github.com/albertocavalcante/bzlhub/internal/closurediff"
	"github.com/albertocavalcante/bzlhub/internal/compat"
	"github.com/albertocavalcante/bzlhub/internal/drift"
	"github.com/albertocavalcante/bzlhub/internal/modulediff"
)

// ModuleCatalog is the minimum capability required to enumerate
// sitemap coordinates.
type ModuleCatalog interface {
	ListModules(ctx context.Context) ([]ModuleSummary, error)
	ListVersions(ctx context.Context, name string) ([]string, error)
}

// ModuleSummaryReader is the minimum capability required for module
// metadata such as document head tags.
type ModuleSummaryReader interface {
	GetModule(ctx context.Context, name string) (*ModuleSummary, error)
}

// MCPSearchService is the read surface used by MCP search tools.
type MCPSearchService interface {
	Search(ctx context.Context, q Query) (*SearchResults, error)
	GetModuleVersion(ctx context.Context, name, version string) (*report.ModuleReport, error)
	ListVersions(ctx context.Context, name string) ([]string, error)
	Summary(ctx context.Context, name, version string) (*bzlsummary.Summary, error)
	History(ctx context.Context, opts HistoryOptions) ([]AuditEvent, error)
}

// MCPCodeNavigationService is the read surface used by MCP code tools.
type MCPCodeNavigationService interface {
	LookupSymbol(ctx context.Context, module, version, symbol string) (*ScipSymbolLookup, error)
	LookupReferences(ctx context.Context, module, version, symbol string, includeDefinition bool) (*ScipSymbolReferences, error)
	LookupConsumers(ctx context.Context, module, version, name string, includeSelf bool) (*ConsumersResult, error)
}

// MCPDiffService is the read surface used by MCP comparison tools.
type MCPDiffService interface {
	Drift(ctx context.Context, opts DriftOptions) (*drift.Report, error)
	Diff(ctx context.Context, opts DiffOptions) (*modulediff.Report, error)
	DiffClosure(ctx context.Context, opts DiffOptions) (*closurediff.Report, error)
	CompatCheck(ctx context.Context, body string, opts CompatCheckOptions) (*compat.Result, error)
}

// MCPSurfaceService is the read surface used by MCP dependency and
// air-gap inventory tools.
type MCPSurfaceService interface {
	AirgapSurface(ctx context.Context, name, version string) (*ClosureSurfaceResponse, error)
	ExternalSurface(ctx context.Context, name, version string) (*ExternalSurfaceResponse, error)
	Closure(ctx context.Context, name, version string) (*ClosureGraph, error)
	ReverseDeps(ctx context.Context, name, version string) (*ReverseDeps, error)
}

// MCPMutationService contains every MCP operation that changes local
// registry or index state. HTTP authorization gates this capability
// independently from the read interfaces above.
type MCPMutationService interface {
	Bump(ctx context.Context, opts BumpOptions) (*report.ModuleReport, error)
	IngestRecursive(ctx context.Context, opts IngestRecursiveOptions) (*IngestRecursiveResult, error)
}

// MCPService is the complete service accepted by the MCP transport.
// Registration helpers consume its narrower embedded capabilities.
type MCPService interface {
	MCPSearchService
	MCPCodeNavigationService
	MCPDiffService
	MCPSurfaceService
	MCPMutationService
}
