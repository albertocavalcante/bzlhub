package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/albertocavalcante/assay/report"

	bazeldoc "github.com/albertocavalcante/bazel-doc-go"
	bzlsummary "github.com/albertocavalcante/bazel-module-summary-go"
	"github.com/albertocavalcante/bzlhub/internal/api"
	"github.com/albertocavalcante/bzlhub/internal/api/paths"
	"github.com/albertocavalcante/bzlhub/internal/docview"
	"github.com/albertocavalcante/bzlhub/internal/githubmeta"
	doc "github.com/albertocavalcante/starlark-doc-go"
)

func (h *handler) apiGetVersion(w http.ResponseWriter, r *http.Request) {
	module, version := paths.ModuleVersion(r)
	rep, err := h.c.GetModuleVersion(r.Context(), module, version)
	if err != nil {
		// GetReport returns a non-typed not-found right now; classify via message.
		if strings.Contains(err.Error(), "not found") {
			http.NotFound(w, r)
			return
		}
		h.apiError(w, err)
		return
	}
	// Augment the response with starlark-doc-go's parsed-docstring
	// view per symbol and (when the local mirror has one) the
	// registry-level metadata.json fields. Done at the API layer
	// (not at persistence) so re-parsing on read keeps the schema
	// lean and lets us iterate on the parser / metadata enrichment
	// without migrating SQLite blobs. Cost is sub-millisecond.
	writeJSON(w, http.StatusOK, h.augmentModuleResponse(r.Context(), rep, module))
}

// moduleResponseWithDocs wraps a *report.ModuleReport with the
// per-symbol parsed-docstring map, the registry-level metadata
// block, and the cross-corpus usage count. JSON-serializable
// directly; field order via inline embedding so all the existing
// report fields appear at the top level of the response.
type moduleResponseWithDocs struct {
	*report.ModuleReport
	// Each entry is a presentation-ready *docview.Doc: the
	// starlark-doc-go fields (Summary/Description/Args/...) plus
	// bazel-doc-go Refs already augmented with resolved Hrefs and
	// a deduplicated Chips list. Frontend iterates and renders;
	// it doesn't compute hrefs, dedup, or filter edge cases.
	ParsedDocs map[string]*docview.Doc      `json:"parsed_docs,omitempty"`
	Metadata   *bzlsummary.RegistryMetadata `json:"metadata,omitempty"`
	// UsageCount mirrors ModuleSummary.UsageCount on the listing
	// endpoint - number of OTHER indexed modules that reference
	// this one via bazel_dep, any version. Lets the detail page
	// surface the same popularity hint.
	UsageCount int `json:"usage_count,omitempty"`
	// TarballSize is the compressed source-tarball size in bytes.
	// Populated at Bump time; 0 for pre-migration ingests. The UI
	// renders this as a chip on the per-version header.
	TarballSize int64 `json:"tarball_size,omitempty"`
	// GitHubMeta is the cached social-signals payload (stars/forks/
	// languages) from the GitHub REST API. Nil when the refresher
	// hasn't fetched this module yet, when the module has no
	// github.com identity in its metadata, or when GitHub-meta is
	// disabled at the operator level.
	GitHubMeta *githubmeta.Meta `json:"github_meta,omitempty"`
	// Provenance records the upstream BCR git state captured at
	// Bump time. Nil for non-BCR upstreams or pre-I4 ingests.
	Provenance *api.BumpProvenance `json:"provenance,omitempty"`
}

