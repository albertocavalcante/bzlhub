package canopy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/albertocavalcante/assay/interp"
	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/drift"
	"github.com/albertocavalcante/canopy/internal/fetch"
	"github.com/albertocavalcante/canopy/internal/ingest"
	"github.com/albertocavalcante/canopy/internal/mirror"
	"github.com/albertocavalcante/canopy/internal/resolve"
	canopyscip "github.com/albertocavalcante/canopy/internal/scip"
	"github.com/albertocavalcante/canopy/internal/store"
)

func (s *Service) IngestDir(ctx context.Context, dir string) (*report.ModuleReport, error) {
	r, err := ingest.FromDir(ctx, s.store, dir)
	if err != nil {
		return r, err
	}
	// Tier-3 attrs hydration runs BEFORE the SCIP generation + module
	// persist that ingest.FromDir's downstream callers depend on, so
	// the persisted ModuleReport has the interpreter-resolved attrs
	// stored alongside it. Best-effort: any failure inside Hydrate
	// silently leaves the report's existing attrs as-is.
	if s.AttrsInterpret {
		interp.Hydrate(ctx, dir, r)
		// Persist again — FromDir wrote the pre-hydrated report; the
		// hydration may have added attrs that downstream consumers
		// (UI, MCP, etc.) read from the persisted copy, not the
		// in-memory one. Best-effort write.
		if werr := s.store.WriteReport(ctx, r); werr != nil {
			slog.Warn("post-hydrate report rewrite failed", "module", r.Name, "version", r.Version, "err", werr)
		}
	}
	// Same best-effort SCIP generation as Bump: failure is logged but
	// doesn't fail the ingest. The (Name, Version) used here is what
	// assay extracted, which may be the fallback "HEAD" for local dirs
	// without an explicit version — that's fine; the symbol prefix
	// will just be "bzlmod <name>@HEAD".
	if scipBlob, scipErr := canopyscip.Generate(dir, r.Name, r.Version, r); scipErr == nil {
		if werr := s.store.WriteScipBlob(ctx, r.Name, r.Version, scipBlob); werr != nil {
			slog.Warn("scip blob write failed", "module", r.Name, "version", r.Version, "err", werr)
		} else if uerr := s.store.SetHasSourceIndex(ctx, r.Name, r.Version, scipBlobHasFiles(scipBlob)); uerr != nil {
			slog.Warn("set has_source_index failed", "module", r.Name, "version", r.Version, "err", uerr)
		}
	} else {
		slog.Warn("scip index generation failed", "module", r.Name, "version", r.Version, "err", scipErr)
	}
	s.emit("module_indexed", eventFromReport(r))
	return r, nil
}

// RefreshMetadataResult is the per-call summary of RefreshMetadata.
// Distinguishes successful refreshes from upstream failures so an
// operator can see at-a-glance how complete the backfill ended up.
type RefreshMetadataResult struct {
	Refreshed int      `json:"refreshed"`
	Failed    int      `json:"failed"`
	Errors    []string `json:"errors,omitempty"`
}

// RefreshMetadata walks every module in the index and re-fetches its
// upstream metadata.json, merging registry-level fields (homepage,
// maintainers, repository, yanked_versions) into the local mirror.
//
// Designed for backfilling modules bumped BEFORE the
// MergeMetadataWithUpstream landed — they have a thin local
// metadata.json with just the version list and no registry fields.
// One run brings the whole corpus up to date.
//
// Cost is one HTTP HEAD-like request + one file write per module —
// fast. Errors per-module are collected, not propagated (a single
// flaky upstream shouldn't abort the rest of the walk).
func (s *Service) RefreshMetadata(ctx context.Context, upstream string) (*RefreshMetadataResult, error) {
	if s.MirrorRoot == "" {
		return nil, errors.New("refresh-metadata not available: canopy was started without --root pointing at a mirror tree")
	}
	if upstream == "" {
		upstream = s.DefaultUpstream
	}
	mw, err := mirror.New(s.MirrorRoot)
	if err != nil {
		return nil, fmt.Errorf("init mirror: %w", err)
	}
	mods, err := s.ListModules(ctx)
	if err != nil {
		return nil, err
	}
	client := fetch.NewClient()
	out := &RefreshMetadataResult{}
	for _, m := range mods {
		bytes, err := client.GetMetadataBytes(ctx, upstream, m.Name)
		if err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("%s: fetch: %v", m.Name, err))
			continue
		}
		// MergeMetadataWithUpstream needs a version to dedupe into
		// the versions list — pass the module's latest so the
		// version list invariant stays correct. Real backfill case
		// is metadata-fields-only, so the version is just preserved.
		if err := mw.MergeMetadataWithUpstream(m.Name, m.LatestVersion, bytes); err != nil {
			out.Failed++
			out.Errors = append(out.Errors, fmt.Sprintf("%s: merge: %v", m.Name, err))
			continue
		}
		out.Refreshed++
	}
	return out, nil
}

