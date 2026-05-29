package canopy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"

	assayext "github.com/albertocavalcante/assay/interp/external"
	"github.com/albertocavalcante/assay/report"
	"github.com/albertocavalcante/starlark-go-bazel/conv"
	bazelctx "github.com/albertocavalcante/starlark-go-bazel/ctx"
	"go.starlark.net/starlark"

	"github.com/albertocavalcante/canopy/internal/api"
)

// AirgapSurface unions ExternalSurface across the dep closure of
// (name, version). The closure walk reuses Service.Closure's logic.
// Per-node Refs are flattened + deduplicated by (URL, platform, file)
// for the closure-wide list; per-node counts stay in Modules[i] so
// the UI can drill into "which module contributed this class."
func (s *Service) AirgapSurface(ctx context.Context, name, version string) (*api.ClosureSurfaceResponse, error) {
	closure, err := s.Closure(ctx, name, version)
	if err != nil {
		return nil, fmt.Errorf("closure: %w", err)
	}

	resp := &api.ClosureSurfaceResponse{
		Root:            closure.Root,
		Modules:         make([]api.ClosureSurfaceModule, 0, len(closure.Nodes)),
		ClassCounts:     map[string]int{},
		MaxDepthReached: closure.MaxDepthReached,
	}

	// Dedupe by (URL, Platform, File) across the closure; first-seen
	// metadata wins so root-module provenance takes precedence over
	// transitive duplicates.
	type refKey struct{ url, platform, file string }
	seenRef := map[refKey]bool{}

	for _, node := range closure.Nodes {
		mod := api.ClosureSurfaceModule{
			Module:   node.Name,
			Version:  node.Version,
			External: node.External,
		}
		if node.External {
			// Closure points outside canopy's index — show the node
			// but don't query (would 404).
			resp.Modules = append(resp.Modules, mod)
			resp.MissingModules = append(resp.MissingModules, node.Name+"@"+node.Version)
			continue
		}
		sub, err := s.ExternalSurface(ctx, node.Name, node.Version)
		if err != nil {
			// Non-fatal: surface the module row with zero refs so the
			// UI knows it's part of the closure even if its surface
			// couldn't be assembled.
			resp.Modules = append(resp.Modules, mod)
			continue
		}
		mod.ClassCounts = sub.ClassCounts
		mod.RefCount = len(sub.Refs)
		resp.Modules = append(resp.Modules, mod)

		nodeID := node.Name + "@" + node.Version
		for _, r := range sub.Refs {
			k := refKey{r.URL, r.Platform, r.File}
			if seenRef[k] {
				continue
			}
			seenRef[k] = true
			r.SourceModule = nodeID
			resp.Refs = append(resp.Refs, r)
			if r.Class != "" {
				resp.ClassCounts[r.Class]++
			}
		}
		resp.ForkErrors = append(resp.ForkErrors, sub.ForkErrors...)
	}

	return resp, nil
}

// confidenceFor derives the API-response "Confidence" field from the
// stored Tainted + Platform values. Three-bucket classification —
// see api.ExternalRef.Confidence for the contract.
func confidenceFor(tainted bool, platform string) string {
	switch {
	case tainted:
		return "tainted"
	case platform != "" && platform != api.DefaultPlatform:
		return "platform-specific"
	default:
		return "resolved"
	}
}

// extensionApparentLabel reconstructs the apparent extension label
// in the form "@<module>//<dir>:<file>.bzl" from a producer's module
// name and the extension's provenance file path. Matches the form
// stored in the cross-module corpus index (from ast.UseExtension's
// apparent-label string).
//
// Example: module="rules_go", provenanceFile="go/extensions.bzl"
//
//	→ "@rules_go//go:extensions.bzl"
//
// Edge cases: empty dir (file at module root) → "@<module>//:basename".
func extensionApparentLabel(module, provenanceFile string) string {
	if module == "" || provenanceFile == "" {
		return ""
	}
	dir, base := path.Split(provenanceFile)
	dir = strings.TrimSuffix(dir, "/")
	return "@" + module + "//" + dir + ":" + base
}

