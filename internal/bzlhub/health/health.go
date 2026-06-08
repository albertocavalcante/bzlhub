// Package health derives the /status page's instant health state
// from a SystemStatus wire payload. The thresholds live here, in
// one place, so the Go side is the single source of truth — the
// UI consumes the derived state on the wire instead of redoing
// the threshold math in TypeScript.
//
// What "instant state" is and isn't
//
// `Derive` returns a one-shot snapshot of how the worst-contributing
// signal classifies the install RIGHT NOW. It does NOT smooth the
// signal over time — that's hysteresis, a render concern owned by
// the browser. A wire payload reporting `instant_state = "degraded"`
// means "at the moment the server composed this response, at least
// one signal was at or past its amber threshold". The browser's job
// is to decide whether to BELIEVE that instantly (green → amber is
// immediate) or wait for more samples (amber → red needs N).
//
// What `signals` adds
//
// Each call to Derive collects every check that tripped — not just
// the worst — into ComputedStatus.Signals. The UI uses this to
// render WHICH upstream is slow / how many modules are drifting /
// how long since the last sync, without re-reading the source
// fields and re-applying threshold math. CLI ops scripts can
// `jq '.computed.signals[] | select(.level == "unhealthy")'` to
// alert on red conditions only.
//
// Why server-derived
//
//   - Single source of truth: changing a threshold means changing
//     one constant here. Pre-refactor we had the same numbers in
//     this package, the UI's TypeScript module, and the plan-65
//     spec — three-way drift waiting to happen.
//   - CLI parity: `bzlhub status` text mode renders the verdict
//     inline ("status: degraded"); `--format=json` exposes it for
//     ops scripts so they no longer have to jq-math timestamps.
//   - Schema-bounded: thresholds are an internal contract, not a
//     wire shape. Changing them does NOT change the wire response
//     shape — consumers see different `instant_state` values, not
//     different field names.
package health

import (
	"fmt"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/api"
)

// StateLevel is one of "healthy", "degraded", "unhealthy". Exposed
// as a typed string so call sites get compile-time tag checks
// while the wire JSON stays a plain enum string.
type StateLevel string

const (
	StateHealthy   StateLevel = "healthy"
	StateDegraded  StateLevel = "degraded"
	StateUnhealthy StateLevel = "unhealthy"
)

// Signal Kinds. Part of the public wire schema — adding new Kinds
// is non-breaking; renaming or removing is. UI / ops scripts switch
// on these values, so any rename needs a deprecation cycle (emit
// both old + new for one release).
const (
	KindUpstreamUnreachable = "upstream_unreachable"
	KindUpstreamSlow        = "upstream_slow"
	KindIngestStale         = "ingest_stale"
	KindSyncStale           = "sync_stale"
	KindDriftCount          = "drift_count"
)

// Steady-state thresholds.
//
// Calibration notes
//
//   - MirrorStaleAmber / MirrorStaleRed gate content-freshness — the
//     per-module ingest into the index. A week without any module
//     moving in upstream is normal during a slow registry phase; a
//     month is suspicious. These are gut numbers, revisitable after
//     30 days of operational data (plan-65 Q56).
//   - SyncStaleAmber / SyncStaleRed gate daemon-liveness — `canopy
//     sync run`'s upstream-pull heartbeat. Typical operator interval
//     is 15–60min; 1h covers one missed cycle, 6h means the daemon
//     is almost certainly dead. Plan-65 Q58.
//   - SlowProbeMs is the per-upstream "this probe round-tripped too
//     slowly" cutoff. 500ms catches the cases where CF Tunnel or
//     a flaky network is hurting users; below that, p99 jitter
//     dominates and we'd flap on noise.
//   - DriftAmber / DriftRed count modules drifting from upstream
//     (behind OR yanked-upstream, summed). A handful is normal as
//     operators triage; tens of modules behind means an automation
//     pipeline is broken.
const (
	SlowProbeMs      = 500
	MirrorStaleAmber = 7 * 24 * time.Hour
	MirrorStaleRed   = 30 * 24 * time.Hour
	SyncStaleAmber   = 1 * time.Hour
	SyncStaleRed     = 6 * time.Hour
	DriftAmber       = 5
	DriftRed         = 20
)

