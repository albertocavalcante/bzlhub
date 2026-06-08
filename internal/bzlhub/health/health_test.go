package health_test

import (
	"strings"
	"testing"
	"time"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub/health"
)

// now is the fixed wall-clock reference every test computes
// against.
var now = time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)

func ago(d time.Duration) string {
	return now.Add(-d).Format(time.RFC3339)
}

// healthyPayload returns a fully-green SystemStatus. Tests start
// from this and selectively unhappy-path one field at a time so
// each threshold's contribution is isolated.
func healthyPayload() api.SystemStatus {
	return api.SystemStatus{
		Version:       "v0.0.0-test",
		UptimeSeconds: 60,
		Mirror: api.MirrorStatus{
			ModulesIndexed:        10,
			VersionsIndexed:       50,
			LastIngestAt:          ago(1 * time.Minute),
			LastSyncAt:            ago(30 * time.Second),
			HeadSHA:               "abc1234",
			PromoteOnServeEnabled: false,
		},
		Federation: api.FederationStatus{
			Upstreams: []api.UpstreamStatus{{
				URL:                "https://upstream.example",
				Reachable:          true,
				LastProbeAt:        ago(5 * time.Second),
				LastProbeLatencyMs: 100,
				CacheEntries:       100,
				CacheHitRate:       0.9,
			}},
		},
		Drift: api.DriftStatusInfo{},
		Addons: api.AddonsStatus{},
	}
}

// hasSignal reports whether the signal list contains a signal
// of the given kind at the given level (assertion helper used
// across every threshold test).
func hasSignal(t *testing.T, sigs []api.Signal, kind, level string) bool {
	t.Helper()
	for _, s := range sigs {
		if s.Kind == kind && s.Level == level {
			return true
		}
	}
	return false
}

// =================================================================
// Derive: healthy baseline → no signals
// =================================================================

func TestDerive_HealthyPayload_NoSignals(t *testing.T) {
	got := health.Derive(healthyPayload(), now)
	if got.InstantState != string(health.StateHealthy) {
		t.Errorf("InstantState = %q; want healthy", got.InstantState)
	}
	if len(got.Signals) != 0 {
		t.Errorf("expected no signals for healthy payload; got %+v", got.Signals)
	}
}

// =================================================================
// Federation signals
// =================================================================

func TestDerive_UpstreamUnreachable_RedSignal(t *testing.T) {
	s := healthyPayload()
	s.Federation.Upstreams[0].Reachable = false
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateUnhealthy) {
		t.Errorf("InstantState = %q; want unhealthy", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindUpstreamUnreachable, string(health.StateUnhealthy)) {
		t.Errorf("missing upstream_unreachable/unhealthy signal: %+v", got.Signals)
	}
}

func TestDerive_UpstreamUnreachable_DetailIncludesProbeError(t *testing.T) {
	s := healthyPayload()
	s.Federation.Upstreams[0].Reachable = false
	s.Federation.Upstreams[0].LastProbeError = "TLS handshake timeout"
	got := health.Derive(s, now)
	if !strings.Contains(got.Signals[0].Detail, "TLS handshake timeout") {
		t.Errorf("expected probe error in detail; got %q", got.Signals[0].Detail)
	}
}

func TestDerive_UpstreamSlow_AmberSignal(t *testing.T) {
	s := healthyPayload()
	s.Federation.Upstreams[0].LastProbeLatencyMs = health.SlowProbeMs + 1
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateDegraded) {
		t.Errorf("InstantState = %q; want degraded", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindUpstreamSlow, string(health.StateDegraded)) {
		t.Errorf("missing upstream_slow/degraded signal: %+v", got.Signals)
	}
}

func TestDerive_UpstreamLatencyAtBoundary_NoSignal(t *testing.T) {
	s := healthyPayload()
	s.Federation.Upstreams[0].LastProbeLatencyMs = health.SlowProbeMs
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateHealthy) || len(got.Signals) > 0 {
		t.Errorf("latency==boundary should not signal; got %+v", got)
	}
}