// driveCorpusURLs re-drives the producer's module_extension impls
// with consumer-derived ModuleSpecs and returns the URLs they would
// generate. Reads the extension-impl .bzl source from the store (saved
// at ingest), builds ModuleSpec per consumer, and invokes the
// extension via assayext.DriveExtensionFromSource.
//
// Returns empty when:
//   - No extension-impl source was persisted at ingest (older entries).
//   - The .bzl source fails to parse or eval (logged as a fork error).
//   - The extension impl produces no URLs for the supplied specs.
func (s *Service) driveCorpusURLs(ctx context.Context, rep *report.ModuleReport, usages []api.ExtensionCorpusUsage) []api.ExternalRef {
	srcs, err := s.store.GetModuleExtensionSources(ctx, rep.Name, rep.Version)
	if err != nil || len(srcs) == 0 {
		return nil
	}
	srcByFile := make(map[string][]byte, len(srcs))
	for _, src := range srcs {
		srcByFile[src.File] = src.Content
	}

	var out []api.ExternalRef
	for _, cu := range usages {
		// Find which Provenance.File this extension comes from.
		var extFile string
		for _, ext := range rep.ModuleExtensions {
			if ext.Name == cu.ExtensionName {
				extFile = ext.Provenance.File
				break
			}
		}
		if extFile == "" {
			continue
		}
		source, ok := srcByFile[extFile]
		if !ok {
			continue
		}
		specs := buildModuleSpecsFromConsumers(cu.Consumers)
		if len(specs) == 0 {
			continue
		}
		driveResult, err := assayext.DriveExtensionFromSource(
			ctx, source, extFile, cu.ExtensionName, specs, assayext.Options{},
		)
		if err != nil {
			slog.Debug("driveCorpusURLs: extension drive failed",
				"module", rep.Name, "version", rep.Version,
				"extension", cu.ExtensionName, "err", err)
			continue
		}
		for _, ref := range driveResult.Refs {
			out = append(out, api.ExternalRef{
				URL: ref.URL, Host: ref.Host, Class: ref.Class,
				Mutability: ref.Mutability, SHA256: ref.SHA256, Integrity: ref.Integrity,
				APIName:    "corpus:" + cu.ExtensionName,
				RuleName:   ref.RuleName,
				Platform:   ref.Platform,
				Tainted:    ref.Tainted,
				File:       ref.File,
				Confidence: confidenceFor(ref.Tainted, ref.Platform),
			})
		}
	}
	return out
}

// buildModuleSpecsFromConsumers groups ExtensionConsumerCall entries
// by consumer (module, version) and builds the bazelctx.ModuleSpec
// shape the driver expects.
//
// IsRoot=false for all consumers: in a real Bazel build, the root
// module is the WORKSPACE running the build, not any of the producer
// ruleset's transitive consumers. We're synthesizing an analysis
// context that doesn't correspond to any one consumer's workspace —
// flagging an arbitrary consumer as root would mis-fire any extension
// impl that branches on `mod.is_root` (rules_python, rules_jvm_external,
// and others do).
//
// Trade-off: extensions whose impl ONLY emits output for the root
// module's tags will produce nothing here. That's an acceptable
// under-report; the alternative (arbitrary root assignment) is
// over-reporting + wrong attribution.
func buildModuleSpecsFromConsumers(consumers []api.ExtensionConsumerCall) []bazelctx.ModuleSpec {
	byConsumer := map[string]*bazelctx.ModuleSpec{}
	consumerOrder := []string{}
	for _, c := range consumers {
		key := c.ConsumerModule + "@" + c.ConsumerVersion
		spec, ok := byConsumer[key]
		if !ok {
			spec = &bazelctx.ModuleSpec{
				Name:    c.ConsumerModule,
				Version: c.ConsumerVersion,
				IsRoot:  false,
				Tags:    map[string][]bazelctx.TagInstance{},
			}
			byConsumer[key] = spec
			consumerOrder = append(consumerOrder, key)
		}
		spec.Tags[c.TagName] = append(spec.Tags[c.TagName], bazelctx.TagInstance{
			Attrs: anyMapToStarlark(c.TagAttrs),
		})
	}
	out := make([]bazelctx.ModuleSpec, 0, len(byConsumer))
	for _, key := range consumerOrder {
		out = append(out, *byConsumer[key])
	}
	return out
}

// anyMapToStarlark converts JSON-decoded map[string]any into
// map[string]starlark.Value for the driver. Delegates element
// conversion to conv.FromGo (canonical Go-any → starlark.Value
// converter; see starlark-go-bazel/conv).
func anyMapToStarlark(attrs map[string]any) map[string]starlark.Value {
	if attrs == nil {
		return nil
	}
	out := make(map[string]starlark.Value, len(attrs))
	for k, v := range attrs {
		out[k] = conv.FromGo(v)
	}
	return out
}

