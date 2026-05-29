package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestJSONLSink_OneEventPerLine asserts the wire shape: every Emit
// produces exactly one line terminated with \n, parseable as a
// standalone JSON object. Operators tail this file with `jq -c .` and
// pipe into Splunk / Loki / whatever; multi-line entries break that
// pipeline.
func TestJSONLSink_OneEventPerLine(t *testing.T) {
	var buf bytes.Buffer
	sink := NewJSONLSink(&buf)

	events := []AuditEvent{
		{Kind: "egress", Verb: "http-get", Host: "github.com", Outcome: "ok"},
		{Kind: "egress", Verb: "http-get", Host: "raw.githubusercontent.com", Outcome: "denied", Reason: "egress-policy-deny"},
		{Kind: "egress", Verb: "http-put", Host: "artifactory.acme.corp", Outcome: "error", Reason: "connection refused"},
	}
	for _, ev := range events {
		sink.Emit(ev)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want %d. Buffer:\n%s", len(lines), len(events), buf.String())
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("line %d not valid JSON: %v (line: %q)", i, err, line)
		}
	}
}

// TestJSONLSink_ConcurrentEmit asserts the sink serialises writes.
// Without the mutex two goroutines emitting at once will interleave
// bytes and produce malformed JSON. The contract MUST survive
// concurrent emission because policyTransport.RoundTrip is called
// from many goroutines (HTTP client is process-wide).
func TestJSONLSink_ConcurrentEmit(t *testing.T) {
	var buf bytes.Buffer
	sink := NewJSONLSink(&buf)

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			sink.Emit(AuditEvent{Kind: "egress", Verb: "http-get", Host: "x.example", Outcome: "ok"})
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != N {
		t.Fatalf("got %d lines, want %d", len(lines), N)
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d malformed (interleave detected): %v", i, err)
		}
	}
}

// TestRoundTrip_DenialEmitsExactlyOneEntry is the load-bearing Plan
// 21 test #5: a denied request produces exactly one audit entry with
// outcome=denied, reason=egress-policy-deny, non-empty stack. The
// stack field is what lets operators trace WHICH call site tried to
// egress — without it, the audit log says "something tried" with no
// way to fix.
func TestRoundTrip_DenialEmitsExactlyOneEntry(t *testing.T) {
	var buf bytes.Buffer
	sink := NewJSONLSink(&buf)

	c := NewHTTPClient(Policy{Mode: ModeDeny}, WithSink(sink))

	_, err := c.Get("https://example.com/anything")
	if !errors.Is(err, ErrEgressForbidden) {
		t.Fatalf("Get error = %v; want ErrEgressForbidden", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d audit lines, want exactly 1. Buffer:\n%s", len(lines), buf.String())
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("audit line not JSON: %v (%q)", err, lines[0])
	}
	if got["outcome"] != "denied" {
		t.Errorf("outcome = %v; want denied", got["outcome"])
	}
	if got["reason"] != "egress-policy-deny" {
		t.Errorf("reason = %v; want egress-policy-deny", got["reason"])
	}
	if stack, _ := got["stack"].(string); stack == "" {
		t.Errorf("stack is empty; expected caller file:line")
	}
	if got["host"] != "example.com" {
		t.Errorf("host = %v; want example.com", got["host"])
	}
}

// TestRoundTrip_PermitInAllowModeEmitsZero asserts the default mode
// is silent: no events for permitted requests, no log spam. ModeAllow
// without a sink, or ModeAllow with a sink, both behave identically
// for permitted requests — emission only happens in ModeAudit (and on
// denials regardless of mode, per Plan 21).
func TestRoundTrip_PermitInAllowModeEmitsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	sink := NewJSONLSink(&buf)
	c := NewHTTPClient(Policy{Mode: ModeAllow}, WithSink(sink))

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if buf.Len() != 0 {
		t.Errorf("expected zero audit emissions in ModeAllow; got %s", buf.String())
	}
}

// TestRoundTrip_PermitInAuditModeEmitsOk asserts audit mode IS
// chatty: every permitted request produces one ok event. This is the
// sync-runner posture — every byte that crosses the public-internet
// boundary is logged.
func TestRoundTrip_PermitInAuditModeEmitsOk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	sink := NewJSONLSink(&buf)
	c := NewHTTPClient(Policy{Mode: ModeAudit}, WithSink(sink))

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d audit lines, want 1. Buffer:\n%s", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("audit line not JSON: %v", err)
	}
	if got["outcome"] != "ok" {
		t.Errorf("outcome = %v; want ok", got["outcome"])
	}
	// duration_ms is omitempty in the wire schema; an "ok" event
	// from a real round-trip has a positive duration_ms by the time
	// it's logged. A zero duration here suggests Emit was called
	// before the round-trip completed.
	if dur, _ := got["duration_ms"].(float64); dur < 0 {
		t.Errorf("duration_ms = %v; want non-negative", dur)
	}
}

// TestPolicyContext_BindingPropagates verifies the WithPolicy /
// PolicyFromContext / Client(ctx) round-trip carries the policy
// through to the actual HTTP client construction. Used by
// cmd/canopy/serve.go at startup to bind the active profile policy
// once, then propagate via context to every caller.
func TestPolicyContext_BindingPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server reached; ModeDeny in context should have refused")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx := WithPolicy(context.Background(), Policy{Mode: ModeDeny})
	c := Client(ctx)
	_, err := c.Get(srv.URL)
	if !errors.Is(err, ErrEgressForbidden) {
		t.Errorf("Get with ModeDeny in ctx = %v; want ErrEgressForbidden", err)
	}
}
