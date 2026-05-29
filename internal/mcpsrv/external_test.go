package mcpsrv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/store"
)

// Round-trip the canopy_external_surface MCP tool: register it, invoke it
// against a seeded store, and decode the JSON payload back into the API
// response struct. Pins both the tool surface (name + required args) and
// the JSON shape clients will see.
func TestMCP_ExternalSurfaceTool_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.WriteReport(ctx, &report.ModuleReport{Name: "m", Version: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteExternalRefs(ctx, "m", "1", []store.ExternalRef{
		{URL: "https://x/y", Host: "x", Class: "unknown", Platform: "any"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	srv := server.NewMCPServer("canopy-test", "test")
	registerTools(srv, canopy.New(s), nil)

	req := mcp.CallToolRequest{}
	req.Params.Name = "canopy_external_surface"
	req.Params.Arguments = map[string]any{"module": "m", "version": "1"}

	result, err := externalSurfaceHandler(canopy.New(s))(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool errored: %+v", result)
	}

	// Pull the JSON text content out of the first result block.
	if len(result.Content) == 0 {
		t.Fatal("no content")
	}
	textContent, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		t.Fatalf("content[0] not text: %T", result.Content[0])
	}
	var got api.ExternalSurfaceResponse
	if err := json.Unmarshal([]byte(textContent.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, textContent.Text)
	}
	if got.Module != "m" || got.Version != "1" {
		t.Errorf("module/version = %q/%q", got.Module, got.Version)
	}
	if len(got.Refs) != 1 || got.Refs[0].URL != "https://x/y" {
		t.Errorf("refs = %+v", got.Refs)
	}
}

// Missing required args → tool returns an error result (not a Go error).
func TestMCP_ExternalSurfaceTool_RequiresArgs(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	req := mcp.CallToolRequest{}
	req.Params.Name = "canopy_external_surface"
	req.Params.Arguments = map[string]any{} // empty

	result, err := externalSurfaceHandler(canopy.New(s))(ctx, req)
	if err != nil {
		t.Fatalf("handler returned go-error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got success: %+v", result)
	}
	textContent, _ := mcp.AsTextContent(result.Content[0])
	if !strings.Contains(textContent.Text, "required") {
		t.Errorf("error message: %q", textContent.Text)
	}
}