func TestDerive_UnreachableSuppressesSlowOnSameUpstream(t *testing.T) {
	// An unreachable upstream with stale-recorded slow latency should
	// report ONLY upstream_unreachable — slowness on a dead host is
	// noise.
	s := healthyPayload()
	s.Federation.Upstreams[0].Reachable = false
	s.Federation.Upstreams[0].LastProbeLatencyMs = health.SlowProbeMs + 200
	got := health.Derive(s, now)
	if hasSignal(t, got.Signals, health.KindUpstreamSlow, string(health.StateDegraded)) {
		t.Errorf("expected NO upstream_slow signal on unreachable host; got %+v", got.Signals)
	}
	if !hasSignal(t, got.Signals, health.KindUpstreamUnreachable, string(health.StateUnhealthy)) {
		t.Errorf("missing upstream_unreachable signal: %+v", got.Signals)
	}
}

// =================================================================
// Mirror ingest age
// =================================================================

func TestDerive_IngestAtAmberBoundary_AmberSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastIngestAt = ago(health.MirrorStaleAmber)
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateDegraded) {
		t.Errorf("InstantState = %q; want degraded", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindIngestStale, string(health.StateDegraded)) {
		t.Errorf("missing ingest_stale/degraded: %+v", got.Signals)
	}
}

func TestDerive_IngestPastRed_RedSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastIngestAt = ago(health.MirrorStaleRed + time.Second)
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateUnhealthy) {
		t.Errorf("InstantState = %q; want unhealthy", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindIngestStale, string(health.StateUnhealthy)) {
		t.Errorf("missing ingest_stale/unhealthy: %+v", got.Signals)
	}
}

func TestDerive_IngestMissing_NoSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastIngestAt = ""
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateHealthy) || len(got.Signals) > 0 {
		t.Errorf("missing ingest_at = no signal; got %+v", got)
	}
}

func TestDerive_IngestGarbage_NoSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastIngestAt = "not-an-iso"
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateHealthy) || len(got.Signals) > 0 {
		t.Errorf("unparseable ingest_at = no signal; got %+v", got)
	}
}

// =================================================================
// Mirror sync heartbeat
// =================================================================

func TestDerive_SyncAtAmberBoundary_AmberSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastSyncAt = ago(health.SyncStaleAmber)
	got := health.Derive(s, now)
	if !hasSignal(t, got.Signals, health.KindSyncStale, string(health.StateDegraded)) {
		t.Errorf("missing sync_stale/degraded at amber boundary: %+v", got.Signals)
	}
}

func TestDerive_SyncJustUnderAmber_NoSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastSyncAt = ago(health.SyncStaleAmber - time.Second)
	got := health.Derive(s, now)
	if len(got.Signals) > 0 {
		t.Errorf("below amber threshold = no signal; got %+v", got.Signals)
	}
}

func TestDerive_SyncPastRed_RedSignal(t *testing.T) {
	s := healthyPayload()
	s.Mirror.LastSyncAt = ago(health.SyncStaleRed + time.Second)
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateUnhealthy) {
		t.Errorf("InstantState = %q; want unhealthy", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindSyncStale, string(health.StateUnhealthy)) {
		t.Errorf("missing sync_stale/unhealthy: %+v", got.Signals)
	}
}

// =================================================================
// Drift count
// =================================================================

func TestDerive_DriftAtAmberBoundary_AmberSignal(t *testing.T) {
	s := healthyPayload()
	s.Drift.ModulesBehind = health.DriftAmber
	got := health.Derive(s, now)
	if !hasSignal(t, got.Signals, health.KindDriftCount, string(health.StateDegraded)) {
		t.Errorf("missing drift_count/degraded: %+v", got.Signals)
	}
}

func TestDerive_DriftPastRed_RedSignal(t *testing.T) {
	s := healthyPayload()
	s.Drift.ModulesBehind = health.DriftRed + 1
	got := health.Derive(s, now)
	if !hasSignal(t, got.Signals, health.KindDriftCount, string(health.StateUnhealthy)) {
		t.Errorf("missing drift_count/unhealthy: %+v", got.Signals)
	}
}

func TestDerive_DriftSumsBehindAndYanked(t *testing.T) {
	s := healthyPayload()
	s.Drift.ModulesBehind = health.DriftAmber / 2
	s.Drift.ModulesYankedUpstream = health.DriftAmber - s.Drift.ModulesBehind
	got := health.Derive(s, now)
	if !hasSignal(t, got.Signals, health.KindDriftCount, string(health.StateDegraded)) {
		t.Errorf("sum should trip amber: %+v", got.Signals)
	}
}

// =================================================================
// Precedence: worst signal wins for InstantState; signals stack
// =================================================================

