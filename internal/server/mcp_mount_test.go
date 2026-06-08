package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/featureflags"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// TestMCPMount_FlagGated verifies plan-64 §4 M1: when
// BZLHUB_MCP_HTTP_ENABLED is off the /mcp path falls through to the
// SPA fallback (returning index.html, not the MCP transport); when on
// it accepts JSON-RPC. Without this guard a typo in the feature-flag
// glue would silently expose the tool catalogue on every deployment.
func TestMCPMount_FlagGated(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	svc := bzlhub.New(s)

	// --- Flag OFF: explicit mount NOT registered. -------------------
	// POST /mcp falls through to the SPA NotFound handler, which
	// returns 200 + the embedded index.html. We assert "not JSON-RPC"
	// rather than a strict 404 because matching the established SPA
	// fallback behaviour is more honest than pretending /mcp is
	// always a sealed endpoint.
	tsOff := httptest.NewServer(server.NewWithOptions(nil, svc, nil, server.Options{
		Verifier: svc,
		Version:  "test",
		// Flags zero value → MCPHTTPEnabled=false
	}))
	t.Cleanup(tsOff.Close)

	res, err := http.Post(tsOff.URL+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if ct := res.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		t.Errorf("flag OFF: /mcp returned JSON (likely the MCP handler leaked through); ct=%s body=%s",
			ct, body)
	}

	// --- Flag ON: explicit mount registered, accepts JSON-RPC. ------
	tsOn := httptest.NewServer(server.NewWithOptions(nil, svc, nil, server.Options{
		Verifier: svc,
		Version:  "test",
		Flags:    featureflags.Flags{MCPHTTPEnabled: true},
	}))
	t.Cleanup(tsOn.Close)

	req, _ := http.NewRequest(http.MethodPost, tsOn.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("flag ON: /mcp status %d body=%s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"jsonrpc"`) {
		t.Errorf("flag ON: /mcp response not JSON-RPC envelope; body=%s", body)
	}
	if !strings.Contains(string(body), `"tools"`) {
		t.Errorf("flag ON: tools/list response missing tools array; body=%s", body)
	}

	// --- Flag ON + browser GET: serves the SPA setup page. ---------
	// A reader landing on /mcp via a footer link or by clicking the
	// /about → /mcp anchor sends Accept: text/html on the GET. The
	// MCP transport's 405-on-GET (plan-64 Decision 3) would be the
	// wrong answer for that audience; the wrapper in server.go
	// differentiates by Accept header and routes browsers to the SPA.
	req, _ = http.NewRequest(http.MethodGet, tsOn.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("flag ON: GET /mcp (browser) status %d body=%s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("flag ON: GET /mcp (browser) ct=%q, want text/html (SPA shell)", ct)
	}
}
