package egress

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Default per-request timeout for clients constructed by this
// package. Canopy callers that need a different timeout should
// override on the returned client; the default is "long enough for
// a slow public registry, short enough to fail loud."
const defaultTimeout = 30 * time.Second

// ClientOption configures a policy-bound HTTP client. Used variadic
// so existing call sites that pass only Policy keep compiling.
type ClientOption func(*policyTransport)

// WithSink attaches an audit Sink to the transport. When configured,
// the sink receives one event per audit-worthy round-trip: every
// denial (regardless of mode) and every successful request when the
// policy mode is ModeAudit. ModeAllow without an explicit denial is
// silent — no log spam under the default posture.
func WithSink(s Sink) ClientOption {
	return func(t *policyTransport) {
		if s != nil {
			t.sink = s
		}
	}
}

// NewHTTPClient returns a *http.Client whose RoundTripper consults
// the supplied policy before every outbound request. This is the
// only sanctioned way to construct an HTTP client inside canopy;
// the lint check in lint_test.go prevents callers from rolling
// their own.
func NewHTTPClient(p Policy, opts ...ClientOption) *http.Client {
	return NewHTTPClientWithTransport(p, http.DefaultTransport, opts...)
}

// NewHTTPClientWithTransport is the composable variant: wraps an
// arbitrary inner RoundTripper with the policy check. Callers that
// already have a transport (e.g. host-allowlist transport in
// internal/fetch, custom retry-aware transport in future MCP
// clients) use this to keep their inner-transport semantics while
// still gating egress through the policy.
//
// The policy check ALWAYS runs before the inner transport. If the
// policy rejects a request, the inner transport is never called —
// even denied attempts cost no work past the URL parse.
func NewHTTPClientWithTransport(p Policy, inner http.RoundTripper, opts ...ClientOption) *http.Client {
	if inner == nil {
		inner = http.DefaultTransport
	}
	t := &policyTransport{
		policy: p,
		inner:  inner,
		sink:   NopSink{},
	}
	for _, opt := range opts {
		opt(t)
	}
	return &http.Client{
		Transport: t,
		Timeout:   defaultTimeout,
	}
}

// policyTransport gates outbound HTTP through the policy and emits
// audit events to the configured sink. Three emission rules:
//
//  1. Policy denial → emit one "denied" event with reason=
//     egress-policy-deny and the caller's file:line in stack.
//     ALWAYS emitted, regardless of mode (sink decides whether to
//     persist; NopSink discards).
//  2. ModeAudit + permitted round-trip → emit one event with
//     outcome=ok (success) or outcome=error (inner transport
//     failure). The sync-runner posture.
//  3. ModeAllow + permitted round-trip → SILENT. No event. The
//     default-profile posture; canopy must not generate audit-log
//     spam when egress is unconstrained.
type policyTransport struct {
	policy Policy
	inner  http.RoundTripper
	sink   Sink
}

func (t *policyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.policy.Check(req); err != nil {
		t.emitDenial(req, err)
		return nil, err
	}

	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	dur := time.Since(start)

	if t.policy.Mode == ModeAudit {
		t.emitOutcome(req, resp, err, dur)
	}
	return resp, err
}

// emitDenial records a policy-deny event. The stack field is the
// caller's file:line — what allows an operator to trace a denied
// attempt back to the code that tried it. Skipping past stdlib
// net/http frames and our own egress frames so the first
// user-relevant frame is reported.
func (t *policyTransport) emitDenial(req *http.Request, err error) {
	if _, isNop := t.sink.(NopSink); isNop {
		return
	}
	t.sink.Emit(AuditEvent{
		TS:      time.Now().UTC(),
		Kind:    "egress",
		Verb:    httpVerb(req),
		Host:    req.URL.Hostname(),
		URL:     redactURL(req.URL.String()),
		Outcome: "denied",
		Reason:  denialReason(err),
		Stack:   callerStack(),
	})
}

// emitOutcome records a permitted round-trip. Captures the inner
// transport's success (outcome=ok) or failure (outcome=error) and
// the wall-clock duration. Bytes-in is not populated here because
// resp.Body has not been consumed yet; future improvements can wrap
// the body with a counting reader if operators need byte-level
// accounting in the audit log.
func (t *policyTransport) emitOutcome(req *http.Request, resp *http.Response, err error, dur time.Duration) {
	if _, isNop := t.sink.(NopSink); isNop {
		return
	}
	ev := AuditEvent{
		TS:       time.Now().UTC(),
		Kind:     "egress",
		Verb:     httpVerb(req),
		Host:     req.URL.Hostname(),
		URL:      redactURL(req.URL.String()),
		Duration: dur,
	}
	if err != nil {
		ev.Outcome = "error"
		ev.Reason = err.Error()
	} else {
		ev.Outcome = "ok"
		if resp != nil {
			ev.BytesIn = resp.ContentLength
		}
	}
	t.sink.Emit(ev)
}

