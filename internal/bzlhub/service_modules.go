package bzlhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	bzlsummary "github.com/albertocavalcante/bazel-module-summary-go"
	"github.com/albertocavalcante/understory/pkg/understory"

	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/codenav"
	"github.com/albertocavalcante/bzlhub/internal/store"
)

// Summary returns the bazel-module-summary-go composed view of one
// (module, version). Pulls the source root from the SourcesCacheDir
// (populated during Bump) and the metadata.json from the mirror tree
// to enrich registry-level fields (homepage, maintainers, yanked).
//
// Two soft conditions:
//   - SourcesCacheDir empty → returns an error explaining the canopy
//     was started without a sources cache.
//   - Source root missing → returns an error suggesting (re-)Bump.
//
// metadata.json missing is silently absorbed by the library; the
// returned Summary just has empty registry fields, which the caller
// can still render.
func (s *Service) Summary(ctx context.Context, name, version string) (*bzlsummary.Summary, error) {
	if s.SourcesCacheDir == "" || s.MirrorRoot == "" {
		return nil, errors.New("summary not available: canopy was started without both --root (mirror) and a sources cache")
	}
	// MaterializeSource unpacks the tarball on demand if it isn't
	// already cached — same machinery codenav uses for browse
	// requests. Idempotent: a hit just stats the .complete sentinel.
	// This means a fresh bzlhub_summary call works even on a module
	// whose source was never browsed before.
	sourceRoot, err := codenav.MaterializeSource(s.MirrorRoot, s.SourcesCacheDir, name, version)
	if err != nil {
		return nil, fmt.Errorf("materialize source for %s@%s: %w", name, version, err)
	}
	return bzlsummary.FromDir(sourceRoot,
		// metadata.json lives in the BCR-shape mirror at
		// modules/<m>/metadata.json. Library tolerates missing file
		// cleanly, so we wire unconditionally.
		bzlsummary.WithMetadataJSON(filepath.Join(s.MirrorRoot, "modules", name, "metadata.json")),
		// Coordinate override — real BCR modules often ship
		// MODULE.bazel with a "0.0.0" stub that release tooling
		// rewrites post-publish. The caller asked for a specific
		// (name, version); make the summary report THAT.
		bzlsummary.WithCanonicalCoordinate(name, version),
	)
}

func (s *Service) ListVersions(ctx context.Context, name string) ([]string, error) {
	return s.store.ListVersions(ctx, name)
}

// GetTarballSize returns the compressed-tarball size in bytes for
// (module, version), or 0 when unknown (pre-migration ingest).
// Exposed for the HTTP layer's per-version response augmentation.
func (s *Service) GetTarballSize(ctx context.Context, name, version string) (int64, error) {
	return s.store.GetTarballSize(ctx, name, version)
}

// ListVersionsWithMeta is ListVersions plus ingested_at per row.
// Exposed (uppercase) so the HTTP layer can type-assert to Service
// for this without widening the cross-transport api.Canopy
// interface — same fall-through pattern as ComputeUsageCounts.
func (s *Service) ListVersionsWithMeta(ctx context.Context, name string) ([]store.VersionRow, error) {
	return s.store.ListVersionsWithMeta(ctx, name)
}

