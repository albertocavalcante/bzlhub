// Package mcpsrv runs canopy as an MCP server over stdio. Coding agents
// (Claude Code, Codex, Gemini CLI, anything MCP-capable) can ask canopy
// questions mid-conversation: search the index, fetch a full ModuleReport.
//
// Transport: JSON-RPC 2.0 over stdin/stdout via mark3labs/mcp-go. All log
// output MUST go to stderr — stdout is the protocol stream.
//
// File layout:
//   - server.go         Serve + Verifier + registerTools dispatcher + jsonResult helper
//   - tools_search.go   read-only browsing: search, module_report, list_versions, summary, history
//   - tools_surface.go  URL-surface queries: airgap_surface, external_surface, closure_graph, reverse_deps, ingest_recursive
//   - tools_diff.go     impact analysis + bump flow: drift, bump, diff, diff_closure, compat_check
//   - tools_codenav.go  SCIP code-nav: lookup_symbol, lookup_references, consumers
//   - tools_verify.go   verify (gated on a non-nil Verifier)
package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/verify"
)

// Verifier is the slice of canopy functionality that the canopy_verify
// MCP tool needs. Lives separately from api.Canopy because the verify
// package imports store, store imports api, and folding Verify onto
// api.Canopy would close an import cycle. The concrete implementation
// (canopy.Service) satisfies both interfaces independently.
type Verifier interface {
	Verify(ctx context.Context, opts verify.Options) (*verify.Report, error)
}

// Serve runs the MCP server over stdio. Blocks until stdin closes or
// ctx is cancelled. If v is non-nil, canopy_verify is registered
// alongside the others. Stdio implies local trust — the operator
// started the binary themselves and the agent runs in-process — so
// write tools (canopy_ingest_recursive, canopy_bump) are always
// registered. HTTP callers should use registerTools directly and
// pass writeEnabled=false for anonymous-read deployments.
func Serve(ctx context.Context, c api.Canopy, v Verifier, version string) error {
	srv := server.NewMCPServer("canopy", version)
	registerTools(srv, c, v, true)
	// ServeStdio doesn't take a context in the current API; the function
	// returns when stdin closes. Cancellation hooks can be added if/when
	// the library exposes them.
	_ = ctx
	if err := server.ServeStdio(srv); err != nil {
		return fmt.Errorf("mcp serve stdio: %w", err)
	}
	return nil
}

// registerTools is the single dispatcher: each tools_*.go file owns a
// register<Domain> function for its group of tools. Splitting by
// domain keeps the AddTool spec + handler co-located so adding a new
// tool touches one file, not three.
//
// writeEnabled gates the mutation tools (canopy_ingest_recursive,
// canopy_bump). The HTTP transport on a public-read instance
// (bzlhub.com) passes false so anonymous visitors can't ingest or
// bump via tools/call; stdio and trusted-internal HTTP deployments
// pass true.
func registerTools(srv *server.MCPServer, c api.Canopy, v Verifier, writeEnabled bool) {
	registerSearchTools(srv, c)
	registerSurfaceTools(srv, c)
	registerDiffTools(srv, c)
	registerCodenavTools(srv, c)
	if v != nil {
		registerVerifyTools(srv, v)
	}
	if writeEnabled {
		registerSurfaceWriteTools(srv, c)
		registerDiffWriteTools(srv, c)
	}
}

// jsonResult marshals v as pretty JSON and wraps it in a TextContent result.
// Agents receive the structured data as text; their LLM can parse and act on
// it. For larger payloads we could move to mcp.JSONContent if/when it
// becomes a stable type in the spec.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{Type: "text", Text: string(body)},
		},
	}, nil
}