// httpVerb encodes the HTTP method into the audit-log verb taxonomy
// (`http-get`, `http-put`, etc.). Lowercased + prefixed so future
// non-HTTP transports (cas-egress in Plan 26) can coexist with a
// distinct prefix.
func httpVerb(req *http.Request) string {
	if req == nil {
		return "http-unknown"
	}
	return "http-" + strings.ToLower(req.Method)
}

// denialReason extracts the canonical reason code from a policy
// denial error. Plan 21 wire schema says reason=egress-policy-deny
// for any policy refusal; the variable-text suffix (the failing
// hostname etc.) lives in the URL/Host fields, not the reason code.
func denialReason(err error) string {
	if errors.Is(err, ErrEgressForbidden) {
		return reasonEgressPolicyDeny
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// redactURL strips userinfo from the URL before logging. Audit logs
// ship to many places; embedded credentials must never leak. The
// rest of the URL (host + path + query) is preserved because
// operators need it to identify which call site failed.
func redactURL(u string) string {
	// Cheap string-level scrub: anything between "://" and the next
	// "@" is userinfo. Only one such window per URL.
	scheme := strings.Index(u, "://")
	if scheme < 0 {
		return u
	}
	at := strings.Index(u[scheme+3:], "@")
	if at < 0 {
		return u
	}
	return u[:scheme+3] + u[scheme+3+at+1:]
}

// callerStack walks the call stack and returns the first frame that
// is neither in stdlib net/http nor in this egress package. That's
// the line of canopy code that initiated the request — the only
// useful information for "who tried to egress?" diagnostics.
//
// Returns an empty string if no frame is found (highly unusual; the
// audit log will simply omit the field).
func callerStack() string {
	pc := make([]uintptr, 32)
	n := runtime.Callers(2, pc)
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pc[:n])
	for {
		f, more := frames.Next()
		if !isFrameSkipped(f.Function) {
			// Render as file:line; full file path so a
			// future "click to source" affordance can resolve.
			return shortFile(f.File) + ":" + intStr(f.Line)
		}
		if !more {
			break
		}
	}
	return ""
}

// isFrameSkipped returns true for stack frames that belong to
// stdlib net/http internals or to the egress package itself. These
// frames are infrastructure; the audit log wants the user-relevant
// frame above them.
func isFrameSkipped(name string) bool {
	if strings.HasPrefix(name, "net/http") {
		return true
	}
	if strings.Contains(name, "/canopy/internal/egress") {
		return true
	}
	return false
}

// shortFile renders an absolute file path as a repo-relative one
// when possible. Cheap string trim against the canopy module path;
// falls back to basename when no module-prefix match is found.
func shortFile(path string) string {
	const marker = "/canopy/"
	if i := strings.LastIndex(path, marker); i >= 0 {
		return path[i+len(marker):]
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// intStr is a tiny stdlib-only int formatter. Avoids pulling fmt
// into the hot path of every audit emission.
func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// policyContextKey is unexported; callers reach the policy through
// WithPolicy / Client only. Prevents the key being collided on by
// any other package that uses string-typed context keys.
type policyContextKey struct{}

// WithPolicy returns a derived context carrying the egress policy.
// Callers downstream pull the policy via Client(ctx).
func WithPolicy(parent context.Context, p Policy) context.Context {
	return context.WithValue(parent, policyContextKey{}, p)
}

// PolicyFromContext returns the policy bound to ctx, or the
// permissive default if none is bound. Useful for tests and for
// callers that want to inspect the active policy without
// constructing a full client.
func PolicyFromContext(ctx context.Context) Policy {
	if ctx == nil {
		return Policy{Mode: ModeAllow}
	}
	if p, ok := ctx.Value(policyContextKey{}).(Policy); ok {
		return p
	}
	return Policy{Mode: ModeAllow}
}

// Client returns a *http.Client wired with the policy from ctx.
// When ctx has no bound policy, returns a permissive default —
// unit tests do not need to thread WithPolicy through every
// helper they call.
//
// This is the entry point called from every refactored HTTP caller
// in commits C5–C8 (Plan 28). Production wiring binds the policy
// once at startup in cmd/bzlhub/serve.go.
func Client(ctx context.Context) *http.Client {
	return NewHTTPClient(PolicyFromContext(ctx))
}