func (s *Service) Drift(ctx context.Context, opts api.DriftOptions) (*drift.Report, error) {
	if s.MirrorRoot == "" {
		return nil, errors.New("drift not available: canopy was started without --root pointing at a mirror tree")
	}
	upstream := opts.Upstream
	if upstream == "" {
		upstream = s.DefaultUpstream
	}
	return drift.Compute(ctx, s.MirrorRoot, upstream, drift.Options{
		Module:  opts.Module,
		Workers: opts.Workers,
	})
}

func (s *Service) Bump(ctx context.Context, opts api.BumpOptions) (rep *report.ModuleReport, retErr error) {
	t0 := time.Now()
	source := opts.Source
	if source == "" {
		source = "unknown"
	}
	defer func() {
		dur := time.Since(t0).Milliseconds()
		ev := store.AuditEvent{
			Kind:       "bump_success",
			Source:     source,
			Module:     opts.Module,
			Version:    opts.Version,
			OK:         retErr == nil,
			DurationMs: dur,
		}
		if retErr != nil {
			ev.Kind = "bump_failure"
			ev.Error = retErr.Error()
		} else if rep != nil {
			payload := map[string]any{
				"rules":     len(rep.Rules),
				"providers": len(rep.Providers),
				"macros":    len(rep.Macros),
			}
			// Cheap BCR provenance: when the upstream is canonical
			// BCR, stamp the current bazelbuild/bazel-central-registry
			// HEAD SHA into the payload. Cached in-Service for 5min
			// so a recursive ingest spends at most one GitHub API
			// call. Failure is silent — provenance is decorative.
			if isBCRUpstream(opts.Upstream) || (opts.Upstream == "" && isBCRUpstream(s.DefaultUpstream)) {
				if sha := s.bcrHeadSHA(ctx, s.GitHubToken); sha != "" {
					payload["bcr_head_sha"] = sha
				}
			}
			b, _ := json.Marshal(payload)
			ev.Payload = b
		}
		s.audit(ctx, ev)
	}()

	if s.MirrorRoot == "" {
		return nil, errors.New("bump not available: canopy was started without --root pointing at a mirror tree")
	}
	if opts.Module == "" || opts.Version == "" {
		return nil, errors.New("bump: module and version are required")
	}
	upstream := opts.Upstream
	if upstream == "" {
		upstream = s.DefaultUpstream
	}

	mw, err := mirror.New(s.MirrorRoot)
	if err != nil {
		return nil, fmt.Errorf("init mirror %s: %w", s.MirrorRoot, err)
	}
	if err := mw.EnsureRegistryJSON(); err != nil {
		return nil, fmt.Errorf("write bazel_registry.json: %w", err)
	}

	client := fetch.NewClient()

	// Probe source.json so we know the blob URL before opening the sink.
	src, err := client.GetSourceJSON(ctx, upstream, opts.Module, opts.Version)
	if err != nil {
		return nil, fmt.Errorf("source.json: %w", err)
	}
	sink, err := mw.BlobWriter(src.URL)
	if err != nil {
		return nil, fmt.Errorf("blob sink: %w", err)
	}

	mat, err := resolve.FromRegistryWithClient(ctx, client, upstream, opts.Module, opts.Version, resolve.Options{
		Tee:          sink,
		CaptureBytes: true,
	})
	if err != nil {
		sink.Abort()
		return nil, fmt.Errorf("resolve: %w", err)
	}
	defer mat.Cleanup()

	_, _, tarballBytes, err := sink.Close()
	if err != nil {
		return nil, fmt.Errorf("finalize blob: %w", err)
	}
	if err := mw.WriteSource(opts.Module, opts.Version, mat.SourceBytes); err != nil {
		return nil, fmt.Errorf("write source.json: %w", err)
	}
	if len(mat.ModuleBytes) > 0 {
		if err := mw.WriteModuleBazel(opts.Module, opts.Version, mat.ModuleBytes); err != nil {
			return nil, fmt.Errorf("write MODULE.bazel: %w", err)
		}
	}
	// Best-effort pull of upstream metadata.json so MergeMetadata
	// can lift homepage / maintainers / repository / yanked_versions
	// into the local file. A registry that omits metadata.json (or a
	// transient fetch failure) shouldn't fail the bump — log + carry
	// on with the version-only merge.
	upstreamMeta, metaErr := client.GetMetadataBytes(ctx, upstream, opts.Module)
	if metaErr != nil {
		slog.Debug("bump: upstream metadata.json fetch failed; proceeding without registry-level fields",
			"module", opts.Module, "upstream", upstream, "err", metaErr)
		upstreamMeta = nil
	}
	if err := mw.MergeMetadataWithUpstream(opts.Module, opts.Version, upstreamMeta); err != nil {
		return nil, fmt.Errorf("merge metadata.json: %w", err)
	}

	r, err := ingest.FromDir(ctx, s.store, mat.Dir)
	if err != nil {
		return nil, fmt.Errorf("assay+index: %w", err)
	}
	// Canonicalize coordinates: registry path wins over MODULE.bazel contents
	// (some upstream modules ship empty version strings).
	if r.Name != opts.Module || r.Version != opts.Version {
		r.Name = opts.Module
		r.Version = opts.Version
		if err := s.store.WriteReport(ctx, r); err != nil {
			return nil, fmt.Errorf("re-store under canonical coords: %w", err)
		}
	}
	// Tier-3 attrs hydration on the freshly-extracted tree. mat.Dir is
	// still rooted in tmpfs at this point (deferred mat.Cleanup runs
	// after this function returns), so the interpreter can resolve
	// load() chains against the same source tree assay just walked.
	// Persist again so the hydrated attrs land in the store along the
	// canonical coordinate.
	if s.AttrsInterpret {
		interp.Hydrate(ctx, mat.Dir, r)
		if err := s.store.WriteReport(ctx, r); err != nil {
			slog.Warn("post-hydrate report rewrite failed", "module", opts.Module, "version", opts.Version, "err", err)
		}
	}
	// Generate + store a SCIP index alongside the canonical
	// ModuleReport. Treated as best-effort: a failure here never
	// aborts a successful bump — SCIP is supplementary navigation
	// data, not part of the build-correctness guarantee. The error
	// gets logged so an operator can investigate without losing the
	// ingest's primary output.
	if scipBlob, scipErr := canopyscip.Generate(mat.Dir, opts.Module, opts.Version, r); scipErr == nil {
		if werr := s.store.WriteScipBlob(ctx, opts.Module, opts.Version, scipBlob); werr != nil {
			slog.Warn("scip blob write failed", "module", opts.Module, "version", opts.Version, "err", werr)
		} else if uerr := s.store.SetHasSourceIndex(ctx, opts.Module, opts.Version, scipBlobHasFiles(scipBlob)); uerr != nil {
			slog.Warn("set has_source_index failed", "module", opts.Module, "version", opts.Version, "err", uerr)
		}
	} else {
		slog.Warn("scip index generation failed", "module", opts.Module, "version", opts.Version, "err", scipErr)
	}
	// Persist the compressed tarball size so the per-version
	// header can render it as a chip. Best-effort: a failed UPDATE
	// here doesn't unwind the ingest (the report is already in).
	if tarballBytes > 0 {
		if err := s.store.SetTarballSize(ctx, opts.Module, opts.Version, tarballBytes); err != nil {
			slog.Warn("set tarball_size failed", "module", opts.Module, "version", opts.Version, "err", err)
		}
	}
	// Fire-and-forget GitHub-meta refresh. Detached from the
	// request ctx so a slow GitHub doesn't extend Bump latency or
	// cancel mid-flight if the client disconnects; bounded by a
	// fresh 15s timeout so a hanging socket can't pile up
	// goroutines. Failure is logged at debug and never affects the
	// bump outcome.
	if s.GitHubMeta != nil {
		module := opts.Module
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := s.RefreshGitHubMeta(bg, module); err != nil {
				slog.Debug("github meta refresh on bump failed", "module", module, "err", err)
			}
		}()
	}
	s.emit("module_indexed", eventFromReport(r))
	return r, nil
}