func TestDerive_WorstSignalWinsInstantState(t *testing.T) {
	s := healthyPayload()
	s.Federation.Upstreams[0].LastProbeLatencyMs = health.SlowProbeMs + 1
	s.Mirror.LastSyncAt = ago(health.SyncStaleRed + time.Second)
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateUnhealthy) {
		t.Errorf("got %q; want unhealthy (worst-of amber+red)", got.InstantState)
	}
}

func TestDerive_MultipleSignalsCollected(t *testing.T) {
	// Three signals: slow upstream + sync_stale amber + drift amber.
	s := healthyPayload()
	s.Federation.Upstreams[0].LastProbeLatencyMs = health.SlowProbeMs + 1
	s.Mirror.LastSyncAt = ago(health.SyncStaleAmber + time.Minute)
	s.Drift.ModulesBehind = health.DriftAmber
	got := health.Derive(s, now)
	if got.InstantState != string(health.StateDegraded) {
		t.Errorf("InstantState = %q; want degraded", got.InstantState)
	}
	if len(got.Signals) != 3 {
		t.Errorf("expected 3 signals; got %d: %+v", len(got.Signals), got.Signals)
	}
}

// =================================================================
// Signal Detail prose: operator-actionable
// =================================================================

func TestDerive_SignalDetailNamesThreshold(t *testing.T) {
	// Operators reading the detail string should see what they're
	// past — the comparison cutoff in the same unit as the
	// measured value.
	s := healthyPayload()
	s.Mirror.LastSyncAt = ago(2 * time.Hour)
	got := health.Derive(s, now)
	for _, sig := range got.Signals {
		if sig.Kind == health.KindSyncStale {
			if !strings.Contains(sig.Detail, "last sync 2h ago") {
				t.Errorf("detail should name age in HumanDuration; got %q", sig.Detail)
			}
			if !strings.Contains(sig.Detail, "1h") {
				t.Errorf("detail should name amber threshold; got %q", sig.Detail)
			}
			return
		}
	}
	t.Fatalf("no sync_stale signal in %+v", got.Signals)
}

// =================================================================
// DeriveLocal — CLI subset
// =================================================================

func TestDeriveLocal_HealthyDefaults_NoSignals(t *testing.T) {
	got := health.DeriveLocal(0, 0, now.Add(-30*time.Second), now)
	if got.InstantState != string(health.StateHealthy) || len(got.Signals) > 0 {
		t.Errorf("clean snapshot should be healthy/no signals; got %+v", got)
	}
}

func TestDeriveLocal_FileBackedInstall_HealthyDespiteEmptyTimestamps(t *testing.T) {
	got := health.DeriveLocal(0, 0, time.Time{}, now)
	if got.InstantState != string(health.StateHealthy) || len(got.Signals) > 0 {
		t.Errorf("File-backed install = healthy; got %+v", got)
	}
}

func TestDeriveLocal_SyncStaleAmber(t *testing.T) {
	got := health.DeriveLocal(0, 0, now.Add(-health.SyncStaleAmber), now)
	if got.InstantState != string(health.StateDegraded) {
		t.Errorf("InstantState = %q; want degraded", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindSyncStale, string(health.StateDegraded)) {
		t.Errorf("missing sync_stale/degraded: %+v", got.Signals)
	}
}

func TestDeriveLocal_DriftRed(t *testing.T) {
	got := health.DeriveLocal(health.DriftRed+1, 0, now, now)
	if got.InstantState != string(health.StateUnhealthy) {
		t.Errorf("InstantState = %q; want unhealthy", got.InstantState)
	}
	if !hasSignal(t, got.Signals, health.KindDriftCount, string(health.StateUnhealthy)) {
		t.Errorf("missing drift_count/unhealthy: %+v", got.Signals)
	}
}

func TestDeriveLocal_StackedSignals(t *testing.T) {
	// Sync amber + drift amber → two signals, level=degraded.
	got := health.DeriveLocal(health.DriftAmber, 0, now.Add(-health.SyncStaleAmber-time.Minute), now)
	if got.InstantState != string(health.StateDegraded) {
		t.Errorf("got %q; want degraded", got.InstantState)
	}
	if len(got.Signals) != 2 {
		t.Errorf("expected 2 signals; got %d: %+v", len(got.Signals), got.Signals)
	}
}

// =================================================================
// HumanDuration
// =================================================================

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h"},
		{36 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := health.HumanDuration(c.d); got != c.want {
			t.Errorf("HumanDuration(%v) = %q; want %q", c.d, got, c.want)
		}
	}
}
