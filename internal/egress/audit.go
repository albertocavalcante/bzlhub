package egress

import (
	"encoding/json"
	"time"
)

// AuditEvent is one entry in the egress audit log. Operators ship
// this stream to Splunk / Loki / whatever — the JSON tags are the
// wire contract.
//
// Fields are additive: future improvements (Plan 21 §"egress log")
// will add cache_status, signature, etc. Never rename or remove
// fields without a deprecation cycle. omitempty is the discipline
// that lets us add fields without bloating denial entries.
type AuditEvent struct {
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`              // "egress" | "cdn-egress" | "cas-egress" (Plans 24, 26)
	Verb    string    `json:"verb"`              // "http-get" | "http-put" | "grpc-read" | ...
	Host    string    `json:"host"`              // hostname only; path goes in URL
	URL     string    `json:"url,omitempty"`     // full URL when present
	Actor   string    `json:"actor,omitempty"`   // canopy / sync-runner identity
	Outcome string    `json:"outcome"`           // "ok" | "denied" | "error"
	Reason  string    `json:"reason,omitempty"`  // policy-deny code, error class
	Stack   string    `json:"stack,omitempty"`   // file:line of the caller (denials only)
	BytesIn int64     `json:"bytes_in,omitempty"`

	// Duration is rendered as integer milliseconds in JSON to keep
	// downstream parsers simple. Use NewAuditEvent helpers (added
	// in commit C9) to populate.
	Duration time.Duration `json:"duration_ms,omitempty"`
}

// MarshalJSON renders Duration as milliseconds, not nanoseconds —
// json.Marshal of a time.Duration defaults to ns, which is too
// granular for log dashboards.
func (e AuditEvent) MarshalJSON() ([]byte, error) {
	type alias AuditEvent
	return json.Marshal(struct {
		alias
		DurationMS int64 `json:"duration_ms,omitempty"`
	}{
		alias:      alias(e),
		DurationMS: e.Duration.Milliseconds(),
	})
}