// ListModules collapses store.ListAllVersions into one summary row
// per module — name + latest version + version count. Single-pass
// grouping on the already-sorted (module, version) stream from the
// store, so the cost is O(rows) with one allocation per module.
//
// "Latest" picks the LAST version in the sort order, which is
// lexical ASC. That matches what /modules/<m>/+page.svelte already
// shows as the "newest first" head; consistent across the UI.
//
// After grouping, we run a second pass to fill HasSourceIndex per
// module: read the cached versions.has_source_index column for
// the latest version. Modules whose source tarballs ship no
// Starlark files (zlib, abseil-cpp, etc.) sit at false; the UI
// hides Code → surfaces for those so users don't click into an
// empty file tree. The column is kept fresh by the ingest path
// (Service.Bump + IngestDir) and reconciled at boot via
// BackfillSourceIndexFlags.
func (s *Service) ListModules(ctx context.Context) ([]api.ModuleSummary, error) {
	rows, err := s.store.ListAllVersions(ctx)
	if err != nil {
		return nil, err
	}
	// Per-module bookkeeping for the diff_href computation: as we
	// stream (module, version) ASC, track the "newest non-stub"
	// and the "previous non-stub before it" so we can compose a
	// useful default diff pair after the loop. Stubs (0.0.0,
	// empty, "0") would produce empty diffs and aren't worth a
	// row's diff link.
	type diffTrack struct {
		prevNonStub   string
		latestNonStub string
	}
	track := map[string]*diffTrack{}

	out := []api.ModuleSummary{}
	for _, mv := range rows {
		if !api.IsStubVersion(mv.Version) {
			t, ok := track[mv.Module]
			if !ok {
				t = &diffTrack{}
				track[mv.Module] = t
			}
			t.prevNonStub = t.latestNonStub
			t.latestNonStub = mv.Version
		}
		if n := len(out); n > 0 && out[n-1].Name == mv.Module {
			out[n-1].LatestVersion = mv.Version // last wins (ASC)
			out[n-1].VersionCount++
			// LatestIngestedAt tracks the freshness of the latest
			// version row — that's the one users see on cards.
			if !mv.IngestedAt.IsZero() {
				out[n-1].LatestIngestedAt = mv.IngestedAt.UTC().Format(time.RFC3339)
			}
			continue
		}
		entry := api.ModuleSummary{
			Name:          mv.Module,
			LatestVersion: mv.Version,
			VersionCount:  1,
		}
		if !mv.IngestedAt.IsZero() {
			entry.LatestIngestedAt = mv.IngestedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}

	// Resolve LatestDiffHref now that the streaming pass is done.
	// Also flag is_new = first-version-only modules ingested in
	// the last 7 days; gives /modules cards a "NEW" badge.
	const newModuleHorizon = 7 * 24 * time.Hour
	now := time.Now().UTC()
	for i := range out {
		if t, ok := track[out[i].Name]; ok && t.prevNonStub != "" && t.latestNonStub != "" {
			out[i].LatestDiffHref = "/modules/" + out[i].Name + "/diff?from=" + t.prevNonStub + "&to=" + t.latestNonStub
		}
		if out[i].VersionCount == 1 && out[i].LatestIngestedAt != "" {
			if ts, err := time.Parse(time.RFC3339, out[i].LatestIngestedAt); err == nil {
				if now.Sub(ts) <= newModuleHorizon {
					out[i].IsNew = true
				}
			}
		}
	}
	usage, err := s.ComputeUsageCounts(ctx)
	if err != nil {
		return nil, err
	}

	for i := range out {
		out[i].HasSourceIndex = s.hasSourceIndex(ctx, out[i].Name, out[i].LatestVersion)
		out[i].Drift = s.driftSummary(ctx, out[i].Name, out[i].LatestVersion)
		out[i].UsageCount = usage[out[i].Name]
		// Enrich with mirror-side registry metadata when available.
		// One small metadata.json read per module; ReadMetadataJSON
		// returns zero-value on missing-file so an unenriched module
		// just produces empty fields.
		if s.MirrorRoot != "" {
			meta, err := bzlsummary.ReadMetadataJSON(filepath.Join(s.MirrorRoot, "modules", out[i].Name, "metadata.json"))
			if err == nil && meta != nil {
				out[i].Homepage = meta.Homepage
				out[i].MaintainerCount = len(meta.Maintainers)
				out[i].RepoLabel = deriveRepoLabel(meta.Repository, meta.Homepage)
			}
		}
	}
	return out, nil
}

// GetModule returns a ModuleSummary for the named module. Powers
// the cross-module HoverCard which needs latest version + version
// count + repo label + has_source_index per hovered link without
// fetching the whole catalogue.
//
// Targeted implementation: uses store.ListVersionsWithMeta(name) —
// one indexed-name SQL query instead of the corpus-wide walk
// ListModules does. Skips the cross-corpus ComputeUsageCounts walk
// (O(N) reports loaded) entirely, since hover-card display doesn't
// render usage_count anyway. The fields it DOES populate match the
// listing view byte-for-byte for the same module name.
//
// Returns api.ErrModuleNotFound when the name isn't indexed —
// handlers map that to HTTP 404 so the hover renders a "not
// indexed here" hint instead of a generic 5xx.
func (s *Service) GetModule(ctx context.Context, name string) (*api.ModuleSummary, error) {
	if name == "" {
		return nil, api.ErrModuleNotFound
	}
	rows, err := s.store.ListVersionsWithMeta(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, api.ErrModuleNotFound
	}

	// DESC order → newest at [0]. Walk forward for the two most-
	// recent non-stub versions so LatestDiffHref points at a
	// meaningful pair (stubs like "" / "0" / "0.0.0" produce empty
	// diffs and aren't worth a link).
	var latestNonStub, prevNonStub string
	for _, r := range rows {
		if api.IsStubVersion(r.Version) {
			continue
		}
		if latestNonStub == "" {
			latestNonStub = r.Version
		} else if prevNonStub == "" {
			prevNonStub = r.Version
			break
		}
	}

	out := &api.ModuleSummary{
		Name:          name,
		LatestVersion: rows[0].Version,
		VersionCount:  len(rows),
	}
	if !rows[0].IngestedAt.IsZero() {
		out.LatestIngestedAt = rows[0].IngestedAt.UTC().Format(time.RFC3339)
	}
	if prevNonStub != "" && latestNonStub != "" {
		out.LatestDiffHref = "/modules/" + name + "/diff?from=" + prevNonStub + "&to=" + latestNonStub
	}
	// is_new: first-version-only modules ingested in the last 7
	// days. Same horizon as ListModules so the badge appears
	// consistently across listing + hover.
	const newModuleHorizon = 7 * 24 * time.Hour
	if out.VersionCount == 1 && out.LatestIngestedAt != "" {
		if ts, err := time.Parse(time.RFC3339, out.LatestIngestedAt); err == nil {
			if time.Now().UTC().Sub(ts) <= newModuleHorizon {
				out.IsNew = true
			}
		}
	}
	out.HasSourceIndex = s.hasSourceIndex(ctx, out.Name, out.LatestVersion)
	out.Drift = s.driftSummary(ctx, out.Name, out.LatestVersion)
	if s.MirrorRoot != "" {
		meta, err := bzlsummary.ReadMetadataJSON(filepath.Join(s.MirrorRoot, "modules", name, "metadata.json"))
		if err == nil && meta != nil {
			out.Homepage = meta.Homepage
			out.MaintainerCount = len(meta.Maintainers)
			out.RepoLabel = deriveRepoLabel(meta.Repository, meta.Homepage)
		}
	}
	// UsageCount intentionally omitted — the hover card doesn't
	// render it, so paying for ComputeUsageCounts (a corpus-wide
	// dep walk) per hover would be pure overhead. ListModules
	// still computes + populates it for the listing page.
	return out, nil
}

// CorpusStats, ComputeCorpusStats, ComputeUsageCounts,
// ComputeUsageCountsByVersion live in stats.go.
// RefreshGitHubMeta, GetGitHubMeta, RefreshGitHubMetaAll,
// resolveGitHubRepo live in githubmeta.go.

// hasSourceIndex reports whether (module, version)'s SCIP blob
// contains at least one indexed document. Reads the cached
// versions.has_source_index column populated by the ingest path
// and the boot-time backfill. Missing rows + DB errors collapse
// to false — the UI treats those the same as "no Starlark to
// navigate."
func (s *Service) hasSourceIndex(ctx context.Context, module, version string) bool {
	has, err := s.store.GetHasSourceIndex(ctx, module, version)
	if err != nil {
		return false
	}
	return has
}

// driftSummary reads the cached api.DriftSummary for (module,
// version) from versions.drift_summary_json and decodes it. Missing
// rows, DB errors, and malformed JSON all collapse to the zero
// value (Status=DriftStatusUnknown) — the UI treats unknown as
// "no chip rendered", and a transient store-layer hiccup must not
// hide every drift chip on the listing page.
//
// Written by the future drift-cache write path (Plan 19 Idea A
// backend, Plan 26 κ6 ModuleReport-in-AC pulldown) and reconciled
// at boot via BackfillDriftSummary (Plan 28 C12 seam, currently a
// no-op walker).
func (s *Service) driftSummary(ctx context.Context, module, version string) api.DriftSummary {
	payload, err := s.store.GetDriftSummary(ctx, module, version)
	if err != nil {
		return api.DriftSummary{}
	}
	var d api.DriftSummary
	if err := json.Unmarshal(payload, &d); err != nil {
		return api.DriftSummary{}
	}
	return d
}

// scipBlobHasFiles returns true when blob parses successfully and
// contains at least one indexed document. The single source of
// truth for "is this SCIP blob worth navigating?" — used by the
// ingest path to populate the cached flag and by the backfill task
// to reconcile pre-migration rows.
func scipBlobHasFiles(blob []byte) bool {
	idx, err := understory.OpenBytes(blob)
	if err != nil {
		return false
	}
	return len(idx.Files()) > 0
}

// deriveRepoLabel returns a compact "owner/repo" display label for
// the module's source repository. Prefers the BCR metadata.json
// `repository` array (entries like "github:owner/repo"); falls back
// to parsing a github.com homepage URL. Returns "" when no
// recognizable repo identity is available — UI then falls back to
// the homepage hostname.
//
// Recognized repository prefixes mirror BCR conventions:
//
//	"github:owner/repo" → "owner/repo"
//	"gitlab:owner/repo" → "owner/repo"
func deriveRepoLabel(repos []string, homepage string) string {
	for _, r := range repos {
		if r == "" {
			continue
		}
		if i := strings.Index(r, ":"); i > 0 {
			rest := r[i+1:]
			if rest != "" && strings.Count(rest, "/") >= 1 {
				return rest
			}
		}
	}
	// Fallback: github.com homepage URL.
	if homepage != "" {
		if u, err := url.Parse(homepage); err == nil && strings.EqualFold(u.Hostname(), "github.com") {
			path := strings.Trim(u.Path, "/")
			parts := strings.Split(path, "/")
			if len(parts) >= 2 {
				return parts[0] + "/" + parts[1]
			}
		}
	}
	return ""
}
