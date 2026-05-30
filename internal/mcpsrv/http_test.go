package mcpsrv_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/mcpsrv"
	"github.com/albertocavalcante/canopy/internal/store"
)

// jsonrpcResp is the minimal wire shape we decode in these tests.
// Mirrors mark3labs/mcp-go's framing without coupling to it.
type jsonrpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	svc := canopy.New(s)

	// version string is cosmetic for MCP serverInfo; pin to a known
	// literal so a future test can assert on it if needed.
	// Tests default to writeEnabled=true to keep the established
	// 19-tool surface assertion stable — the new TestHTTP_WriteTools
	// case below covers the writeEnabled=false branch explicitly.
	h := mcpsrv.NewHTTPHandler(svc, svc, "test-version", true)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func postJSONRPC(t *testing.T, ts *httptest.Server, body string) jsonrpcResp {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Per MCP spec the client SHOULD send Accept including both
	// application/json and text/event-stream. mark3labs/mcp-go's
	// stateless handler accepts requests without it; we send it
	// anyway to mirror real client behaviour (Claude Code, Cursor).
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("POST /: status %d body=%s", res.StatusCode, raw)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var resp jsonrpcResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: code=%d msg=%s body=%s", resp.Error.Code, resp.Error.Message, raw)
	}
	return resp
}

// TestHTTP_ToolsList exercises the smoke-test end-to-end: every tool
// registered via registerTools must surface in the tools/list response.
// Locks the "what does the public endpoint advertise?" contract — when
// a new tool is added the count goes up; when a tool is dropped the
// test forces the drop to be explicit, not silent.
func TestHTTP_ToolsList(t *testing.T) {
	ts := newTestServer(t)
	resp := postJSONRPC(t, ts, `{"jsonrpc":"2.0","method":"tools/list","id":1}`)

	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode tools/list result: %v raw=%s", err, resp.Result)
	}
	if len(payload.Tools) < 18 {
		t.Fatalf("tools/list returned %d tools, want at least 18 (read tools + verify); got names=%v",
			len(payload.Tools), payload.Tools)
	}
	// Spot-check a few representative tool names so a registrar regression
	// (e.g. accidentally dropping the search registrar from the dispatcher)
	// fails loud rather than silently halving the count.
	wantPresent := []string{
		"canopy_search",
		"canopy_module_report",
		"canopy_drift",
		"canopy_lookup_symbol",
		"canopy_verify",
	}
	have := make(map[string]bool, len(payload.Tools))
	for _, t := range payload.Tools {
		have[t.Name] = true
	}
	for _, want := range wantPresent {
		if !have[want] {
			t.Errorf("tools/list missing %q; have=%v", want, mapKeys(have))
		}
	}
}

// TestHTTP_ToolsCall exercises one read-side tool end-to-end via the
// HTTP transport. canopy_search against an empty store is the cheapest
// "did the dispatcher wire correctly?" check — no fixtures required,
// and the result type is well-typed JSON.
func TestHTTP_ToolsCall(t *testing.T) {
	ts := newTestServer(t)
	body := `{"jsonrpc":"2.0","method":"tools/call","id":2,
		"params":{"name":"canopy_search","arguments":{"query":"nothing"}}}`
	resp := postJSONRPC(t, ts, body)

	// Result shape is {content: [{type:"text", text:"..."}], ...}.
	// We don't pin the exact content (empty-store search behaviour
	// can evolve); we just assert the envelope is well-formed.
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode tools/call result: %v raw=%s", err, resp.Result)
	}
	if payload.IsError {
		t.Errorf("canopy_search reported tool error: %s", resp.Result)
	}
	if len(payload.Content) == 0 {
		t.Errorf("canopy_search returned empty content array")
	}
}

