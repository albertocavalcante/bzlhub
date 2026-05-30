package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/featureflags"
	"github.com/albertocavalcante/canopy/internal/server"
	"github.com/albertocavalcante/canopy/internal/store"
)

// TestSystemStatus_ShapeAndHeaders locks the wire contract for
// /api/v1/system/status — plan-65 v2 §Part 3 calls out the shape as
// "locked". The page binds to these field names; renaming any of them
// without a deprecation cycle would silently break /status.
//
// We also assert the Cache-Control: no-store header so a misbehaving
// reverse proxy can't cache the snapshot to the next visitor.
func TestSystemStatus_ShapeAndHeaders(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.New(nil, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/api/v1/system/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, body=%s", res.StatusCode, body)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store, must-revalidate" {
		t.Errorf("Cache-Control = %q, want no-store, must-revalidate", got)
	}

	var status api.SystemStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	// Field-presence check — we don't assert specific values (the
	// store is empty / version ldflags may be unset under `go test`)
	// but the structural fields MUST be there for the UI to bind.
	if status.UptimeSeconds < 0 {
		t.Errorf("uptime_seconds should be non-negative; got %d", status.UptimeSeconds)
	}
	if status.Mirror.ModulesIndexed != 0 {
		t.Errorf("empty store should report 0 modules; got %d", status.Mirror.ModulesIndexed)
	}
	if status.Federation.Upstreams == nil {
		t.Error("federation.upstreams should be the empty array, not null")
	}
	// Addons stay all-false on a default config — these are the
	// future-shaped capabilities flags.
	if status.Addons.PromoteOnServe || status.Addons.SnapshotPublishing ||
		status.Addons.Litestream || status.Addons.MCPHTTP {
		t.Errorf("addons should default all-false; got %+v", status.Addons)
	}
}

// TestSystemStatus_AddonMCPHTTPReflectsFlag asserts the /status
// addons block is wired to the live MCPHTTPEnabled flag, not a
// hard-coded false. /status is the "is this feature enabled?"
// surface; the flag flowing through is what makes the page honest.
func TestSystemStatus_AddonMCPHTTPReflectsFlag(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ts := httptest.NewServer(server.NewWithOptions(nil, canopy.New(s), nil, server.Options{
		Flags: featureflags.Flags{MCPHTTPEnabled: true},
	}))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/api/v1/system/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d body=%s", res.StatusCode, body)
	}
	var status api.SystemStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if !status.Addons.MCPHTTP {
		t.Errorf("addons.mcp_http = false, want true (flag was on); body=%s", body)
	}
}
