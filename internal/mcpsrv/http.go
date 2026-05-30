package mcpsrv

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/canopy/internal/api"
)

// NewHTTPHandler returns an http.Handler implementing the MCP
// Streamable HTTP transport (spec 2025-11-25).
//
// Decisions per plan-64 §2:
//   - Stateless. WithStateLess(true) — no Mcp-Session-Id ever issued;
//     every POST is independent. Load-balancer-friendly when (if) we
//     shard, and avoids "session lost" UX when canopy restarts.
//   - Non-streaming. WithDisableStreaming(true) — GETs return 405
//     instead of opening an SSE channel. All bzlhub tools complete
//     well under 100ms; SSE adds Cloudflare Tunnel buffering
//     complexity for zero gain at this scale. Revisit when search
//     starts producing progressive results.
//   - Per-request *MCPServer. Per plan-64 Gotcha 2 the stateless mode
//     in mark3labs/mcp-go (mirroring the TypeScript SDK 1.26.0
//     CVE-class fix) has a known cross-client leak risk when one
//     *MCPServer is shared across goroutines. Constructing fresh per
//     request costs ~one allocation of tool-registry scaffolding per
//     call — bzlhub serves <100 req/s and tool-call cost dwarfs
//     setup.
//
// Mount EXCLUSIVELY at /mcp on the parent chi.Mux. mark3labs/mcp-go
// #493 documents that the Streamable HTTP server treats ANY GET on
// its mount path (or any path beneath it) as an SSE channel-open
// request. Registering siblings under /mcp/* would silently hang
// curl clients.
//
// The v argument may be nil — when so, canopy_verify is not
// registered, matching the same nil-safe contract as Serve (stdio).
//
// writeEnabled gates the MUTATION tools (canopy_ingest_recursive,
// canopy_bump). Public read-only deployments should pass false:
// anonymous visitors then see ~17 read tools in tools/list and
// cannot trigger background mirror writes via tools/call. Trusted
// internal deployments pass true. The server.go wiring drives this
// off featureflags.MCPWriteToolsEnabled (default false).
func NewHTTPHandler(c api.Canopy, v Verifier, version string, writeEnabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Per-request server: fresh tool dispatcher state, no
		// cross-goroutine reuse. Allocation is the cost; the win is
		// no chance of one client's tool-call response data
		// inadvertently flushing into another client's connection
		// while both are mid-flight.
		srv := server.NewMCPServer("canopy", version)
		registerTools(srv, c, v, writeEnabled)

		httpSrv := server.NewStreamableHTTPServer(
			srv,
			server.WithStateLess(true),
			server.WithDisableStreaming(true),
		)
		httpSrv.ServeHTTP(w, req)
	})
}
