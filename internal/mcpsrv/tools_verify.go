package mcpsrv

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/bzlhub/internal/verify"
)

// registerVerifyTools registers the bzlhub_verify tool. Called only
// when Serve was given a non-nil Verifier (the in-process verify
// binding only exists when bzlhub serve/mcp was wired with a mirror
// root + db path).
func registerVerifyTools(srv *server.MCPServer, v Verifier) {
	srv.AddTool(
		mcp.NewTool("bzlhub_verify",
			mcp.WithDescription("Run integrity + consistency checks over the local canopy mirror tree. Five checks in one pass: blob_integrity (tarball SHA256 vs source.json SRI), source_json_schema (url + integrity well-formed), module_bazel_present (MODULE.bazel exists + parses), index_mirror_agreement (SQLite index ↔ mirror tree consistency), orphan_blobs (blobs/ contents are all referenced). Each finding has a Severity (error|warning|info), a Kind (machine-routable), a Message, and a Fix hint.\n\nUse this BEFORE any destructive operation against the mirror (bump, ingest_recursive, manual cleanup) — corrupted state is much harder to recover from after a write. Also a sensible periodic health probe; the call is cheap (sub-second on small mirrors) unless `deep` is set, which re-assays every module and is minutes-scale.\n\nReturns Report{Findings[], Errors, Warnings, Info, ModulesExamined, BlobsExamined, Elapsed}. Empty findings + healthy counts = the mirror is in a known-good state. Surface any Error findings to the user verbatim — they each include a Fix string telling the user the recommended remediation."),
			mcp.WithString("mirror_root", mcp.Required(), mcp.Description("Absolute path to the BCR-shape mirror directory.")),
			mcp.WithString("db_path", mcp.Description("Optional SQLite index path. When set, the index_mirror_agreement check runs; otherwise that check is skipped (others still run).")),
			mcp.WithBoolean("deep", mcp.Description("Optional. Re-runs assay on every module and diffs the result against the stored ModuleReport. Slow on large mirrors; opt-in only.")),
			mcp.WithArray("check", mcp.Description("Optional. Restrict to specific check kinds (any of: blob_integrity, blob_missing, source_json_schema, module_bazel_present, index_mirror_agreement, orphan_blobs, deep_report_mismatch). Empty → all enabled checks run.")),
		),
		verifyHandler(v),
	)
}

func verifyHandler(v Verifier) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		mirrorRoot, _ := args["mirror_root"].(string)
		if mirrorRoot == "" {
			return mcp.NewToolResultError("mirror_root is required"), nil
		}
		dbPath, _ := args["db_path"].(string)
		deep, _ := args["deep"].(bool)

		opts := verify.Options{
			MirrorRoot: mirrorRoot,
			DBPath:     dbPath,
			Deep:       deep,
		}
		if raw, ok := args["check"].([]any); ok {
			for _, k := range raw {
				if s, ok := k.(string); ok {
					opts.Checks = append(opts.Checks, verify.Kind(s))
				}
			}
		}
		r, err := v.Verify(ctx, opts)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("verify: %v", err)), nil
		}
		return jsonResult(r)
	}
}
