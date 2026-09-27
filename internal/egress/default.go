package egress

import (
	"net/http"
	"sync"
)

// defaultRuntime is the process-wide egress posture selected by the
// composition root. bzlhub is a single-process application and every
// outbound subsystem is expected to use DefaultHTTPClient (or Client
// with a context override), so one immutable-at-runtime snapshot keeps
// the policy consistent across registry, forge, webhook, and metadata
// calls.
var defaultRuntime = struct {
	sync.RWMutex
	policy Policy
	sink   Sink
}{
	policy: Policy{Mode: ModeAllow},
	sink:   NopSink{},
}

// ConfigureDefault installs the process-wide policy and audit sink.
// Call it once, before constructing any outbound client. Policy slices
// are copied so later configuration mutation cannot change live
// enforcement behind the caller's back.
func ConfigureDefault(p Policy, sink Sink) {
	if sink == nil {
		sink = NopSink{}
	}
	p.Allow = append([]string(nil), p.Allow...)
	defaultRuntime.Lock()
	defaultRuntime.policy = p
	defaultRuntime.sink = sink
	defaultRuntime.Unlock()
}

func defaultSnapshot() (Policy, Sink) {
	defaultRuntime.RLock()
	defer defaultRuntime.RUnlock()
	p := defaultRuntime.policy
	p.Allow = append([]string(nil), p.Allow...)
	return p, defaultRuntime.sink
}

// DefaultHTTPClient returns a client using the process-wide policy.
func DefaultHTTPClient() *http.Client {
	p, sink := defaultSnapshot()
	return NewHTTPClient(p, WithSink(sink))
}

// DefaultHTTPClientWithTransport is DefaultHTTPClient composed around
// an existing transport such as fetch's redirect-aware host allowlist.
func DefaultHTTPClientWithTransport(inner http.RoundTripper) *http.Client {
	p, sink := defaultSnapshot()
	return NewHTTPClientWithTransport(p, inner, WithSink(sink))
}