// augmentModuleResponse builds the augmented response wrapper.
// Augmentations (parsed_docs, metadata, usage_count) are best-effort
// additions on top of the existing report; missing data leaves the
// field empty rather than failing the response.
func (h *handler) augmentModuleResponse(ctx context.Context, rep *report.ModuleReport, module string) any {
	if rep == nil {
		return rep
	}
	version := ""
	if rep.Version != "" {
		version = rep.Version
	}
	parsed := buildParsedDocs(rep, docview.Owner{Module: module, Version: version})
	meta := h.readRegistryMetadata(module)
	usage := 0
	var tarballSize int64
	var ghMeta *githubmeta.Meta
	var prov *api.BumpProvenance
	if h.helper != nil {
		// Read-side helpers live behind ReadHelper rather than
		// api.Canopy: those are detail-page augmentations, not
		// part of the cross-transport contract. Tests that pass a
		// nil helper see zero values everywhere; the response just
		// degrades to the plain report shape.
		if counts, err := h.helper.ComputeUsageCounts(ctx); err == nil {
			usage = counts[module]
		}
		if version != "" {
			if size, err := h.helper.GetTarballSize(ctx, module, version); err == nil {
				tarballSize = size
			}
			if p, err := h.helper.GetLatestBumpProvenance(ctx, module, version); err == nil {
				prov = p
			}
		}
		if m, err := h.helper.GetGitHubMeta(ctx, module); err == nil {
			ghMeta = m
		}
	}

	// Don't bloat the response when no augmentation produced anything:
	// old behavior (plain report) stays for clients that don't know
	// about the wrapper shape.
	if len(parsed) == 0 && meta == nil && usage == 0 && tarballSize == 0 && ghMeta == nil && prov == nil {
		return rep
	}
	return moduleResponseWithDocs{
		ModuleReport: rep,
		ParsedDocs:   parsed,
		Metadata:     meta,
		UsageCount:   usage,
		TarballSize:  tarballSize,
		GitHubMeta:   ghMeta,
		Provenance:   prov,
	}
}

// buildParsedDocs walks every doc-bearing symbol and returns a
// name -> ParsedDoc map. Symbols whose doc is empty are omitted so
// the response stays compact.
// canopyLinkResolver implements docview.LinkResolver using the same
// URL shapes the UI builds via ui/src/lib/links.ts. Keeping the
// templates here (not just on the frontend) is the price of
// shipping presentation-ready data: multiple clients (UI, CLI,
// MCP) get consistent URLs from one source.
type canopyLinkResolver struct{}

func (canopyLinkResolver) ModuleHref(name string) string {
	return "/modules/" + name
}

func (canopyLinkResolver) CodeNavFileHref(module, version, file string) string {
	return "/modules/" + module + "/" + version + "/code-nav/file/" + file
}

func buildParsedDocs(rep *report.ModuleReport, owner docview.Owner) map[string]*docview.Doc {
	parsed := map[string]*docview.Doc{}
	resolver := canopyLinkResolver{}
	add := func(name, body string) {
		if body == "" {
			return
		}
		// Three-step pipeline: starlark-doc-go produces the section
		// structure; bazel-doc-go overlays Bazel-aware reference
		// extraction (labels, xrefs) on top; docview resolves each
		// ref to a canopy URL and dedupes for the chip row so the
		// frontend just iterates and renders.
		v := docview.Build(bazeldoc.Enrich(doc.Parse(body)), owner, resolver)
		if v != nil {
			parsed[name] = v
		}
	}
	for _, r := range rep.Rules {
		add(r.Name, r.Doc)
	}
	for _, p := range rep.Providers {
		add(p.Name, p.Doc)
	}
	for _, m := range rep.Macros {
		add(m.Name, m.Doc)
	}
	for _, a := range rep.Aspects {
		add(a.Name, a.Doc)
	}
	for _, rr := range rep.RepositoryRules {
		add(rr.Name, rr.Doc)
	}
	for _, me := range rep.ModuleExtensions {
		add(me.Name, me.Doc)
	}
	if len(parsed) == 0 {
		return nil
	}
	return parsed
}

// readRegistryMetadata pulls the per-module metadata.json from the
// mirror tree (if any) and returns the RegistryMetadata subset.
// Returns nil when:
//   - the mirror isn't configured (no Options.MirrorRoot)
//   - the file doesn't exist (lib treats ErrNotExist as zero value;
//     we collapse zero value to nil to keep the response compact)
//   - parsing fails (logged; the response still goes out without
//     a metadata block rather than failing the whole request)
func (h *handler) readRegistryMetadata(module string) *bzlsummary.RegistryMetadata {
	if h.opts.MirrorRoot == "" {
		return nil
	}
	meta, err := bzlsummary.ReadMetadataJSON(
		mirrorMetadataPath(h.opts.MirrorRoot, module),
	)
	if err != nil {
		h.log.Debug("readRegistryMetadata: parse failed", "module", module, "err", err)
		return nil
	}
	// Collapse a fully-empty struct to nil so the JSON response stays
	// clean: no `"metadata": {}` entries on modules whose mirror
	// only has the version-stub metadata.json.
	if meta.Homepage == "" && len(meta.Maintainers) == 0 && len(meta.Repository) == 0 && len(meta.YankedVersions) == 0 {
		return nil
	}
	return meta
}

func mirrorMetadataPath(mirrorRoot, module string) string {
	return mirrorRoot + "/modules/" + module + "/metadata.json"
}
