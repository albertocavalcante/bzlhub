package mcpsrv

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// registerSearchTools registers the read-only browsing surface:
// search, module_report, list_versions, summary, history.
func registerSearchTools(srv *server.MCPServer, c api.Canopy) {
	srv.AddTool(
		mcp.NewTool("bzlhub_search",
			mcp.WithDescription("Full-text + faceted search across canopy's index of Bazel modules. Matches module names, rule names, provider names, macro names, and doc strings via FTS5 trigram tokenizer."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Free-text search query")),
			mcp.WithArray("hermeticity", mcp.Description("Optional hermeticity-class filter (any of pure-starlark, prebuilt-binaries-pinned, build-from-source, network-fetch-pinned, network-fetch-unpinned, requires-system-tools, repository-rule-arbitrary-code).")),
			mcp.WithNumber("limit", mcp.Description("Max hits to return (default 50, max 10000).")),
		),
		searchHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("bzlhub_module_report",
			mcp.WithDescription("Fetch the full ModuleReport for one (module, version) — includes rules with attribute schemas, providers with fields, macros, repository rules, module extensions, toolchains, and the hermeticity profile with per-finding provenance."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Module version")),
		),
		moduleReportHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("bzlhub_list_versions",
			mcp.WithDescription("List known versions of a Bazel module in canopy's index, newest first."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name")),
		),
		listVersionsHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("bzlhub_summary",
			mcp.WithDescription("Return the 'first impression' summary of one (module, version): name, version, compatibility level, declared bazel_deps, README contents, LICENSE name + path, example directories, and registry-level fields (homepage, maintainers, repository, yanked versions) when canopy's mirror has metadata.json. Use this when the user asks 'what is X@Y?' or wants a one-shot snapshot of a module without paging through rules/providers/macros (use bzlhub_module_report for the deep schema). Built on bazel-module-summary-go so the shape is stable across MCP, future CLI, and direct library consumers."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name (e.g. rules_go)")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Module version (e.g. 0.50.1)")),
		),
		summaryHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("bzlhub_history",
			mcp.WithDescription("Return recent audit events: bump_success / bump_failure / ingest_recursive_success / ingest_recursive_failure. Each row carries module, version, source surface (drift-ui / cli / mcp / rest), duration_ms, success flag, and optional error/payload. Use this to answer 'what happened to my mirror?' or 'who bumped rules_go yesterday?' Read-only ops (search, drift) are intentionally NOT logged here."),
			mcp.WithArray("kind", mcp.Description("Filter by event kinds (any of). Empty → all kinds.")),
			mcp.WithString("source", mcp.Description("Filter by source surface (e.g. drift-ui, cli, mcp, rest). Empty → any.")),
			mcp.WithString("module", mcp.Description("Filter to a specific module name. Empty → any.")),
			mcp.WithNumber("limit", mcp.Description("Max events to return (default 100, max 10000).")),
		),
		historyHandler(c),
	)
}

func searchHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		query, _ := args["query"].(string)
		if query == "" {
			return mcp.NewToolResultError("query is required"), nil
		}
		q := api.Query{Text: query}
		if limit, ok := args["limit"].(float64); ok {
			q.Limit = int(limit)
		}
		if herms, ok := args["hermeticity"].([]any); ok {
			for _, h := range herms {
				if s, ok := h.(string); ok {
					q.Hermeticity = append(q.Hermeticity, report.HermeticityClass(s))
				}
			}
		}
		results, err := c.Search(ctx, q)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search: %v", err)), nil
		}
		return jsonResult(results)
	}
}

func moduleReportHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		rep, err := c.GetModuleVersion(ctx, module, version)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("get %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(rep)
	}
}

func listVersionsHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		if module == "" {
			return mcp.NewToolResultError("module is required"), nil
		}
		versions, err := c.ListVersions(ctx, module)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list %s: %v", module, err)), nil
		}
		return jsonResult(map[string]any{"module": module, "versions": versions})
	}
}

// summaryHandler maps the MCP arg surface onto api.Canopy.Summary,
// which composes bazel-module-summary-go around canopy's mirrored
// sources and metadata.json. Surface-level concerns only — the
// library does all the actual data composition.
func summaryHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		sum, err := c.Summary(ctx, module, version)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("summary %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(sum)
	}
}

func historyHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		opts := api.HistoryOptions{}
		if kinds, ok := args["kind"].([]any); ok {
			for _, k := range kinds {
				if s, ok := k.(string); ok {
					opts.Kinds = append(opts.Kinds, s)
				}
			}
		}
		if s, ok := args["source"].(string); ok {
			opts.Source = s
		}
		if s, ok := args["module"].(string); ok {
			opts.Module = s
		}
		if n, ok := args["limit"].(float64); ok {
			opts.Limit = int(n)
		}
		events, err := c.History(ctx, opts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("history: %v", err)), nil
		}
		return jsonResult(map[string]any{"events": events})
	}
}
