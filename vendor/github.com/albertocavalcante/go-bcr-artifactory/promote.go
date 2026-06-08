package artifactory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/albertocavalcante/go-bcr-httpstore"
)

// PromoteOptions configures a single build-promotion call. Mirrors
// the subset of Artifactory's promote-build API that's load-bearing
// for canopy's release-management workflow; less common fields can
// be added later as new pointer fields without breaking callers
// (zero-pointer = "send default").
type PromoteOptions struct {
	// BuildName is the Artifactory build name (the first path
	// segment under /api/build/promote/). Required.
	BuildName string

	// BuildNumber is the Artifactory build number (the second
	// path segment). Required.
	BuildNumber string

	// SourceRepo is the repo the build's artifacts currently live
	// in. Required.
	SourceRepo string

	// TargetRepo is the repo to promote the artifacts to. Required.
	TargetRepo string

	// Status is a free-form status label stamped on the build at
	// promotion time (e.g. "promoted", "released", "rejected").
	// Optional; omitted from request body when empty.
	Status string

	// Comment is an optional human-readable note. Omitted when empty.
	Comment string

	// Copy controls whether artifacts are copied (true) or moved
	// (false; default in Artifactory). Set true to leave the
	// source repo intact.
	Copy bool

	// DryRun, when true, asks Artifactory to validate the promote
	// without actually doing it. Useful for CI gating.
	DryRun bool

	// Properties are key-value tags to apply to promoted artifacts
	// (Artifactory's sidecar property mechanism). Multi-value per
	// key supported. Omitted when nil or empty.
	Properties map[string][]string

	// Timestamp overrides the default (now) on the request. Useful
	// to attribute the promotion to a specific point in CI time.
	// Zero value means "send no timestamp; let upstream default".
	Timestamp time.Time
}

// promoteRequest is the JSON body Artifactory expects on the
// /api/build/promote endpoint. Kept unexported — callers configure
// via PromoteOptions, which is the stable contract.
type promoteRequest struct {
	Status     string              `json:"status,omitempty"`
	Comment    string              `json:"comment,omitempty"`
	SourceRepo string              `json:"sourceRepo"`
	TargetRepo string              `json:"targetRepo"`
	Copy       bool                `json:"copy"`
	DryRun     bool                `json:"dryRun,omitempty"`
	Properties map[string][]string `json:"properties,omitempty"`
	Timestamp  string              `json:"timestamp,omitempty"`
}

// PromoteBuild promotes a build via Artifactory's
// /api/build/promote/<buildName>/<buildNumber> endpoint. Moves
// (or copies, if opts.Copy) the build's artifacts from
// opts.SourceRepo to opts.TargetRepo, with optional status,
// comment, properties, and timestamp.
//
// Designed for canopy's release-management workflow: the canopy
// publisher reads a candidate build from the source repo, runs
// verification, then promotes it to the release repo with status
// metadata stamped on.
//
// Free function rather than a struct method because promotion
// doesn't carry per-instance state — every call spans a different
// (source, target) pair. Compare with Properties (which carries
// a repo) where the struct shape makes sense.
//
// Returns httpstore.ErrInvalidOptions when any of BuildName,
// BuildNumber, SourceRepo, TargetRepo is empty (validated before
// the upstream call so misconfiguration fails loudly without
// burning a round-trip).
//
// Status mapping:
//
//   - 200 / 201 / 204 → nil (success; promotion either committed
//     or, for DryRun, validated successfully)
//   - 400 → ErrUpstreamStatus (validation failure from Artifactory;
//     wrapped with the response body's hints)
//   - 401 → httpstore.ErrUnauthorized
//   - 403 → httpstore.ErrForbidden
//   - 404 → httpstore.ErrUpstream404 (build name / number not found)
//   - other non-2xx → httpstore.ErrUpstreamStatus
func PromoteBuild(ctx context.Context, backend *httpstore.Backend, opts PromoteOptions) error {
	if backend == nil {
		return fmt.Errorf("%w: backend is required", httpstore.ErrInvalidOptions)
	}
	if opts.BuildName == "" {
		return fmt.Errorf("%w: PromoteOptions.BuildName is required", httpstore.ErrInvalidOptions)
	}
	if opts.BuildNumber == "" {
		return fmt.Errorf("%w: PromoteOptions.BuildNumber is required", httpstore.ErrInvalidOptions)
	}
	if opts.SourceRepo == "" {
		return fmt.Errorf("%w: PromoteOptions.SourceRepo is required", httpstore.ErrInvalidOptions)
	}
	if opts.TargetRepo == "" {
		return fmt.Errorf("%w: PromoteOptions.TargetRepo is required", httpstore.ErrInvalidOptions)
	}

	body := promoteRequest{
		Status:     opts.Status,
		Comment:    opts.Comment,
		SourceRepo: opts.SourceRepo,
		TargetRepo: opts.TargetRepo,
		Copy:       opts.Copy,
		DryRun:     opts.DryRun,
		Properties: opts.Properties,
	}
	if !opts.Timestamp.IsZero() {
		body.Timestamp = opts.Timestamp.UTC().Format(time.RFC3339)
	}

	payload, err := json.Marshal(body)
	if err != nil {
		// json.Marshal of map[string][]string + strings + bools can
		// only fail on a programmer error (e.g. cyclic structure
		// passed via reflection). Surface anyway for diagnostics.
		return fmt.Errorf("%w: marshal promote body: %v",
			httpstore.ErrInvalidOptions, err)
	}

	relPath := path.Join("api/build/promote", opts.BuildName, opts.BuildNumber)
	headers := http.Header{"Content-Type": []string{"application/json"}}
	resp, err := backend.Do(ctx, http.MethodPost, relPath, nil, headers, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w: POST %s (build %s/%s not found)",
			httpstore.ErrUpstream404, relPath, opts.BuildName, opts.BuildNumber)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: POST %s", httpstore.ErrUnauthorized, relPath)
	case http.StatusForbidden:
		return fmt.Errorf("%w: POST %s", httpstore.ErrForbidden, relPath)
	default:
		return fmt.Errorf("%w: POST %s -> %d %s",
			httpstore.ErrUpstreamStatus, relPath, resp.StatusCode, resp.Status)
	}
}
