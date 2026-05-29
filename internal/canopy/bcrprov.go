package canopy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/egress"
	"github.com/albertocavalcante/canopy/internal/githubapi/token"
	"github.com/albertocavalcante/canopy/internal/store"
)

// storeAuditQueryBumpFor builds the AuditQuery that asks for the
// most recent bump_success rows for a given module (the latest
// per-version filter is applied in GetLatestBumpProvenance because
// AuditQuery doesn't carry a Version filter). Limit is small —
// the matching row will be at the top in DESC order.
func storeAuditQueryBumpFor(module, _ string) store.AuditQuery {
	return store.AuditQuery{
		Kinds:  []string{"bump_success"},
		Module: module,
		Limit:  20,
	}
}

// bcrHEADCacheTTL bounds how often we re-query GitHub for the
// bazelbuild/bazel-central-registry HEAD commit. 5 min is short
// enough that a recursive ingest of a 50-module closure picks up
// the same SHA throughout (one API call covers the whole sweep)
// but long enough that two consecutive Bumps don't both spend a
// rate-limit slot.
const bcrHEADCacheTTL = 5 * time.Minute

// bcrProv caches the BCR HEAD SHA for the in-process Service, so a
// closure ingest doesn't spawn one GitHub call per module. Zero
// value is the empty cache; the lock protects both fields.
type bcrProv struct {
	mu      sync.Mutex
	sha     string
	expires time.Time
}

// isBCRUpstream reports whether the upstream registry URL looks
// like the canonical Bazel Central Registry (bcr.bazel.build).
// Loose substring match — handles trailing slashes, schemes, and
// the test-fixture variant ("registry.bazel.build") consistently.
func isBCRUpstream(upstream string) bool {
	return strings.Contains(strings.ToLower(upstream), "bcr.bazel.build")
}

// bcrHeadSHA returns the current HEAD commit SHA of
// bazelbuild/bazel-central-registry on its main branch, fetching
// from the GitHub API at most once per bcrHEADCacheTTL. Returns
// ("", nil) on any error (transport failure, rate-limit, parse
// error) — the SHA is decorative; never block a Bump on it.
//
// Uses the Service's token.Provider when configured (authenticated
// 5000/h bucket); falls back to anonymous (60/h) which is fine
// because of the TTL cache.
func (s *Service) bcrHeadSHA(ctx context.Context, tp token.Provider) string {
	if s == nil {
		return ""
	}
	s.bcrProvCache.mu.Lock()
	if s.bcrProvCache.sha != "" && time.Now().Before(s.bcrProvCache.expires) {
		out := s.bcrProvCache.sha
		s.bcrProvCache.mu.Unlock()
		return out
	}
	s.bcrProvCache.mu.Unlock()

	sha := fetchBCRHeadSHA(ctx, tp)
	if sha == "" {
		return ""
	}

	s.bcrProvCache.mu.Lock()
	s.bcrProvCache.sha = sha
	s.bcrProvCache.expires = time.Now().Add(bcrHEADCacheTTL)
	s.bcrProvCache.mu.Unlock()
	return sha
}

// fetchBCRHeadSHA does the one-shot GitHub API call. Kept package-
// private and stateless so the cache wrapper is the only call site
// exercising the network.
func fetchBCRHeadSHA(ctx context.Context, tp token.Provider) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/bazelbuild/bazel-central-registry/commits/HEAD", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "canopy")
	if tp != nil {
		if tok, _ := tp.Token(ctx); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	client := egress.NewHTTPClient(egress.Policy{})
	client.Timeout = 5 * time.Second
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	// Cap the read — GitHub's commits-API response is ~30 bytes for
	// this query but a compromised proxy could serve a huge JSON.
	// Best-effort lookup, so unbounded OOM here would be a bad
	// trade for the value it provides.
	const maxBodyBytes = 64 * 1024
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes+1))
	if err != nil || int64(len(raw)) > maxBodyBytes {
		return ""
	}
	var body struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	return body.SHA
}

// bcrProvenancePayload is the JSON shape under audit_events.payload
// when a Bump captures BCR provenance. Other Bump payload fields
// (rule counts, etc.) live alongside.
type bcrProvenancePayload struct {
	BCRHeadSHA string `json:"bcr_head_sha,omitempty"`
}

// extractBCRHeadSHA pulls the BCR HEAD SHA out of an audit payload
// JSON blob, returning "" when the payload doesn't carry one.
// Used by the read-side helper that surfaces provenance to the UI.
func extractBCRHeadSHA(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var p bcrProvenancePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return p.BCRHeadSHA
}

// bcrCommitURL renders the GitHub UI URL for a specific BCR commit
// SHA. Empty input returns "".
func bcrCommitURL(sha string) string {
	if sha == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/bazelbuild/bazel-central-registry/tree/%s", sha)
}

// GetLatestBumpProvenance reads the most recent bump_success audit
// event for (module, version) and extracts the BCR HEAD SHA from
// its payload. Returns (nil, nil) when no provenance is available
// (pre-I4 ingests, non-BCR upstream, GitHub call failed at Bump
// time). Never returns an error for missing data — provenance is
// decorative.
func (s *Service) GetLatestBumpProvenance(ctx context.Context, module, version string) (*api.BumpProvenance, error) {
	if s.store == nil {
		return nil, nil
	}
	rows, err := s.store.ListAudit(ctx, storeAuditQueryBumpFor(module, version))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Module != module || r.Version != version {
			continue
		}
		sha := extractBCRHeadSHA(r.Payload)
		if sha == "" {
			continue
		}
		return &api.BumpProvenance{
			BCRHeadSHA: sha,
			URL:        bcrCommitURL(sha),
			RecordedAt: r.Timestamp,
		}, nil
	}
	return nil, nil
}
