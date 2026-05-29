package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/api/paths"
	"github.com/albertocavalcante/canopy/internal/backend"
	"github.com/albertocavalcante/canopy/internal/canopy"
	"github.com/albertocavalcante/canopy/internal/server"
	"github.com/albertocavalcante/canopy/internal/store"
)

// Plan 16 F3 — /api/v1/upstreams reports federation state.

// Non-federated config: File backend alone. Primary is reported as
// "local" with Root; Upstreams is the empty array (NOT nil — clients
// distinguish "no upstreams configured" from "field missing" by
// presence; serializing as [] gives a stable wire shape).
func TestUpstreams_NoFederationReportsLocalPrimary(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bk := backend.NewFile("/tmp/canopy-test")
	ts := httptest.NewServer(server.New(bk, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.Upstreams())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d body=%s", res.StatusCode, body)
	}
	var got api.UpstreamsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if got.Primary.Kind != "local" {
		t.Errorf("primary.kind = %q, want local", got.Primary.Kind)
	}
	if got.Primary.Root != "/tmp/canopy-test" {
		t.Errorf("primary.root = %q, want /tmp/canopy-test", got.Primary.Root)
	}
	if len(got.Upstreams) != 0 {
		t.Errorf("upstreams = %d, want 0 (non-federated)", len(got.Upstreams))
	}
	// Non-federated: no cache exists → zeros across the board.
	if got.CacheStats.Entries != 0 || got.CacheStats.Hits != 0 || got.CacheStats.Misses != 0 {
		t.Errorf("non-federated cache_stats = %+v, want zeros", got.CacheStats)
	}
	// Stable wire shape: the field must be a serialized empty array,
	// not `null`. Plan 16 spec says clients distinguish "no upstreams"
	// from "field absent" by presence.
	// writeJSON pretty-prints, so the empty array appears as
	// `"upstreams": []` (with space after the colon). The point of
	// the assertion is to catch a regression where the field went to
	// `null`; either form is fine for that.
	if !strings.Contains(string(body), `"upstreams": []`) {
		t.Errorf("expected serialized empty array, got: %s", body)
	}
}

// Federated config: Cascade wrapping a File primary + two probe-only
// upstream stubs. Each upstream's last_probe state surfaces in the
// response.
func TestUpstreams_FederationReportsEachUpstream(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Two stub upstream registries. One returns 200 to a probe (reachable);
	// the other returns 502 (TCP layer succeeded → still reachable per
	// the cascade's intentional design — see cascade_test.go).
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(good.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)

	primary := backend.NewFile("/tmp/canopy-test")
	cascade, err := backend.NewCascade(backend.CascadeConfig{
		Primary: primary,
		Upstreams: []*backend.Upstream{
			{URL: good.URL},
			{URL: bad.URL},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Run the boot probes so the reachability snapshot is populated
	// before the endpoint reads it.
	for _, up := range cascade.Upstreams() {
		_ = cascade.ProbeUpstream(ctx, up) // ignore err — we want the side-effect on Upstream.mu
	}

	ts := httptest.NewServer(server.New(cascade, canopy.New(s), nil))
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + paths.Upstreams())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var got api.UpstreamsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}

	// Primary kind is still "local" — Cascade introspection unwraps
	// the primary backend.
	if got.Primary.Kind != "local" {
		t.Errorf("primary.kind = %q, want local (cascade unwraps primary)", got.Primary.Kind)
	}
	if got.Primary.Root != "/tmp/canopy-test" {
		t.Errorf("primary.root = %q, want /tmp/canopy-test", got.Primary.Root)
	}
	if len(got.Upstreams) != 2 {
		t.Fatalf("upstreams = %d, want 2", len(got.Upstreams))
	}
	// Order preserved from the cascade's configured order.
	if got.Upstreams[0].URL != good.URL || got.Upstreams[1].URL != bad.URL {
		t.Errorf("upstream order: got %s,%s want %s,%s",
			got.Upstreams[0].URL, got.Upstreams[1].URL, good.URL, bad.URL)
	}
	// Good upstream: 200 probe → reachable; no error msg.
	if !got.Upstreams[0].Reachable {
		t.Error("good upstream should be reachable after 200 probe")
	}
	if got.Upstreams[0].LastProbeErrorMsg != "" {
		t.Errorf("good upstream error msg = %q, want empty", got.Upstreams[0].LastProbeErrorMsg)
	}
	if got.Upstreams[0].LastProbe.IsZero() {
		t.Error("good upstream last_probe should be set")
	}
	// Bad upstream: 502 → ProbeUpstream marks it unreachable
	// (boot-probe semantics: 5xx is transient/unreachable, distinct
	// from the cascade lookup which treats 5xx as TCP-reachable).
	if got.Upstreams[1].Reachable {
		t.Error("bad upstream should be unreachable after 5xx boot probe")
	}
	if got.Upstreams[1].LastProbeErrorMsg == "" {
		t.Error("bad upstream should carry an error message")
	}
	// Cache stats are valid wire-shape (entries+hits+misses present,
	// non-negative). The boot probes go through ProbeUpstream which
	// doesn't touch the response cache, so we can't assert specific
	// counts here — just that the field is populated correctly.
	if got.CacheStats.Hits < 0 || got.CacheStats.Misses < 0 || got.CacheStats.Entries < 0 {
		t.Errorf("cache_stats has negative field: %+v", got.CacheStats)
	}
}

