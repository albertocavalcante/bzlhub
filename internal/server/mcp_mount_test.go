package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/albertocavalcante/bzlhub/internal/auth"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/featureflags"
	"github.com/albertocavalcante/bzlhub/internal/policy"
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
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("flag ON: GET /mcp (browser) status %d body=%s", res.StatusCode, body)
	}
	// Source-only checkouts intentionally keep only
	// internal/embed/ui/.gitkeep; their SPA handler returns the explicit 503
	// build stub. CI and release builds populate the directory first and return
	// 200. Both outcomes prove the browser request reached the SPA rather than
	// the MCP transport.
	if res.StatusCode == http.StatusServiceUnavailable &&
		!strings.Contains(string(body), "UI not built") {
		t.Fatalf("flag ON: GET /mcp returned unexpected 503 body=%s", body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("flag ON: GET /mcp (browser) ct=%q, want text/html (SPA shell)", ct)
	}
}

func TestMCPMount_PolicyGatesReadAndWritePerIdentity(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	svc := bzlhub.New(s)

	const (
		readerToken   = "reader-token-with-enough-random-material"
		approverToken = "approver-token-with-enough-random-material"
	)
	registry := mcpTestBearerRegistry(t, map[string][]string{
		readerToken:   {"reader"},
		approverToken: {"approver"},
	})
	pol := &policy.Policy{Auth: policy.Auth{Actions: map[string]policy.Gate{
		"use_mcp_read":  policy.GateAny,
		"use_mcp_write": policy.Gate("group:approver"),
	}}}
	ts := httptest.NewServer(server.NewWithOptions(nil, svc, nil, server.Options{
		Verifier:       svc,
		Version:        "test",
		BearerRegistry: registry,
		Policy:         policy.Static(pol),
		Flags: featureflags.Flags{
			MCPHTTPEnabled:       true,
			MCPWriteToolsEnabled: true,
		},
	}))
	t.Cleanup(ts.Close)

	for _, tc := range []struct {
		name      string
		token     string
		wantWrite bool
	}{
		{name: "anonymous"},
		{name: "authenticated non-approver", token: readerToken},
		{name: "approver", token: approverToken, wantWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, status := mcpListTools(t, ts.URL, tc.token)
			if status != http.StatusOK {
				t.Fatalf("status = %d, body=%s", status, body)
			}
			for _, tool := range []string{"bzlhub_bump", "bzlhub_ingest_recursive"} {
				got := strings.Contains(body, `"`+tool+`"`)
				if got != tc.wantWrite {
					t.Errorf("tool %s advertised=%v, want %v; body=%s",
						tool, got, tc.wantWrite, body)
				}
			}
		})
	}

	pol.Auth.Actions["use_mcp_read"] = policy.GateAuthenticated
	body, status := mcpListTools(t, ts.URL, "")
	if status != http.StatusForbidden {
		t.Fatalf("anonymous read status = %d, want %d; body=%s",
			status, http.StatusForbidden, body)
	}
	if !strings.Contains(body, "use_mcp_read") {
		t.Errorf("anonymous denial does not name policy action; body=%s", body)
	}
}

func mcpListTools(t *testing.T, baseURL, token string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), res.StatusCode
}

func mcpTestBearerRegistry(t *testing.T, tokens map[string][]string) *auth.IdentityRegistry {
	t.Helper()
	var entries []string
	for token, groups := range tokens {
		sum := sha256.Sum256([]byte(token))
		quotedGroups := make([]string, 0, len(groups))
		for _, group := range groups {
			quotedGroups = append(quotedGroups, `"`+group+`"`)
		}
		entries = append(entries, `{
			"token_sha256": "`+hex.EncodeToString(sum[:])+`",
			"identity": {
				"user": "`+groups[0]+`@example.com",
				"groups": [`+strings.Join(quotedGroups, ",")+`]
			}
		}`)
	}
	path := filepath.Join(t.TempDir(), "identities.json")
	body := `{"version":1,"tokens":[` + strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := auth.LoadIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
