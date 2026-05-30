package mcpsrv

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/canopy/internal/api"
)

// registerDiffTools registers the READ-side analysis tools: drift,
// diff, diff_closure, compat_check. These read canopy state + may
// fetch upstream metadata transiently but never write the mirror.
// Safe to expose anonymously on a public read-only instance.
//
// The write-side companion canopy_bump lives in
// registerDiffWriteTools below.
func registerDiffTools(srv *server.MCPServer, c api.Canopy) {
	srv.AddTool(
		mcp.NewTool("canopy_drift",
			mcp.WithDescription("Compare canopy's local mirror against an upstream BCR-shape registry. Returns per-module drift status (in-sync / behind / yanked-upstream / local-only / upstream-error) plus the newer versions available. Use this to find out what's stale before calling canopy_bump."),
			mcp.WithString("upstream", mcp.Description("Upstream registry URL. Default: https://bcr.bazel.build (whatever the canopy serve was configured with).")),
			mcp.WithString("module", mcp.Description("Optional: limit the report to this single module name.")),
		),
		driftHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_diff",
			mcp.WithDescription("Structured diff between two versions of the same Bazel module. Surfaces concrete migration impact: bazel_deps added/removed/version-changed, compatibility_level shift, rules added/removed/attribute-schema-changed (with per-attribute type/default/mandatory deltas), provider field-set changes, macro additions/removals, aspect/toolchain/repository_rule changes, module_extension changes (incl. tag_class deltas — the use_extension surface), and hermeticity class shifts. By default both versions must be in the local index. Pass 'upstream' to enable a what-if mode: missing sides are fetched + analyzed transiently from that registry without persisting — useful for previewing a bump before committing. Response includes from_source/to_source ('local'|'upstream') so callers know which path was taken.\n\nIMPORTANT: the response also carries a 'breaking' array — one entry per structurally-breaking finding (compat_level_shift, rule/provider/extension/repo_rule removed, attribute removed, mandatory flip, provider field removed, tag_class removed). BEFORE recommending or executing a bump, call canopy_diff and check 'breaking'. If non-empty, the bump WILL require consumer code changes; surface the findings to the user before proceeding. An empty breaking array is the closest thing to a green light a bump can get."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name.")),
			mcp.WithString("from", mcp.Required(), mcp.Description("Source version (the one currently in your build).")),
			mcp.WithString("to", mcp.Required(), mcp.Description("Target version (where you want to go).")),
			mcp.WithString("upstream", mcp.Description("Optional BCR-shape registry URL; enables on-the-fly fetch + analyze for any version missing from the local index.")),
		),
		diffHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_diff_closure",
			mcp.WithDescription("Recursive bazel_dep CLOSURE diff: walks the MVS-resolved closure on both sides, runs a per-module diff for every dep whose version moved (including the root), and rolls up breaking findings into a closure-wide total. This is the migration's true blast radius — a bump of X@from→X@to pulls in different versions of every transitive dep, each with its own potentially-breaking surface. Use this INSTEAD OF canopy_diff for any non-trivial advice; canopy_diff alone hides the transitive impact.\n\nRequires 'upstream' (MVS resolution needs a registry). The response carries:\n  • closure_breaking_total — single number; this is the headline CI/PR gate signal.\n  • closure_breaking_by_module — map of module → breaking-count, so you can name the worst offenders.\n  • module_diffs — full per-module breakdowns for every changed module, including the root. Drill into specific modules from here when the user asks 'what exactly breaks?'\n  • closure_deps — added/removed/version-changed modules at the closure level.\n  • errors_by_module — modules that couldn't be analyzed (e.g. unusual archive formats). Surface gaps explicitly; never silently skip.\n\nIMPORTANT: BEFORE recommending or executing a bump, call canopy_diff_closure and check closure_breaking_total. If > 0, name the per-module contributors from closure_breaking_by_module in your assessment to the user. An empty rollup with zero errors_by_module is the closest a closure-wide bump can get to a green light."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name (the root of the closure walk).")),
			mcp.WithString("from", mcp.Required(), mcp.Description("Source version.")),
			mcp.WithString("to", mcp.Required(), mcp.Description("Target version.")),
			mcp.WithString("upstream", mcp.Required(), mcp.Description("BCR-shape registry URL. REQUIRED — closure walking needs a registry for MVS resolution.")),
		),
		diffClosureHandler(c),
	)

	srv.AddTool(
		mcp.NewTool("canopy_compat_check",
			mcp.WithDescription("Given a MODULE.bazel text blob, diff every bazel_dep against the LATEST indexed version in canopy and report what BREAKS for a consumer that adopts the latest. This is the analyzer behind the /compat-check UI page; agents call it directly when the user asks 'is it safe to upgrade?' or 'why did rules_go 0.50→0.51 break my build?'.\n\nThe response shape (compat.Result):\n  - self: the analyzed module's (name, version) when input declares one\n  - summary: total_deps / breaking_deps / missing_from_corpus / already_latest\n  - deps[]: one entry per bazel_dep with from_version → to_version, in_corpus flag, breaking_count, and an inline `findings[]` array. Each finding carries kind + symbol + reason + hint + codemod (Plan 06 — ready-to-pipe `buildozer ...` command, or `# review:` discovery comment for kinds that need human judgment).\n  - plan_markdown: paste-ready migration plan for a PR description\n  - plan_shell: ready-to-pipe `migrate.sh` bash script (Plan 06) that wraps every clean codemod in `run` + emits `[manual]` rows for discovery-only findings. Defaults to --dry-run; explicit --apply to mutate.\n\nIMPORTANT: surface the codemods + safety messaging when recommending a bump. The script is a SUGGESTION; Buildozer edits are pattern-based and may match more sites than intended. Always recommend the operator review before --apply."),
			mcp.WithString("body", mcp.Required(), mcp.Description("MODULE.bazel content as a single string. Max ~256KB.")),
			mcp.WithBoolean("include_dev", mcp.Description("Include dev_dependency = True bazel_deps in the analysis. Default false (matches the 'will my prod build break?' framing).")),
		),
		compatCheckHandler(c),
	)
}