func (s *Service) IngestRecursive(ctx context.Context, opts api.IngestRecursiveOptions) (out *api.IngestRecursiveResult, retErr error) {
	t0 := time.Now()
	source := opts.Source
	if source == "" {
		source = "unknown"
	}
	defer func() {
		ev := store.AuditEvent{
			Kind:       "ingest_recursive_success",
			Source:     source,
			Module:     opts.Module,
			Version:    opts.Version,
			OK:         retErr == nil,
			DurationMs: time.Since(t0).Milliseconds(),
		}
		if retErr != nil {
			ev.Kind = "ingest_recursive_failure"
			ev.Error = retErr.Error()
		} else if out != nil {
			b, _ := json.Marshal(map[string]any{
				"visited":  out.Visited,
				"mirrored": out.Mirrored,
				"errors":   len(out.Errors),
			})
			ev.Payload = b
		}
		s.audit(ctx, ev)
	}()

	if s.MirrorRoot == "" {
		return nil, errors.New("ingest-recursive not available: canopy was started without --root pointing at a mirror tree")
	}
	if opts.Module == "" || opts.Version == "" {
		return nil, errors.New("ingest-recursive: module and version are required")
	}
	upstream := opts.Upstream
	if upstream == "" {
		upstream = s.DefaultUpstream
	}
	mw, err := mirror.New(s.MirrorRoot)
	if err != nil {
		return nil, fmt.Errorf("init mirror: %w", err)
	}

	bvForTools := ""
	if opts.IncludeBazelTools {
		bvForTools = opts.BazelVersion
		if bvForTools == "" {
			bvForTools = "9.1.0"
		}
	}

	res, err := ingest.RecursiveFromRegistry(ctx, upstream, opts.Module, opts.Version, ingest.RecursiveOptions{
		Mirror:               mw,
		BazelToolsForVersion: bvForTools,
		Workers:              opts.Workers,
		Bus:                  s.Bus,
	})
	if err != nil {
		return nil, err
	}

	out = &api.IngestRecursiveResult{
		Visited:  res.Visited,
		Mirrored: res.Mirrored,
	}
	for _, ev := range res.Errors {
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		out.Errors = append(out.Errors, api.RecursiveIngestErr{
			Module: ev.Module, Version: ev.Version, Error: msg,
		})
		// Surface each per-module failure as its own audit row so
		// /api/history?kind=ingest_module_failure exposes the precise
		// (module, version, error) triples — not just the aggregate
		// count baked into the outer ingest_recursive_* event. This
		// is the only mechanism that turns a noisy recursive walk
		// into queryable failure data after the run.
		s.audit(ctx, store.AuditEvent{
			Kind:    "ingest_module_failure",
			Source:  source,
			Module:  ev.Module,
			Version: ev.Version,
			OK:      false,
			Error:   msg,
		})
	}
	return out, nil
}
