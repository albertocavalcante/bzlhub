package egress

import (
	"encoding/json"
	"io"
	"sync"
)

// Sink receives audit events from the egress transport. Emission is
// synchronous and best-effort; implementations that need durability
// (file rollover, network shipping, retry-on-failure) handle that
// themselves. The contract is "called once per audit-worthy event,
// from the goroutine that performed the round-trip" — Emit MUST be
// safe to call concurrently.
//
// Plan 21 §"egress audit log" defines the JSONL wire format;
// JSONLSink below implements it.
type Sink interface {
	Emit(AuditEvent)
}

// NopSink discards events. Used when no audit sink is configured
// (default profile, dev workstations). Cheap; the policyTransport
// short-circuits Emit calls on NopSink by checking the type.
type NopSink struct{}

// Emit satisfies Sink. Intentionally empty.
func (NopSink) Emit(AuditEvent) {}

// JSONLSink writes one JSON object per line to an io.Writer.
// Wraps writes in a mutex so concurrent RoundTrip calls don't
// interleave bytes (which would produce malformed JSON and break
// downstream parsers).
type JSONLSink struct {
	w  io.Writer
	mu sync.Mutex
}

// NewJSONLSink returns a sink that appends to w. Caller manages
// the writer's lifecycle (file rotation, close, etc.).
func NewJSONLSink(w io.Writer) *JSONLSink {
	return &JSONLSink{w: w}
}

// Emit serialises one event as a single-line JSON object followed
// by '\n'. Errors are silently dropped — the audit log should never
// be the reason canopy fails a request. Operators monitor the log's
// size and rotation freshness as the staleness signal.
func (s *JSONLSink) Emit(ev AuditEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(b)
	_, _ = s.w.Write([]byte("\n"))
}
