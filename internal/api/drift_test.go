package api

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDriftStatus_StableStrings locks the wire tokens. Operators key
// dashboards off these; the UI's DriftChip palette switches on these
// exact strings. Renames are policy changes — a failing test here is
// a reminder that the consumer side must move in lockstep.
func TestDriftStatus_StableStrings(t *testing.T) {
	cases := []struct {
		s    DriftStatus
		want string
	}{
		{DriftStatusUnknown, "unknown"},
		{DriftStatusInSync, "in-sync"},
		{DriftStatusBehind, "behind"},
		{DriftStatusYankedUpstream, "yanked-upstream"},
		{DriftStatusLocalOnly, "local-only"},
		{DriftStatusUpstreamError, "upstream-error"},
	}
	for _, c := range cases {
		if got := string(c.s); got != c.want {
			t.Errorf("DriftStatus = %q, want %q", got, c.want)
		}
	}
}

// TestDriftSummary_JSONShape locks the wire format readers depend on:
// snake-case keys, omitempty discipline, no extraneous fields. UI
// snapshot tests downstream key off these names exactly.
func TestDriftSummary_JSONShape(t *testing.T) {
	d := DriftSummary{
		Status:         DriftStatusBehind,
		Behind:         4,
		LatestUpstream: "1.9.0",
		ComputedAt:     time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"status", "behind", "latest_upstream", "computed_at"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q in JSON: %s", k, string(b))
		}
	}
}

// TestDriftSummary_ZeroValueOmitsEverything asserts the
// signal-by-absence contract: an empty DriftSummary marshals to {}
// with no fields. Listing-page payloads must not bloat by N times
// the "unknown" string when no drift data is available — the empty
// object is the cheapest possible shape, and the UI hides the chip
// when no fields are present.
func TestDriftSummary_ZeroValueOmitsEverything(t *testing.T) {
	d := DriftSummary{}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("zero-value DriftSummary = %q, want %q", b, "{}")
	}
}

// TestDriftSummary_RoundTripsCleanly asserts the type is its own
// inverse through encoding/json. Critical because the store layer
// persists the JSON bytes and the api layer rehydrates them — any
// drift introduced by marshaling here breaks the inline-badges
// pipeline silently.
func TestDriftSummary_RoundTripsCleanly(t *testing.T) {
	in := DriftSummary{
		Status:         DriftStatusYankedUpstream,
		Behind:         0,
		LatestUpstream: "2.0.0",
		ComputedAt:     time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out DriftSummary
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.Status != in.Status {
		t.Errorf("Status: got %q, want %q", out.Status, in.Status)
	}
	if out.LatestUpstream != in.LatestUpstream {
		t.Errorf("LatestUpstream: got %q, want %q", out.LatestUpstream, in.LatestUpstream)
	}
	if !out.ComputedAt.Equal(in.ComputedAt) {
		t.Errorf("ComputedAt: got %v, want %v", out.ComputedAt, in.ComputedAt)
	}
}

// TestModuleSummary_DriftFieldEmbedded asserts ModuleSummary carries
// the new field with the correct JSON tag. Guards the API contract:
// the UI listing page reads ModuleSummary.drift and renders the chip
// strip; misnaming the field here breaks the listing rendering for
// every consumer.
func TestModuleSummary_DriftFieldEmbedded(t *testing.T) {
	m := ModuleSummary{
		Name:          "bazel_skylib",
		LatestVersion: "1.7.1",
		Drift: DriftSummary{
			Status: DriftStatusBehind,
			Behind: 4,
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	drift, ok := got["drift"].(map[string]any)
	if !ok {
		t.Fatalf("ModuleSummary JSON has no 'drift' key: %s", string(b))
	}
	if drift["status"] != "behind" {
		t.Errorf("drift.status = %v, want behind", drift["status"])
	}
	if behind, _ := drift["behind"].(float64); behind != 4 {
		t.Errorf("drift.behind = %v, want 4", drift["behind"])
	}
}