// registerDiffWriteTools registers the mutation tools in the diff
// family — currently canopy_bump, which fetches and persists a
// (module, version) into the local mirror. Called separately by the
// dispatcher so HTTP deployments can opt out via
// featureflags.MCPWriteToolsEnabled; stdio always registers it.
func registerDiffWriteTools(srv *server.MCPServer, c api.Canopy) {
	srv.AddTool(
		mcp.NewTool("canopy_bump",
			mcp.WithDescription("Fetch one (module, version) from an upstream BCR-shape registry, mirror it locally, and index it. Idempotent: re-bumping the same version is a no-op. Use this after canopy_drift to advance a module to its upstream-latest. Returns the produced ModuleReport. Errors return early: 'not configured' means canopy was started without a mirror root; integrity / upstream errors are surfaced verbatim."),
			mcp.WithString("module", mcp.Required(), mcp.Description("Bazel module name (e.g., 'rules_go').")),
			mcp.WithString("version", mcp.Required(), mcp.Description("Target version (e.g., '0.52.0' or a 4-component canopy variant '0.52.0.1').")),
			mcp.WithString("upstream", mcp.Description("Upstream registry URL. Default: the service's configured default (typically https://bcr.bazel.build).")),
		),
		bumpHandler(c),
	)
}

func driftHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		upstream, _ := args["upstream"].(string)
		module, _ := args["module"].(string)
		rep, err := c.Drift(ctx, api.DriftOptions{Upstream: upstream, Module: module})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("drift: %v", err)), nil
		}
		return jsonResult(rep)
	}
}

func bumpHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		version, _ := args["version"].(string)
		if module == "" || version == "" {
			return mcp.NewToolResultError("module and version are required"), nil
		}
		upstream, _ := args["upstream"].(string)
		rep, err := c.Bump(ctx, api.BumpOptions{Module: module, Version: version, Upstream: upstream, Source: "mcp"})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("bump %s@%s: %v", module, version, err)), nil
		}
		return jsonResult(rep)
	}
}

func diffHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		from, _ := args["from"].(string)
		to, _ := args["to"].(string)
		upstream, _ := args["upstream"].(string)
		if module == "" || from == "" || to == "" {
			return mcp.NewToolResultError("module, from, and to are required"), nil
		}
		d, err := c.Diff(ctx, api.DiffOptions{
			Module:      module,
			FromVersion: from,
			ToVersion:   to,
			Upstream:    upstream,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("diff %s %s→%s: %v", module, from, to, err)), nil
		}
		return jsonResult(d)
	}
}

func diffClosureHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		module, _ := args["module"].(string)
		from, _ := args["from"].(string)
		to, _ := args["to"].(string)
		upstream, _ := args["upstream"].(string)
		if module == "" || from == "" || to == "" {
			return mcp.NewToolResultError("module, from, and to are required"), nil
		}
		if upstream == "" {
			return mcp.NewToolResultError("upstream is required for closure diff (MVS resolution needs a registry)"), nil
		}
		d, err := c.DiffClosure(ctx, api.DiffOptions{
			Module:      module,
			FromVersion: from,
			ToVersion:   to,
			Upstream:    upstream,
		})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("diff-closure %s %s→%s: %v", module, from, to, err)), nil
		}
		return jsonResult(d)
	}
}

// Plan 05/06: compat-check via MCP. Returns the full compat.Result —
// including plan_markdown + plan_shell (Plan 06 codemods) — so an
// agent can recommend a migration AND attach the ready-to-pipe
// migrate.sh in one round-trip.
func compatCheckHandler(c api.Canopy) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		body, _ := args["body"].(string)
		if body == "" {
			return mcp.NewToolResultError("body is required (MODULE.bazel content)"), nil
		}
		opts := api.CompatCheckOptions{}
		if inc, ok := args["include_dev"].(bool); ok {
			opts.IncludeDevDependencies = inc
		}
		res, err := c.CompatCheck(ctx, body, opts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("compat-check: %v", err)), nil
		}
		return jsonResult(res)
	}
}
