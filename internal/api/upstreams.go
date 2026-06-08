package api

import "time"

// UpstreamsResponse is the wire shape for GET /api/v1/upstreams
// (Plan 16 F3). Reports the federation backend's current state so
// operators can monitor reachability + diagnose miss patterns.
//
// MVP shape: primary + per-upstream reachability. The cache_stats and
// collisions_count/sample fields documented in plan 16 are deferred
// until Cascade tracks them (today's first-200-wins implementation
// has no cache and no collision recording; both are explicit
// follow-ups).
//
// When canopy serves a non-federated configuration (no --upstream
// flag / BZLHUB_UPSTREAMS env), Upstreams is the empty array and
// Primary reports the local backend kind. Clients can treat empty
// Upstreams as "federation disabled" without parsing the Primary
// kind explicitly.
type UpstreamsResponse struct {
	Primary    PrimaryInfo    `json:"primary"`
	Upstreams  []UpstreamInfo `json:"upstreams"`
	// CacheStats reflects the federation response cache (Plan 16
	// Layer C). Zero values when the cache is disabled (operators
	// running with `BZLHUB_UPSTREAM_CACHE_SIZE` ≤ 0) or when no
	// federation upstream has been queried yet.
	CacheStats CacheStatsInfo `json:"cache_stats"`
	// CollisionsCount is the number of distinct (module, version)
	// pairs canopy has observed in MORE than one federation source
	// — Plan 16 Layer D provenance audit. Zero when the federation
	// hasn't seen any cross-upstream collisions yet (typical for a
	// well-curated mirror + a single upstream).
	CollisionsCount int `json:"collisions_count"`
	// CollisionsSample lists up to 10 newest collision groups,
	// per Plan 16 spec. Empty array when CollisionsCount is 0.
	CollisionsSample []ModuleCollisionInfo `json:"collisions_sample"`
}

// ModuleCollisionInfo names one (module, version) that appears in
// multiple federation sources. ServedFrom is the source that wins
// the cascade (usually 'local' if locally mirrored, else the
// highest-priority upstream URL); Shadowed lists every other upstream
// that ALSO has the (module, version) but is ignored for serve.
type ModuleCollisionInfo struct {
	Module     string   `json:"module"`
	Version    string   `json:"version"`
	ServedFrom string   `json:"served_from"`
	Shadowed   []string `json:"shadowed"`
	LastSeen   string   `json:"last_seen,omitempty"`
}

// CacheStatsInfo is a point-in-time cache snapshot. Mirrors the
// backend.CacheStats wire shape without coupling api to backend.
type CacheStatsInfo struct {
	Entries int   `json:"entries"`
	Hits    int64 `json:"hits"`
	Misses  int64 `json:"misses"`
}

// PrimaryInfo identifies the cascade's primary (always-tried-first)
// backend. Today there's exactly one production primary (File);
// future S3/Postgres primaries surface here via the same shape with
// different Kind values.
type PrimaryInfo struct {
	// Kind is "local" when the primary is a filesystem backend
	// (--root <path>), "none" when canopy was started without a
	// primary, or a future backend identifier (s3/postgres/oci) as
	// canopy grows.
	Kind string `json:"kind"`
	// Root is the filesystem path when Kind=="local". Empty for
	// other kinds.
	Root string `json:"root,omitempty"`
}

// UpstreamInfo is one (upstream, last-probed-state) record.
// Reachable, LastProbe, and LastProbeLatencyMs are populated by the
// boot probe and refreshed by every cascade lookup that touches the
// upstream. LastProbeErrorMsg is empty on success.
type UpstreamInfo struct {
	URL                string    `json:"url"`
	Reachable          bool      `json:"reachable"`
	LastProbe          time.Time `json:"last_probe"`
	LastProbeLatencyMs int64     `json:"last_probe_latency_ms"`
	// LastProbeErrorMsg is the error string from the last failed
	// probe, or empty on success. Surfaced verbatim so operators can
	// diff "DNS failure" from "TLS handshake" from "HTTP 502".
	LastProbeErrorMsg string `json:"last_probe_error_msg,omitempty"`
}
