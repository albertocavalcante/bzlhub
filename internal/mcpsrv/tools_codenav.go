package mcpsrv

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// registerCodenavTools registers SCIP-backed code-navigation tools:
// lookup_symbol, lookup_references, consumers. Together they answer
// "where is this defined?", "where is this used inside the module?",
// and "who across the corpus calls this?".
func registerCodenavTools(srv *server.MCPServer, c api.Canopy) {
	srv.AddTool(
		mcp.NewTool("bzlhub_lookup_symbol",
			mcp.WithDescription("Resolve a SCIP symbol string to its definition site (file + range + documentation) for a specific (module, version). Backed by understory v0.1.0, the OSS code-navigation library canopy embeds.\n\nThe SCIP symbol shape canopy emits is `bzlmod <module>@<version> <relpath>#<name>` — e.g. `bzlmod rules_python@0.40.0 python/defs.bzl#py_library`. You construct the symbol by combining the (module, version) coordinate with the rule/provider/macro's provenance.file (returned by bzlhub_get_module) and its short name.\n\nReturns Found=false (NOT an error) when the symbol isn't defined in this index — typical when the symbol is present only as an external `load(...)` reference (which means it's defined in a DIFFERENT module's SCIP; call bzlhub_lookup_symbol again with that module's coordinates).\n\nUse this when you need the AUTHORITATIVE location for a symbol with cross-module precision. For broad lookups (rules/providers in a module), prefer bzlhub_get_module — its ModuleReport already carries provenance and is cheaper."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name (e.g. rules_python).")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Module version (e.g. 0.40.0).")),
			mcp.WithString("symbol", mcp.Required(), mcp.Description("Full SCIP symbol string. Shape: `bzlmod <module>@<version> <relpath>#<name>`.")),
		),
		lookupSymbolHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("bzlhub_lookup_references",
			mcp.WithDescription("Return every occurrence of a SCIP symbol in (module, version)'s index — call sites, variable reads, and (optionally) the definition itself. Backed by understory.\n\nUse this to answer \"where is X used inside module M?\". For cross-module usage (\"where is rules_python.py_library called from rules_proto?\"), call bzlhub_lookup_references on EACH consuming module — canopy's per-module SCIP indexes don't carry cross-module reference edges yet.\n\nSet include_definition=false to get usage sites only (typical for refactoring / 'find usages'). Set true (default) to include the def occurrence in the same response.\n\nReturns {count: N, references: [...]}. Empty references = symbol unreferenced in this index OR symbol absent. Distinguishing the two would need a separate query — for now collapse both into count=0."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name.")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Module version.")),
			mcp.WithString("symbol", mcp.Required(), mcp.Description("Full SCIP symbol string (same shape as bzlhub_lookup_symbol).")),
			mcp.WithBoolean("include_definition", mcp.Description("Include the definition occurrence in the result. Default true.")),
		),
		lookupReferencesHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("bzlhub_consumers",
			mcp.WithDescription("Given a rule / provider / macro / repo_rule / module_extension name defined by (module, version), return EVERY call site of that symbol across canopy's indexed corpus (Plan 07). This is something pkg.go.dev does at the import level; canopy does it at the rule-call level — no other Bazel registry does this.\n\nThree workflows:\n  1. Maintainer planning a removal: 'I'm dropping cc_binary.linkstatic_legacy — who uses it?'\n  2. Consumer studying usage: 'How do other modules actually call rules_oci.oci_image?'\n  3. Migration audit: paired with bzlhub_compat_check codemods, this shows which sites the codemod will touch.\n\nResponse:\n  - symbol: the resolved SCIP symbol string (`bzlmod <m>@<v> <relpath>#<name>`)\n  - kind: rule | provider | macro | repo_rule | module_extension\n  - total_call_sites + consumer_count: across the corpus, AFTER filtering the defining module's own occurrences\n  - consumers[]: per (consumer_module, consumer_version) with call_sites[] (file + line + pre-shaped code-nav href)\n\nThe defining module is filtered by default — operators don't want their own examples drowning the list. Set include_self=true to keep them.\n\n404 cases (returned as errors): module not indexed, or `name` doesn't resolve to any symbol in that module's report."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Defining module name (e.g. 'rules_cc').")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Defining module version (e.g. '0.0.10').")),
			mcp.WithString("name", mcp.Required(), mcp.Description("User-facing symbol name (rule/provider/macro/repo_rule/module_extension identifier). The server resolves to the SCIP symbol via the module's stored ModuleReport.")),
			mcp.WithBoolean("include_self", mcp.Description("Include the defining module's own occurrences. Default false (consumers-only view).")),
		),
		consumersHandler(c),
	)
}

func lookupSymbolHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		symbol, _ := args["symbol"].(string)
		if module == "" || version == "" || symbol == "" {
			return mcp.NewToolResultError("module, version, and symbol are all required"), nil
		}
		res, err := c.LookupSymbol(ctx, module, version, symbol)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("lookup-symbol %s@%s %q: %v", module, version, symbol, err)), nil
		}
		return jsonResult(res)
	}
}

func lookupReferencesHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		symbol, _ := args["symbol"].(string)
		if module == "" || version == "" || symbol == "" {
			return mcp.NewToolResultError("module, version, and symbol are all required"), nil
		}
		// include_definition defaults to true (match the library
		// default). MCP clients can override by passing the flag.
		includeDef := true
		if v, ok := args["include_definition"].(bool); ok {
			includeDef = v
		}
		res, err := c.LookupReferences(ctx, module, version, symbol, includeDef)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("lookup-references %s@%s %q: %v", module, version, symbol, err)), nil
		}
		return jsonResult(res)
	}
}

// Plan 07: cross-corpus consumer view via MCP. Translates the
// user-facing name → SCIP symbol via the defining module's
// ModuleReport, then walks every indexed (m, v) to collect call
// sites. Filters the defining module by default.
func consumersHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		name, _ := args["name"].(string)
		if module == "" || version == "" || name == "" {
			return mcp.NewToolResultError("module, version, and name are all required"), nil
		}
		includeSelf := false
		if v, ok := args["include_self"].(bool); ok {
			includeSelf = v
		}
		res, err := c.LookupConsumers(ctx, module, version, name, includeSelf)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("consumers %s@%s/%s: %v", module, version, name, err)), nil
		}
		return jsonResult(res)
	}
}