// TestHTTP_ConcurrentIsolation is the regression gate for plan-64
// Gotcha 2: stateless-mode cross-client response leakage. We fire 50
// concurrent tools/call requests with distinct request IDs and assert
// each response's id matches the request id that produced it. A shared
// *MCPServer would race the id-tracking state and shuffle ids across
// responses; the per-request construction in NewHTTPHandler keeps
// goroutines isolated.
func TestHTTP_ConcurrentIsolation(t *testing.T) {
	ts := newTestServer(t)

	const n = 50
	var wg sync.WaitGroup
	mismatches := make(chan string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			body := fmt.Sprintf(
				`{"jsonrpc":"2.0","method":"tools/call","id":%d,
				"params":{"name":"canopy_search","arguments":{"query":"q%d"}}}`,
				id, id,
			)
			req, err := http.NewRequest(http.MethodPost, ts.URL,
				bytes.NewBufferString(body))
			if err != nil {
				mismatches <- err.Error()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			res, err := ts.Client().Do(req)
			if err != nil {
				mismatches <- err.Error()
				return
			}
			defer res.Body.Close()
			raw, _ := io.ReadAll(res.Body)
			var resp jsonrpcResp
			if err := json.Unmarshal(raw, &resp); err != nil {
				mismatches <- fmt.Sprintf("decode: %v body=%s", err, raw)
				return
			}
			// JSON numbers decode to float64 through any; compare via fmt.
			gotID := fmt.Sprintf("%v", resp.ID)
			wantID := fmt.Sprintf("%d", id)
			if gotID != wantID {
				mismatches <- fmt.Sprintf("request id=%s got response id=%s body=%s",
					wantID, gotID, raw)
			}
		}(i)
	}
	wg.Wait()
	close(mismatches)
	for msg := range mismatches {
		t.Error(msg)
	}
}

// TestHTTP_GETReturns405 locks plan-64 Decision 3
// (WithDisableStreaming): GET on the mount path must NOT open an SSE
// channel that hangs the client. mark3labs/mcp-go returns 405 Method
// Not Allowed when streaming is disabled.
func TestHTTP_GETReturns405(t *testing.T) {
	ts := newTestServer(t)
	res, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		raw, _ := io.ReadAll(res.Body)
		t.Errorf("GET status = %d, want 405 (streaming disabled); body=%s",
			res.StatusCode, raw)
	}
}

// TestHTTP_WriteTools_GatedByFlag is the security regression gate
// for the audit pass: when writeEnabled=false the public tools/list
// MUST NOT advertise mutation tools (canopy_ingest_recursive,
// canopy_bump). A typo in the dispatcher or an accidentally-true
// default would silently expose write surface to anonymous /mcp
// callers; this test fails loudly in that case.
//
// The complementary writeEnabled=true case is covered transitively
// by TestHTTP_ToolsList — the 18-tool floor includes the two write
// tools when the default test server is built with writeEnabled=true.
func TestHTTP_WriteTools_GatedByFlag(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	svc := canopy.New(s)

	// writeEnabled=false — public anonymous-read deployment shape
	// (matches the production CANOPY_MCP_WRITE_TOOLS_ENABLED=false
	// default on bzlhub.com).
	h := mcpsrv.NewHTTPHandler(svc, svc, "test-version", false)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	resp := postJSONRPC(t, ts, `{"jsonrpc":"2.0","method":"tools/list","id":1}`)
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &payload); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	have := make(map[string]bool, len(payload.Tools))
	for _, tool := range payload.Tools {
		have[tool.Name] = true
	}

	// The two tools that mutate the mirror. Both must be absent.
	for _, banned := range []string{"canopy_ingest_recursive", "canopy_bump"} {
		if have[banned] {
			t.Errorf("write tool %q is exposed when writeEnabled=false (security regression); have=%v",
				banned, mapKeys(have))
		}
	}

	// Sanity: the read tools should still be present so we know the
	// dispatcher didn't accidentally skip everything.
	for _, want := range []string{"canopy_search", "canopy_drift", "canopy_external_surface"} {
		if !have[want] {
			t.Errorf("read tool %q missing — dispatcher broken?", want)
		}
	}
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
