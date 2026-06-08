package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	bcrmirror "github.com/albertocavalcante/go-bcr-mirror"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
	"github.com/albertocavalcante/bzlhub/internal/featureflags"
	"github.com/albertocavalcante/bzlhub/internal/server"
	"github.com/albertocavalcante/bzlhub/internal/store"
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

	ts := httptest.NewServer(server.New(nil, bzlhub.New(s), nil))
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

	ts := httptest.NewServer(server.NewWithOptions(nil, bzlhub.New(s), nil, server.Options{
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

// TestSystemStatus_PopulatesMirrorHeadAndLastSync asserts the new
// Plan 21 staleness fields surface on /api/v1/system/status when
// the bzlhub.Service is backed by a git-aware mirror. Without
// this, the /status page can't show "synced X ago" without an
// extra HTTP round-trip to the CLI's status verb.
func TestSystemStatus_PopulatesMirrorHeadAndLastSync(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Materialise a fixture remote + clone it via bcrmirror so
	// Mirror.LastSync is populated.
	remote := t.TempDir()
	repo, err := git.PlainInit(remote, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	_, _ = wt.Add("README.md")
	_, _ = wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	})

	mirrorPath := filepath.Join(t.TempDir(), "mirror")
	m := bcrmirror.New(mirrorPath, "file://"+remote)
	if _, err := m.Clone(ctx, bcrmirror.CloneOptions{Branch: "master"}); err != nil {
		t.Fatal(err)
	}

	cs := bzlhub.New(s)
	cs.UseMirror(m)

	ts := httptest.NewServer(server.New(nil, cs, nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/api/v1/system/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	var status api.SystemStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if status.Mirror.HeadSHA == "" {
		t.Errorf("status.mirror.head_sha is empty; expected the cloned HEAD; body=%s", body)
	}
	if status.Mirror.LastSyncAt == "" {
		t.Errorf("status.mirror.last_sync_at is empty; expected the Clone timestamp; body=%s", body)
	}
}

// TestSystemStatus_ComputedWireRoundTrip asserts the
// internal/canopy/health derivation reaches the wire — without
// this guard, a refactor that drops the `status.Computed =
// health.Derive(...)` call in apiStatus would silently regress
// the /status page (UI would fall back to 'unhealthy' for every
// poll because computed.instant_state would be missing) and the
// CLI verdict tag in `bzlhub status` while every unit test stayed
// green.
//
// Pins three contracts:
//
//  1. status.Computed.InstantState is populated and is one of the
//     three modeled states.
//  2. status.Computed.Signals is present (slice, possibly empty)
//     and round-trips structurally through JSON.
//  3. A FRESHLY-cloned mirror with no drift + reachable upstream
//     yields healthy + empty signals — proves we're not silently
//     emitting phantom signals for healthy installs.
func TestSystemStatus_ComputedWireRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	remote := t.TempDir()
	repo, err := git.PlainInit(remote, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	_, _ = wt.Add("README.md")
	_, _ = wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "tester", Email: "t@x", When: time.Now()},
	})

	mirrorPath := filepath.Join(t.TempDir(), "mirror")
	m := bcrmirror.New(mirrorPath, "file://"+remote)
	if _, err := m.Clone(ctx, bcrmirror.CloneOptions{Branch: "master"}); err != nil {
		t.Fatal(err)
	}

	cs := bzlhub.New(s)
	cs.UseMirror(m)

	ts := httptest.NewServer(server.New(nil, cs, nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/api/v1/system/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	var status api.SystemStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	switch status.Computed.InstantState {
	case "healthy", "degraded", "unhealthy":
		// ok
	default:
		t.Errorf("computed.instant_state = %q; want one of healthy/degraded/unhealthy; body=%s",
			status.Computed.InstantState, body)
	}

	// Fresh clone, no upstreams configured ⇒ no signals should
	// fire. If this starts producing signals it means Derive's
	// "no signal" cases regressed (e.g. empty LastIngestAt now
	// triggers a stale).
	if status.Computed.InstantState != "healthy" {
		t.Errorf("freshly-cloned + empty-drift install should be healthy; got %q (signals: %+v)",
			status.Computed.InstantState, status.Computed.Signals)
	}
	if len(status.Computed.Signals) != 0 {
		t.Errorf("expected empty signals on healthy install; got %+v", status.Computed.Signals)
	}

	// Verify the raw JSON includes the computed block (catches a
	// subtle marshaler regression where the field gets renamed or
	// omitted under omitempty by mistake).
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	if _, ok := raw["computed"]; !ok {
		t.Errorf("response JSON missing top-level `computed` key; body=%s", body)
	}
}
