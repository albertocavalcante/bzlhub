package api

// SystemStatus is the wire shape for GET /api/v1/system/status.
//
// Drives the /status page (plan-65 v2 §Part 3) and any external
// monitoring that wants a single-shot, low-cardinality view of the
// instance. Every field is composed from state canopy already
// tracks — no invented metrics. Fields canopy cannot honestly
// compute today are omitted on the wire via omitempty rather than
// emitted as 0 / false / null with theatrical confidence.
//
// Stability: the canopy binary version (Version) IS the schema
// version. Additions are backward-compatible; renames or removals
// go through a one-minor-release deprecation cycle where both names
// are honoured with a log warning.
type SystemStatus struct {
	Version       string `json:"version"`
	Commit        string `json:"commit,omitempty"`
	BuiltAt       string `json:"built_at,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds"`

	Mirror     MirrorStatus     `json:"mirror"`
	Federation FederationStatus `json:"federation"`
	Drift      DriftStatusInfo  `json:"drift"`
	Addons     AddonsStatus     `json:"addons"`
}

// MirrorStatus is the local mirror snapshot: how much we hold and
// when we last grew it. SizeBytes is omitted (zero + omitempty) when
// the backing computation isn't wired — the mirror size walk is
// deferred per plan-65 v2 §Part 3 ("no invented metrics" rule).
type MirrorStatus struct {
	ModulesIndexed        int    `json:"modules_indexed"`
	VersionsIndexed       int    `json:"versions_indexed"`
	SizeBytes             int64  `json:"size_bytes,omitempty"`
	LastIngestAt          string `json:"last_ingest_at,omitempty"`
	PromoteOnServeEnabled bool   `json:"promote_on_serve_enabled"`
}

// FederationStatus mirrors the federation reachability snapshot
// already published by /api/v1/upstreams, reshaped for the status
// page's needs. The per-upstream record carries the LAST probe
// state — neither the polling frequency nor any rolling average is
// computed (also "no invented metrics"). Cache stats are aggregated
// from the cascade.
type FederationStatus struct {
	Upstreams []UpstreamStatus `json:"upstreams"`
}

// UpstreamStatus is one upstream's reachability + cache snapshot.
//
// CacheEntries + CacheHitRate are aggregate over the cascade (the
// cascade has one cache shared across upstreams, not per-upstream
// caches). They appear under each upstream for UI convenience; both
// values are identical across the array. CacheHitRate is a float in
// [0, 1] when there's been at least one cache lookup, otherwise 0
// (the UI distinguishes "0 lookups" from "0% hit rate" via the
// "warming" hint when CacheEntries is 0).
type UpstreamStatus struct {
	URL                string  `json:"url"`
	Reachable          bool    `json:"reachable"`
	LastProbeAt        string  `json:"last_probe_at,omitempty"`
	LastProbeLatencyMs int64   `json:"last_probe_latency_ms,omitempty"`
	LastProbeError     string  `json:"last_probe_error,omitempty"`
	CacheEntries       int     `json:"cache_entries"`
	CacheHitRate       float64 `json:"cache_hit_rate"`
}

// DriftStatusInfo is the cached drift summary across the mirror —
// populated by iterating ListModules and counting per-module drift
// summaries already persisted in the index. No upstream calls
// happen on /status — the heavy drift recompute runs on its own
// schedule (see /drift page + /api/v1/drift).
//
// LastRefreshAt is the latest LatestIngestedAt across modules
// whose drift summary is non-empty — a coarse "when did we last
// learn anything about drift?" signal. Empty when no module has a
// cached drift summary yet.
type DriftStatusInfo struct {
	LastRefreshAt         string `json:"last_refresh_at,omitempty"`
	ModulesBehind         int    `json:"modules_behind"`
	ModulesYankedUpstream int    `json:"modules_yanked_upstream"`
}

// AddonsStatus reports whether optional capabilities are enabled
// on this instance. Future-shaped — every field is "false" on the
// v0 public bzlhub.com instance because none of these capabilities
// ship yet. They appear in the contract now so adding them later
// is a value flip, not a schema change. The UI renders them as
// "all disabled" rather than hiding the section, so a reader
// reviewing the status page can see what's NOT enabled at a glance.
type AddonsStatus struct {
	PromoteOnServe     bool `json:"promote_on_serve"`
	SnapshotPublishing bool `json:"snapshot_publishing"`
	Litestream         bool `json:"litestream"`
	MCPHTTP            bool `json:"mcp_http"`
}