// corpusUsagesForExtensions queries the cross-module use_extension
// index for every extension declared by (name, version) and groups
// the results by extension. Returns nil when the module declares no
// extensions or none have any consumers in the corpus.
func (s *Service) corpusUsagesForExtensions(ctx context.Context, rep *report.ModuleReport) []api.ExtensionCorpusUsage {
	if rep == nil || len(rep.ModuleExtensions) == 0 {
		return nil
	}
	var out []api.ExtensionCorpusUsage
	for _, ext := range rep.ModuleExtensions {
		label := extensionApparentLabel(rep.Name, ext.Provenance.File)
		if label == "" {
			continue
		}
		usages, err := s.store.GetUseExtensionUsagesForExtension(ctx, label, ext.Name)
		if err != nil || len(usages) == 0 {
			continue
		}
		entry := api.ExtensionCorpusUsage{
			ExtensionFile: label,
			ExtensionName: ext.Name,
		}
		for _, u := range usages {
			var attrs map[string]any
			if u.TagAttrsJSON != "" {
				_ = json.Unmarshal([]byte(u.TagAttrsJSON), &attrs)
			}
			entry.Consumers = append(entry.Consumers, api.ExtensionConsumerCall{
				ConsumerModule:  u.ConsumerModule,
				ConsumerVersion: u.ConsumerVersion,
				TagName:         u.TagName,
				TagAttrs:        attrs,
				DevDependency:   u.DevDependency,
				Isolate:         u.Isolate,
			})
		}
		out = append(out, entry)
	}
	return out
}

// ExternalSurface assembles the per-module URL inventory from the
// store and computes class-counts for chip rendering. Mirrors
// api.Canopy.ExternalSurface. The store rows arrive class-sorted, so
// no additional sort is needed for stable output.
func (s *Service) ExternalSurface(ctx context.Context, name, version string) (*api.ExternalSurfaceResponse, error) {
	refs, err := s.store.GetExternalRefs(ctx, name, version)
	if err != nil {
		return nil, err
	}
	forkErrs, err := s.store.GetExternalForkErrors(ctx, name, version)
	if err != nil {
		return nil, err
	}

	apiRefs := make([]api.ExternalRef, 0, len(refs))
	counts := map[string]int{}
	for _, r := range refs {
		apiRefs = append(apiRefs, api.ExternalRef{
			URL: r.URL, Host: r.Host, Class: r.Class,
			Mutability: r.Mutability, SHA256: r.SHA256, Integrity: r.Integrity,
			APIName: r.APIName, RuleName: r.RuleName,
			Platform: r.Platform, Tainted: r.Tainted, File: r.File,
			Confidence: confidenceFor(r.Tainted, r.Platform),
		})
		if r.Class != "" {
			counts[r.Class]++
		}
	}
	apiErrs := make([]api.ExternalForkError, 0, len(forkErrs))
	for _, e := range forkErrs {
		apiErrs = append(apiErrs, api.ExternalForkError{Platform: e.Platform, Message: e.Message})
	}
	resp := &api.ExternalSurfaceResponse{
		Module: name, Version: version,
		Refs: apiRefs, ForkErrors: apiErrs, ClassCounts: counts,
	}
	// Augment with corpus-side use_extension usages when the module
	// has indexed extensions AND any consumers in the corpus pin tag
	// values on them. Non-fatal: GetReport may fail (e.g., module
	// blob is stale) — fall through with refs-only.
	if rep, err := s.store.GetReport(ctx, name, version); err == nil {
		resp.CorpusUsages = s.corpusUsagesForExtensions(ctx, rep)
		// When corpus usages exist AND the producer's extension-impl
		// .bzl source was persisted at ingest, drive the extensions
		// with consumer-derived ModuleSpecs and merge the resulting
		// URLs into the Refs list. Each corpus-derived ref carries
		// APIName="corpus" so the UI can distinguish it.
		if len(resp.CorpusUsages) > 0 {
			corpusRefs := s.driveCorpusURLs(ctx, rep, resp.CorpusUsages)
			for _, r := range corpusRefs {
				resp.Refs = append(resp.Refs, r)
				if r.Class != "" {
					if resp.ClassCounts == nil {
						resp.ClassCounts = map[string]int{}
					}
					resp.ClassCounts[r.Class]++
				}
			}
		}
	}
	return resp, nil
}
