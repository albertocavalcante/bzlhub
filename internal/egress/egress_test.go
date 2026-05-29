package egress

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestModeStringCanonical asserts every Mode renders to a stable
// lowercase token. The audit JSONL log embeds this verbatim, so
// renames are policy changes, not refactors.
func TestModeStringCanonical(t *testing.T) {
	cases := []struct {
		m    Mode
		want string
	}{
		{ModeAllow, "allow"},
		{ModeDeny, "deny"},
		{ModeAudit, "audit"},
	}
	for _, c := range cases {
		if got := c.m.String(); got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", c.m, got, c.want)
		}
	}
}

// TestPolicyCheck_AllowMode_PassesThroughAllHosts asserts the
// permissive default: in ModeAllow with an empty allowlist, every
// host is permitted. This is the default-profile behaviour that has
// shipped since Phase 0; the egress package must not regress it.
func TestPolicyCheck_AllowMode_PassesThroughAllHosts(t *testing.T) {
	p := Policy{Mode: ModeAllow}
	req := mustReq(t, "https://github.com/anything")
	if err := p.Check(req); err != nil {
		t.Errorf("Check on ModeAllow + empty allowlist = %v; expected nil", err)
	}
}

// TestPolicyCheck_DenyMode_RefusesEverything asserts the mirror-only
// posture: ModeDeny refuses every request unconditionally, including
// requests to hosts that would otherwise be on the allowlist. Deny is
// a stronger statement than "allowlist is empty"; it is "no egress
// permitted, period." Allowlist entries are irrelevant under Deny.
func TestPolicyCheck_DenyMode_RefusesEverything(t *testing.T) {
	p := Policy{
		Mode:  ModeDeny,
		Allow: []string{"github.com", "bcr.bazel.build"}, // ignored
	}
	for _, target := range []string{
		"https://github.com/foo",
		"https://bcr.bazel.build/modules/foo",
		"https://internal.corp.example/mirror",
	} {
		req := mustReq(t, target)
		err := p.Check(req)
		if err == nil {
			t.Errorf("Check on ModeDeny for %q returned nil; expected ErrEgressForbidden", target)
			continue
		}
		if !errors.Is(err, ErrEgressForbidden) {
			t.Errorf("Check on ModeDeny for %q = %v; want ErrEgressForbidden", target, err)
		}
	}
}

// TestPolicyCheck_AllowMode_WithAllowlist asserts the sync-runner
// posture: when Allow is non-empty, only hosts ON the allowlist pass.
// Off-allowlist hosts are refused. This is the enforcement story for
// the "egress is permitted but bounded" case.
func TestPolicyCheck_AllowMode_WithAllowlist(t *testing.T) {
	p := Policy{
		Mode:  ModeAllow,
		Allow: []string{"github.com", "bcr.bazel.build"},
	}

	t.Run("on-allowlist passes", func(t *testing.T) {
		req := mustReq(t, "https://github.com/bazelbuild/foo")
		if err := p.Check(req); err != nil {
			t.Errorf("on-allowlist host: Check = %v; expected nil", err)
		}
	})

	t.Run("off-allowlist refused", func(t *testing.T) {
		req := mustReq(t, "https://evil.example/exfil")
		err := p.Check(req)
		if err == nil {
			t.Fatalf("off-allowlist host: Check returned nil; expected refusal")
		}
		if !errors.Is(err, ErrEgressForbidden) {
			t.Errorf("off-allowlist host: Check = %v; want ErrEgressForbidden", err)
		}
		if !strings.Contains(err.Error(), "evil.example") {
			t.Errorf("error %q does not name the offending host", err)
		}
	})
}

// TestPolicyCheck_AuditMode_PassesThroughAllHosts asserts audit mode
// is observationally identical to allow-with-empty-allowlist for the
// pass/fail decision. The DIFFERENCE between audit and allow is what
// happens in the audit log on every call — that's tested in
// TestNewHTTPClient_AuditMode below.
func TestPolicyCheck_AuditMode_PassesThroughAllHosts(t *testing.T) {
	p := Policy{Mode: ModeAudit}
	req := mustReq(t, "https://anything.example/")
	if err := p.Check(req); err != nil {
		t.Errorf("Check on ModeAudit = %v; expected nil", err)
	}
}

