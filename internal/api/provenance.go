package api

import "time"

// BumpProvenance is the read-side view of the cheap provenance
// captured at Bump time (I4): the upstream BCR git state at the
// moment canopy ingested this (module, version). Decorative —
// canopy stays useful when this is absent (non-BCR upstreams,
// pre-I4 ingests, anonymous GitHub rate-limited during Bump).
type BumpProvenance struct {
	// BCRHeadSHA is the bazelbuild/bazel-central-registry HEAD
	// commit at Bump time. Empty when canopy couldn't reach GitHub.
	BCRHeadSHA string `json:"bcr_head_sha"`
	// URL is the GitHub UI link to that commit's tree view.
	URL string `json:"url,omitempty"`
	// RecordedAt is the timestamp of the bump_success audit event.
	RecordedAt time.Time `json:"recorded_at"`
}