// Derive returns the instant health state for a /api/v1/system/status
// payload at the given wall-clock moment.
//
// `now` is passed in (not read from time.Now) so callers can unit-
// test threshold boundaries deterministically and so the wire's
// derived state is computed against the server's request time,
// not whatever clock the renderer happens to have.
//
// Every contributing signal is collected (not short-circuited on
// worst); the InstantState is then `worseOf` over the collection.
// A healthy payload returns InstantState=healthy with an empty/nil
// Signals slice.
func Derive(s api.SystemStatus, now time.Time) api.ComputedStatus {
	var sigs []api.Signal

	// ---- Federation: one signal per affected upstream -----------
	for _, u := range s.Federation.Upstreams {
		if !u.Reachable {
			detail := fmt.Sprintf("%s unreachable", u.URL)
			if u.LastProbeError != "" {
				detail = fmt.Sprintf("%s unreachable: %s", u.URL, u.LastProbeError)
			}
			sigs = append(sigs, api.Signal{
				Kind:   KindUpstreamUnreachable,
				Level:  string(StateUnhealthy),
				Detail: detail,
			})
			continue
		}
		if u.LastProbeLatencyMs > SlowProbeMs {
			sigs = append(sigs, api.Signal{
				Kind:  KindUpstreamSlow,
				Level: string(StateDegraded),
				Detail: fmt.Sprintf("%s at %dms (> %dms)",
					u.URL, u.LastProbeLatencyMs, SlowProbeMs),
			})
		}
	}

	// ---- Mirror ingest age -------------------------------------
	if t, ok := parseRFC3339(s.Mirror.LastIngestAt); ok {
		if sig, present := ageSignal(
			KindIngestStale, now.Sub(t),
			MirrorStaleAmber, MirrorStaleRed,
			"last ingest %s ago",
		); present {
			sigs = append(sigs, sig)
		}
	}

	// ---- Mirror sync heartbeat ---------------------------------
	if t, ok := parseRFC3339(s.Mirror.LastSyncAt); ok {
		if sig, present := ageSignal(
			KindSyncStale, now.Sub(t),
			SyncStaleAmber, SyncStaleRed,
			"last sync %s ago",
		); present {
			sigs = append(sigs, sig)
		}
	}

	// ---- Drift count -------------------------------------------
	driftTotal := s.Drift.ModulesBehind + s.Drift.ModulesYankedUpstream
	if sig, present := driftSignal(driftTotal); present {
		sigs = append(sigs, sig)
	}

	return api.ComputedStatus{
		InstantState: string(levelFromSignals(sigs)),
		Signals:      sigs,
	}
}

// DeriveLocal is the CLI-side derivation. The `bzlhub status`
// command doesn't have a federation cascade running — it opens
// the store + Mirror in read-only mode and emits a snapshot. So
// the only signals it can evaluate are mirror sync staleness and
// drift count (ingest_at is also not visible to the CLI). Returns
// ComputedStatus for parity with Derive — the wire shape and the
// CLI JSON match by construction.
func DeriveLocal(driftBehind, driftYanked int, lastSync time.Time, now time.Time) api.ComputedStatus {
	var sigs []api.Signal

	if !lastSync.IsZero() {
		if sig, present := ageSignal(
			KindSyncStale, now.Sub(lastSync),
			SyncStaleAmber, SyncStaleRed,
			"last sync %s ago",
		); present {
			sigs = append(sigs, sig)
		}
	}
	if sig, present := driftSignal(driftBehind + driftYanked); present {
		sigs = append(sigs, sig)
	}

	return api.ComputedStatus{
		InstantState: string(levelFromSignals(sigs)),
		Signals:      sigs,
	}
}

// ---- internal helpers ---------------------------------------------

// ageSignal returns the Signal for an age-based check, or
// (zero, false) when the age is below the amber threshold.
// Detail uses HumanDuration so the wire string stays compact
// and operator-readable.
func ageSignal(kind string, age, amber, red time.Duration, fmtAge string) (api.Signal, bool) {
	switch {
	case age > red:
		return api.Signal{
			Kind:   kind,
			Level:  string(StateUnhealthy),
			Detail: fmt.Sprintf(fmtAge+" (> %s)", HumanDuration(age), HumanDuration(red)),
		}, true
	case age >= amber:
		return api.Signal{
			Kind:   kind,
			Level:  string(StateDegraded),
			Detail: fmt.Sprintf(fmtAge+" (>= %s)", HumanDuration(age), HumanDuration(amber)),
		}, true
	}
	return api.Signal{}, false
}

func driftSignal(total int) (api.Signal, bool) {
	switch {
	case total > DriftRed:
		return api.Signal{
			Kind:   KindDriftCount,
			Level:  string(StateUnhealthy),
			Detail: fmt.Sprintf("%d modules drifting (> %d)", total, DriftRed),
		}, true
	case total >= DriftAmber:
		return api.Signal{
			Kind:   KindDriftCount,
			Level:  string(StateDegraded),
			Detail: fmt.Sprintf("%d modules drifting (>= %d)", total, DriftAmber),
		}, true
	}
	return api.Signal{}, false
}

// levelFromSignals returns the worst level among signals, or
// StateHealthy when the slice is empty. Mirrors the pre-Signals
// worst-wins precedence exactly so the InstantState value is
// identical to what plain Derive used to return.
func levelFromSignals(sigs []api.Signal) StateLevel {
	level := StateHealthy
	for _, sig := range sigs {
		if rankString(sig.Level) > rankString(string(level)) {
			level = StateLevel(sig.Level)
		}
	}
	return level
}

func rankString(s string) int {
	switch s {
	case string(StateUnhealthy):
		return 2
	case string(StateDegraded):
		return 1
	default:
		return 0
	}
}

func parseRFC3339(iso string) (time.Time, bool) {
	if iso == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// HumanDuration renders a Duration as the largest unit (3m, 2h,
// 5d). Exported so the CLI status renderer can share the same
// format as the signal Detail strings, keeping operator-facing
// formatting consistent across the verb output and the wire
// signals[].
func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
