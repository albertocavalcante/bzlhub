package api

import "time"

// DriftStatus is the closed enum of drift outcomes for a
// (module, version). Encoded as a stable lowercase-hyphenated
// string in JSON; the UI keys colour palette + chip rendering off
// these tokens. Renames are policy changes, not refactors.
//
// Semantics:
//   - DriftStatusUnknown: no drift source configured, or compute
//     not yet run. Default for fresh rows. UI hides the chip
//     entirely.
//   - DriftStatusInSync: local latest matches upstream latest.
//     UI hides the chip (signal-by-absence — green-state silent).
//   - DriftStatusBehind: upstream has versions newer than local
//     latest. UI renders "↑N" with the Behind count.
//   - DriftStatusYankedUpstream: local latest is in upstream's
//     yanked_versions. UI renders "⚠ yanked" — a security
//     signal, not a freshness signal.
//   - DriftStatusLocalOnly: module not present upstream (private
//     fork, internal-only). UI renders "local".
//   - DriftStatusUpstreamError: drift compute failed; treated as
//     unknown for chip rendering but persisted for the audit log.
type DriftStatus string

// Closed enum of drift statuses. Adding a value requires updating
// the UI's DriftChip palette (Plan 19 Idea A) and the canopy drift
// computer (Plan 20 git-aware drift evolution).
const (
	DriftStatusUnknown        DriftStatus = "unknown"
	DriftStatusInSync         DriftStatus = "in-sync"
	DriftStatusBehind         DriftStatus = "behind"
	DriftStatusYankedUpstream DriftStatus = "yanked-upstream"
	DriftStatusLocalOnly      DriftStatus = "local-only"
	DriftStatusUpstreamError  DriftStatus = "upstream-error"
)

// DriftSummary is the per-(module, version) drift signal projected
// onto the UI. Carried on ModuleSummary so every endpoint that
// already returns a summary surfaces drift for free — no separate
// /api/v1/drift roundtrip required for the listing page.
//
// Persisted as JSON in versions.drift_summary_json. The struct's
// zero value (Status=DriftStatusUnknown) matches the column's '{}'
// default; rows that predate the column or canopies without a
// configured drift source decode to this shape automatically.
//
// Plan 22 decision #3: kept as a struct from day one so Plan 21's
// layered staleness fields (UpstreamSHA, SyncedAt, PromotedAt,
// LocalPulledAt) land additively without a migration through every
// UI rendering site.
type DriftSummary struct {
	// Status is the categorical outcome. The UI's chip palette
	// keys off this field exclusively.
	Status DriftStatus `json:"status,omitempty"`

	// Behind is the count of upstream versions newer than the
	// local latest. Non-zero only when Status == Behind.
	Behind int `json:"behind,omitempty"`

	// LatestUpstream is the upstream's current latest version
	// string, for the hover popover ("4 newer; upstream is at
	// 1.9.0"). Empty when Status == Unknown or LocalOnly.
	LatestUpstream string `json:"latest_upstream,omitempty"`

	// ComputedAt is when this drift signal was last computed.
	// Drives the honest-staleness affordance ("as of 4h ago")
	// from Plan 21. Zero value renders as "never computed."
	// omitzero (not omitempty) — time.Time is a struct, omitempty
	// has no effect on it, and the zero-time string would bloat
	// every "no drift data" payload.
	ComputedAt time.Time `json:"computed_at,omitzero"`

	// UpstreamSHA is the HEAD commit of the local git-aware
	// Mirror at the moment this drift was computed — i.e. the
	// upstream snapshot the verdict was derived from. Empty when
	// drift was computed without a Mirror attached (legacy
	// HTTP-probe path, File backend, or rows predating the
	// git-aware drift writer). Plan 21 staleness layer.
	UpstreamSHA string `json:"upstream_sha,omitempty"`

	// SyncedAt is when the Mirror's upstream was last confirmed —
	// from bcrmirror.Mirror.LastSync at compute time. Distinct
	// from ComputedAt: the latter is the freshness of the verdict,
	// the former is the freshness of the upstream data the
	// verdict was computed from. A drift refresh between syncs
	// updates ComputedAt but leaves SyncedAt at the time of the
	// last actual upstream probe. Zero when Mirror has never
	// synced (fresh Open before any Clone or Sync).
	SyncedAt time.Time `json:"synced_at,omitzero"`
}
