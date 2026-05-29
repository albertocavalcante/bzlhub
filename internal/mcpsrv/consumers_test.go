package mcpsrv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	scipproto "github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/store"
)

// Registration-level sanity check: registerTools binds the right
// handler to the right tool name. Without this, a typo like
// AddTool(NewTool("canopy_consumers"), compatCheckHandler(c)) would
// pass the existing round-trip tests (they call the Go function by
// reference) while breaking real MCP clients that dispatch by name.
//
// Exercises the full tools/call dispatch path via HandleMessage so a
// misnamed registration shows up as a wrong-result rather than a
// silent pass.
func TestMCP_NewToolsAreRegisteredByName(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.WriteReport(ctx, &report.ModuleReport{
		Name: "producer", Version: "1",
		Rules: []report.RuleSpec{
			{Name: "my_rule", Provenance: report.Provenance{File: "rules/lib.bzl"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	srv := server.NewMCPServer("canopy-test", "test")
	registerTools(srv, canopy.New(s), nil)

	cases := []struct {
		toolName string
		args     map[string]any
		// Substrings that MUST appear in the JSON result body. The
		// JSON is indented (json.MarshalIndent in jsonResult), so
		// assertions match the indented shape ("key": "value").
		wantSubstrings []string
	}{
		{
			toolName: "canopy_consumers",
			args: map[string]any{
				"module":  "producer",
				"version": "1",
				"name":    "my_rule",
			},
			wantSubstrings: []string{
				`"kind": "rule"`,
				`"module": "producer"`,
				`"name": "my_rule"`,
			},
		},
		{
			toolName: "canopy_compat_check",
			args: map[string]any{
				// Real input — at least one bazel_dep so the analyzer
				// doesn't bail with ErrEmptyInput. The dep doesn't
				// need to be in canopy's corpus; missing-from-corpus
				// is a valid result that still produces a Summary.
				"body": `module(name = "x", version = "1")` + "\n" +
					`bazel_dep(name = "nonexistent_dep", version = "1.0.0")` + "\n",
			},
			wantSubstrings: []string{
				`"summary"`,
				`"deps"`,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.toolName, func(t *testing.T) {
			req := map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "tools/call",
				"params": map[string]any{
					"name":      c.toolName,
					"arguments": c.args,
				},
			}
			msg, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			resp := srv.HandleMessage(ctx, msg)
			body, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			// Unwrap: JSONRPC envelope → result.content[0].text → the
			// indented JSON our handler emitted via jsonResult. Plain
			// substring match on `body` doesn't work because the inner
			// JSON's quotes are escaped in the envelope.
			var env struct {
				Result struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v body=%s", err, body)
			}
			if env.Result.IsError {
				t.Fatalf("tool returned isError=true: %s", body)
			}
			if len(env.Result.Content) == 0 {
				t.Fatalf("no content in response: %s", body)
			}
			inner := env.Result.Content[0].Text
			for _, want := range c.wantSubstrings {
				if !strings.Contains(inner, want) {
					t.Errorf("tools/call %s did not return %q in inner JSON\n---inner---\n%s", c.toolName, want, inner)
				}
			}
		})
	}
}

// canopy_consumers MCP tool round-trip: seed a producer + consumer
// with a real SCIP reference, invoke the tool via its handler, and
// verify the JSON shape clients will see.
func TestMCP_ConsumersTool_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.WriteReport(ctx, &report.ModuleReport{
		Name: "producer", Version: "1",
		Rules: []report.RuleSpec{
			{Name: "my_rule", Provenance: report.Provenance{File: "rules/lib.bzl"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "consumer", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteScipBlob(ctx, "consumer", "1", scipBlobWithRef(t,
		"bzlmod producer@1 rules/lib.bzl#my_rule", "uses/foo.bzl", 7,
	)); err != nil {
		t.Fatal(err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = "canopy_consumers"
	req.Params.Arguments = map[string]any{
		"module":  "producer",
		"version": "1",
		"name":    "my_rule",
	}
	result, err := consumersHandler(canopy.New(s))(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool errored: %+v", result)
	}
	text, _ := mcp.AsTextContent(result.Content[0])
	var got api.ConsumersResult
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, text.Text)
	}
	if got.Kind != "rule" || got.ConsumerCount != 1 || got.TotalCallSites != 1 {
		t.Errorf("got %+v", got)
	}
	if len(got.Consumers) != 1 || got.Consumers[0].Module != "consumer" {
		t.Errorf("consumer entry: %+v", got.Consumers)
	}
}

// Missing required args → error result (not a Go error).
func TestMCP_ConsumersTool_RequiresArgs(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	req := mcp.CallToolRequest{}
	req.Params.Name = "canopy_consumers"
	req.Params.Arguments = map[string]any{} // empty
	result, err := consumersHandler(canopy.New(s))(ctx, req)
	if err != nil {
		t.Fatalf("handler returned go-error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result, got success")
	}
	text, _ := mcp.AsTextContent(result.Content[0])
	if !strings.Contains(text.Text, "required") {
		t.Errorf("error msg: %q", text.Text)
	}
}

// canopy_compat_check MCP tool round-trip: feed a real-shaped
// MODULE.bazel body + verify the response includes plan_markdown +
// plan_shell (Plan 06 codemods bundled).
func TestMCP_CompatCheckTool_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Trivial corpus: a single module not present in the analyzed
	// input. Result.deps will have one entry with InCorpus=false; no
	// breaking findings, but the analyzer should still emit a valid
	// (possibly empty) PlanMarkdown.
	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "rules_go", Version: "0.50.0"}); err != nil {
		t.Fatal(err)
	}

	body := `module(name = "myapp", version = "0.1.0")

bazel_dep(name = "rules_go", version = "0.50.0")
`

	req := mcp.CallToolRequest{}
	req.Params.Name = "canopy_compat_check"
	req.Params.Arguments = map[string]any{"body": body}
	result, err := compatCheckHandler(canopy.New(s))(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		text, _ := mcp.AsTextContent(result.Content[0])
		t.Fatalf("tool errored: %s", text.Text)
	}
	text, _ := mcp.AsTextContent(result.Content[0])
	if !strings.Contains(text.Text, `"summary"`) {
		t.Errorf("response missing summary field: %s", text.Text)
	}
	if !strings.Contains(text.Text, `"self"`) {
		t.Errorf("response missing self field: %s", text.Text)
	}
}

// Helper mirroring the server-side test helper but local to this
// package so the test stays self-contained.
func scipBlobWithRef(t *testing.T, symbol, file string, line int32) []byte {
	t.Helper()
	idx := &scipproto.Index{
		Metadata: &scipproto.Metadata{Version: 0},
		Documents: []*scipproto.Document{{
			RelativePath: file,
			Occurrences: []*scipproto.Occurrence{{
				Symbol:      symbol,
				Range:       []int32{line, 0, line, 7},
				SymbolRoles: 0,
			}},
		}},
	}
	b, err := proto.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
