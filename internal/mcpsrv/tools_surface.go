package mcpsrv

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/canopy/internal/api"
)

// registerSurfaceTools registers URL-surface + closure-graph queries:
// airgap_surface, external_surface, closure_graph, reverse_deps,
// ingest_recursive. These are the "what does this module pull in,
// what does it expose, who depends on it" tools.
func registerSurfaceTools(srv *server.MCPServer, c api.Canopy) {
	srv.AddTool(
		mcp.NewTool("canopy_airgap_surface",
			mcp.WithDescription("Return the closure-wide URL surface for one (module, version): every URL the entire transitive bazel_deps closure of <module>@<version> would fetch, unioned and deduplicated, plus per-closure-node ref counts so the caller can see which dependency contributes which URLs. This is the airgap-prep question — 'what mirror entries do I need to bring this build into a sealed environment?' — answered for the complete closure, not just one module. Each ref carries the same classification + mutability + tainted fields as canopy_external_surface. Returns refs, modules (one row per closure node with ref_count + class_counts), fork_errors, class_counts (closure-wide), max_depth_reached, and missing_modules (closure references not yet in canopy's index)."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name (e.g. rules_go)")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Module version (e.g. 0.50.1)")),
		),
		airgapSurfaceHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_external_surface",
			mcp.WithDescription("Return the external URL surface for one (module, version) — every URL the module's repository_rule and module_extension implementations would fetch, extracted via canopy's static analysis pipeline. Each ref is classified by ecosystem (bcr / maven / pypi-canonical / pypi-extra / npm / go-proxy / github-release / github-archive / oci / cloud-storage / vendor-http / unknown) and labeled with mutability (immutable when sha256/integrity-pinned; mutable-host for github archives; unknown otherwise) and a `tainted` flag when the analyzer's per-fork eval depended on opaque state (ctx.execute output, opaque external load). Use this when answering 'what URLs would I need to mirror in an air-gapped environment?' or 'is this module's download surface deterministic?'. Returns refs, fork_errors (per-platform analysis failures), and class_counts (chip-style aggregate)."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name (e.g. rules_go)")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Module version (e.g. 0.50.1)")),
		),
		externalSurfaceHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_closure_graph",
			mcp.WithDescription("Return the bazel_dep closure of (module, version) as a directed graph — nodes + edges walked from the locally-persisted reports. Each node is a (name, version) pair plus an `external` flag indicating modules referenced but NOT indexed in this canopy (use canopy_ingest_recursive to fill the gap). The graph stops at a depth cap; `max_depth_reached` flags when the walk hit it.\n\nUse this to ground answers to 'what does X@Y pull in?' and to surface gaps in the corpus before recommending bumps. For VISUAL output, consumers usually render via Mermaid (see canopy's UI); the JSON shape here is the same one the UI consumes."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Root module name (e.g., 'rules_go').")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Root version.")),
		),
		closureGraphHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_reverse_deps",
			mcp.WithDescription("Return the modules in this canopy that depend on (module, version) — i.e., 'who consumes me?' Walks every indexed (m, v)'s bazel_deps and collects the consumers. Same data canopy's UI surfaces as 'used by N modules' on the listing cards, but at version-specific granularity instead of name-only.\n\nUse this when the user asks 'is it safe to break X?' or 'who depends on this module?' Empty result = no known consumers in this canopy (which doesn't mean none globally — canopy is your local mirror, not the world)."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Target module name.")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Target version (specific; reverse-deps are not aggregated across versions here — call once per version).")),
		),
		reverseDepsHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_ingest_recursive",
			mcp.WithDescription("Walk the bazel_dep closure of (module, version) and mirror every reached version into canopy's local tree. Each module is fetched, SRI-verified, mirrored (modules/<n>/<v>/* + content-addressed blobs/<sha256>), and indexed. Errors on individual modules don't abort sibling fetches — partial closures are useful inputs to canopy_drift. Returns visited/mirrored counts + per-module error list. Use with include_bazel_tools=true to also seed Bazel's implicit MODULE.tools deps for a self-sufficient air-gap mirror."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Root module name (e.g., 'rules_go').")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Root version (e.g., '0.52.0').")),
			mcp.WithString("upstream", mcp.Description("Upstream registry URL. Default: the service's configured default.")),
			mcp.WithBoolean("include_bazel_tools", mcp.Description("Seed the closure with Bazel's implicit MODULE.tools deps for the given bazel_version. Required for a fully self-sufficient air-gap mirror because Bazel injects these regardless of the user's MODULE.bazel.")),
			mcp.WithString("bazel_version", mcp.Description("Bazel version used when include_bazel_tools=true. Default 9.1.0.")),
			mcp.WithNumber("workers", mcp.Description("Concurrent fetchers. Default 8. Bandwidth is usually the bottleneck.")),
		),
		ingestRecursiveHandler(c),
	)
}

func airgapSurfaceHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		resp, err := c.AirgapSurface(ctx, module, version)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("airgap surface %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(resp)
	}
}

func externalSurfaceHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		resp, err := c.ExternalSurface(ctx, module, version)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("external surface %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(resp)
	}
}

// closureGraphHandler exposes Service.Closure to MCP agents — the
// forward bazel_dep walk from a (module, version) over canopy's
// persisted reports.
func closureGraphHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		g, err := c.Closure(ctx, module, version)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("closure %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(g)
	}
}

// reverseDepsHandler exposes Service.ReverseDeps to MCP agents —
// the "who depends on me" walk across canopy's corpus.
func reverseDepsHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		rd, err := c.ReverseDeps(ctx, module, version)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reverse-deps %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(rd)
	}
}

// Source-tag the ingest_recursive call so audit rows distinguish
// MCP-triggered changes from REST/UI/CLI. Wrapper is tiny: it fills
// in Source then defers to the existing api.Canopy method.
func ingestRecursiveHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		opts := api.IngestRecursiveOptions{Module: module, Version: version, Source: "mcp"}
		if up, ok := args["upstream"].(string); ok {
			opts.Upstream = up
		}
		if inc, ok := args["include_bazel_tools"].(bool); ok {
			opts.IncludeBazelTools = inc
		}
		if bv, ok := args["bazel_version"].(string); ok {
			opts.BazelVersion = bv
		}
		if w, ok := args["workers"].(float64); ok {
			opts.Workers = int(w)
		}
		res, err := c.IngestRecursive(ctx, opts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("ingest-recursive %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(res)
	}
}
