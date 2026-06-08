package preflight

import "github.com/albertocavalcante/bzlhub/internal/store"

// Verdict is what a Checker produces for one request. The Runner
// persists it as preflight_json before transitioning to NextState.
//
// Only NextState is mandatory; the optional fields reserve shape
// for richer checkers without breaking the stored JSON.
type Verdict struct {
	// NextState is one of auto_pass, needs_review, denied.
	// Anything else is rejected by the Runner.
	NextState store.RequestState `json:"next_state"`
	// Reason is the human-readable explanation. Required when
	// NextState == denied; optional otherwise.
	Reason string `json:"reason,omitempty"`
	// License is the detected SPDX identifier, when known. Empty
	// when the checker didn't run a license probe.
	License string `json:"license,omitempty"`
	// Hermeticity records which hermeticity bucket the request fell
	// into. See Plan 67 §Hermeticity for the taxonomy.
	Hermeticity string `json:"hermeticity,omitempty"`
	// ArchiveSize records the source archive size in bytes (post-
	// fetch). 0 when the checker didn't fetch.
	ArchiveSize int64 `json:"archive_size,omitempty"`
	// SourceSHA is the SHA-256 of the fetched archive. Empty when
	// no fetch happened.
	SourceSHA string `json:"source_sha,omitempty"`
	// Notes carries free-form diagnostic detail for the UI.
	Notes string `json:"notes,omitempty"`
	// CascadeSource is the upstream's source location for requests
	// that auto-passed via the cascade short-circuit. Admit reads it
	// when the request itself has no source_url — cascade-hit
	// modules don't need the user to supply a URL.
	CascadeSource *CascadeHit `json:"cascade_source,omitempty"`
}

// validNextStates is the set of states a Verdict may transition to.
// Sanity-checked by the Runner before issuing the SQL transition.
var validNextStates = map[store.RequestState]bool{
	store.RequestStateAutoPass:    true,
	store.RequestStateNeedsReview: true,
	store.RequestStateDenied:      true,
}