// TestNewHTTPClient_RoundTripDelegates asserts the happy path: a
// permissive client makes a real round-trip to a test server and
// returns the response. The egress.NewHTTPClient wrapper is not
// allowed to silently break the stdlib semantics callers rely on.
func TestNewHTTPClient_RoundTripDelegates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(Policy{Mode: ModeAllow})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestNewHTTPClient_DenyMode_RoundTripFails asserts the deny mode
// reaches into the transport layer: even with a real reachable
// server, the request never leaves the host. The error MUST be
// ErrEgressForbidden so callers can distinguish policy refusal from
// network failure.
func TestNewHTTPClient_DenyMode_RoundTripFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream was contacted; deny mode is supposed to refuse before the network call")
	}))
	t.Cleanup(srv.Close)

	c := NewHTTPClient(Policy{Mode: ModeDeny})
	_, err := c.Get(srv.URL)
	if err == nil {
		t.Fatal("Get returned nil error; expected ErrEgressForbidden")
	}
	if !errors.Is(err, ErrEgressForbidden) {
		t.Errorf("Get error = %v; want ErrEgressForbidden", err)
	}
}

// TestNewHTTPClientWithTransport_WrapsInner asserts the composition
// contract: the inner transport is delegated to on permit, and is
// NEVER touched on deny. This is the contract internal/fetch leans
// on — it already has its own allowlist transport, and egress must
// wrap it without altering the inner semantics.
func TestNewHTTPClientWithTransport_WrapsInner(t *testing.T) {
	var innerCalled bool
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		innerCalled = true
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
	})

	t.Run("permit path delegates to inner", func(t *testing.T) {
		innerCalled = false
		c := NewHTTPClientWithTransport(Policy{Mode: ModeAllow}, inner)
		req := mustReq(t, "https://anything.example/")
		_, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		if !innerCalled {
			t.Error("inner transport was not invoked on permit")
		}
	})

	t.Run("deny path skips inner entirely", func(t *testing.T) {
		innerCalled = false
		c := NewHTTPClientWithTransport(Policy{Mode: ModeDeny}, inner)
		req := mustReq(t, "https://anything.example/")
		_, err := c.Do(req)
		if !errors.Is(err, ErrEgressForbidden) {
			t.Errorf("Do error = %v; want ErrEgressForbidden", err)
		}
		if innerCalled {
			t.Error("inner transport was invoked on deny; policy did not gate")
		}
	})
}

// roundTripFunc lets a closure stand in for an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestAuditEvent_JSONShape locks down the wire schema. Operators ship
// the JSONL log to Splunk / Loki / whatever; schema drift breaks their
// dashboards. Add fields freely (they're additive); never rename or
// remove without a deprecation window.
func TestAuditEvent_JSONShape(t *testing.T) {
	ev := AuditEvent{
		TS:       time.Date(2026, 5, 28, 14, 23, 11, 0, time.UTC),
		Kind:     "egress",
		Verb:     "http-get",
		Host:     "github.com",
		URL:      "https://github.com/foo",
		Outcome:  "denied",
		Reason:   "egress-policy-deny",
		Stack:    "internal/bcrprobe/probe.go:142",
		BytesIn:  0,
		Duration: 12 * time.Millisecond,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, k := range []string{"ts", "kind", "verb", "host", "url", "outcome", "reason", "stack", "duration_ms"} {
		if _, ok := got[k]; !ok {
			t.Errorf("audit event JSON missing required key %q; got: %s", k, string(b))
		}
	}

	if got["outcome"] != "denied" {
		t.Errorf("outcome = %v, want denied", got["outcome"])
	}
}

// TestClient_PolicyContextRoundTrip verifies the package-level
// helper that fetches the policy-bound client for an arbitrary
// caller. Plan 22 PR 2 specifies egress.Client(ctx) *http.Client as
// the single construction point; this test guards that contract.
func TestClient_PolicyContextRoundTrip(t *testing.T) {
	ctx := WithPolicy(context.Background(), Policy{Mode: ModeAllow, Allow: []string{"x.example"}})
	c := Client(ctx)
	if c == nil {
		t.Fatal("Client returned nil; expected configured *http.Client")
	}
	// Calling Client without WithPolicy must also work, returning a
	// permissive default. Operators in unit tests should not be
	// forced to thread a policy through every helper.
	def := Client(context.Background())
	if def == nil {
		t.Fatal("Client(unbound ctx) returned nil; expected permissive default")
	}
}

// mustReq is a small helper that fabricates an http.Request without
// boilerplate; failures in setup are not the test's interest.
func mustReq(t *testing.T, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest(%q): %v", target, err)
	}
	return req
}
