package api

import (
	"context"
	"time"
)

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

	// Computed carries server-derived signals shaped for the UI's
	// instant-state pill and the `bzlhub status` CLI verdict. The
	// SOURCE fields above are the inputs; Computed is the
	// canonical conclusion. Populating happens in
	// internal/canopy/health.Derive — see plan-65 §State rules.
	Computed ComputedStatus `json:"computed"`
}

// ComputedStatus is the wire shape for server-derived health
// signals. It lives next to the source fields so a JSON reader
// sees inputs and conclusion together. Extending this block
// (e.g., per-signal breakdown) is additive — additional fields
// can be added with omitempty without breaking existing clients.
type ComputedStatus struct {
	// InstantState is "healthy", "degraded", or "unhealthy" —
	// the worst-contributing signal classification at request
	// time. The browser applies hysteresis on top to smooth
	// flapping (a render concern), but the WIRE state is what
	// the server believes RIGHT NOW.
	InstantState string `json:"instant_state"`

	// Signals is the unordered list of every threshold check
	// that contributed to a non-healthy InstantState. Each
	// signal carries its own Level, so a UI rendering a
	// breakdown can colour individual rows without re-deriving;
	// CLI ops scripts can pipe `.signals[] | select(.level ==
	// "unhealthy")` to alert on red conditions only. Empty (or
	// omitted) when InstantState is "healthy" — no contributing
	// signals to report.
	Signals []Signal `json:"signals,omitempty"`
}

// Signal is one contributing reason behind ComputedStatus.InstantState.
//
// Stability: Kind values are part of the public schema (UI and
// ops scripts switch on them); adding new Kinds is non-breaking,
// renaming or removing is. Level mirrors StateLevel ("degraded"
// or "unhealthy" — never "healthy" since healthy signals
// wouldn't appear in this list). Detail is operator-facing
// prose explaining the specific value that tripped the
// threshold, suitable for direct display.
type Signal struct {
	Kind   string `json:"kind"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
}

// MirrorHeader is an optional interface canopy implementations may
// satisfy to surface their Mirror's HEAD + LastSync on
// /api/v1/system/status. Decoupled from api.Canopy so mock
// implementations and File-backed deployments don't need to wire
// anything Mirror-shaped.
type MirrorHeader interface {
	MirrorHead(ctx context.Context) (sha string, lastSync time.Time)
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

	// HeadSHA is the BCR clone's current HEAD. Empty for
	// File-backed installs.
	HeadSHA string `json:"head_sha,omitempty"`

	// LastSyncAt is the RFC3339 timestamp of the Mirror's last
	// upstream contact, distinct from LastIngestAt (which is
	// per-module ingest into canopy's index, not the upstream
	// pull).
	LastSyncAt string `json:"last_sync_at,omitempty"`
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
