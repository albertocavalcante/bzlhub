// Package external connects assay's external-surface analyzer to
// canopy's SQLite store. IngestModule is the single entry point;
// callers from canopy/internal/ingest invoke it post-WriteReport.
package external

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	assayext "github.com/albertocavalcante/assay/interp/external"
	"github.com/albertocavalcante/assay/report"

	"github.com/albertocavalcante/canopy/internal/store"
)

// IngestModule runs assay's external.Analyze against the ingested
// module's tree at moduleDir, then persists results to s. Idempotent
// per (module, version).
//
// Restricts analysis to files that assay's bzlwalk pass already
// flagged as containing repository_rule / module_extension
// definitions — skipping test/example .bzl files that have no
// rules worth driving. On rulesets like rules_go (~150 .bzl files,
// ~10 with rules), this is a 10x+ win in ingest wall time.
func IngestModule(ctx context.Context, s *store.Store, moduleDir string, rep *report.ModuleReport) error {
	res, err := assayext.Analyze(ctx, moduleDir, assayext.Options{
		RelevantFiles: relevantFiles(rep),
	})
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	refs := make([]store.ExternalRef, 0, len(res.Refs))
	for _, r := range res.Refs {
		refs = append(refs, store.ExternalRef{
			URL:        r.URL,
			Host:       r.Host,
			Class:      r.Class,
			Mutability: r.Mutability,
			SHA256:     r.SHA256,
			Integrity:  r.Integrity,
			APIName:    r.APIName,
			RuleName:   r.RuleName,
			Platform:   r.Platform,
			Tainted:    r.Tainted,
			File:       r.File,
		})
	}

	forkErrs := make([]store.ExternalForkError, 0, len(res.ForkErrors))
	for _, fe := range res.ForkErrors {
		platform := fe.Platform.Label()
		msg := ""
		if fe.Err != nil {
			msg = fe.Err.Error()
		}
		forkErrs = append(forkErrs, store.ExternalForkError{Platform: platform, Message: msg})
	}

	if err := s.WriteExternalRefs(ctx, rep.Name, rep.Version, refs, forkErrs); err != nil {
		return err
	}

	// Scan the consumer's MODULE.bazel for use_extension call sites
	// and store them in the cross-module index. Non-fatal: a missing
	// or malformed MODULE.bazel just skips this contribution to the
	// corpus index without invalidating the external_refs we just
	// wrote.
	usages, scanErr := scanMODULEBazelUsages(moduleDir)
	if scanErr != nil {
		slog.Debug("scanMODULEBazelUsages: skipped",
			"module", rep.Name, "version", rep.Version, "err", scanErr)
	} else if err := s.WriteUseExtensionUsages(ctx, rep.Name, rep.Version, usages); err != nil {
		return fmt.Errorf("write use_extension usages: %w", err)
	}

	// Persist the .bzl source of every file declaring a module_extension.
	// Query-time corpus re-drive (Service.ExternalSurface) reads these
	// back instead of re-fetching the producer's tarball.
	if sources := readExtensionSources(moduleDir, rep); len(sources) > 0 || rep.ModuleExtensions != nil {
		if err := s.WriteModuleExtensionSources(ctx, rep.Name, rep.Version, sources); err != nil {
			return fmt.Errorf("write module_extension sources: %w", err)
		}
	}
	return nil
}

// readExtensionSources reads each .bzl file referenced by the report's
// ModuleExtensions Provenance, preserving the content for later re-eval.
// Files outside moduleDir are rejected — Provenance.File is producer-
// supplied content and could in principle name `../../etc/passwd`.
// Read errors are logged at Debug level so stale-ingest debugging
// doesn't require code edits.
func readExtensionSources(moduleDir string, rep *report.ModuleReport) []store.ModuleExtensionSource {
	if rep == nil || len(rep.ModuleExtensions) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []store.ModuleExtensionSource
	for _, ext := range rep.ModuleExtensions {
		file := ext.Provenance.File
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		// IsLocal rejects paths containing ".." or starting with "/"
		// (anything that could escape moduleDir).
		if !filepath.IsLocal(file) {
			slog.Debug("readExtensionSources: rejecting non-local path",
				"module", rep.Name, "version", rep.Version, "file", file)
			continue
		}
		data, err := os.ReadFile(filepath.Join(moduleDir, file))
		if err != nil {
			slog.Debug("readExtensionSources: read failed",
				"module", rep.Name, "version", rep.Version, "file", file, "err", err)
			continue
		}
		out = append(out, store.ModuleExtensionSource{File: file, Content: data})
	}
	return out
}

// scanMODULEBazelUsages reads moduleDir/MODULE.bazel (when present),
// extracts every use_extension declaration + its tag calls, and maps
// them to the store's row shape. Returns nil + nil when no MODULE.bazel
// exists; that's fine — the consumer just contributes nothing to the
// cross-module index.
func scanMODULEBazelUsages(moduleDir string) ([]store.UseExtensionUsage, error) {
	path := filepath.Join(moduleDir, "MODULE.bazel")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read MODULE.bazel: %w", err)
	}
	sites, err := ScanUseExtensions(data)
	if err != nil {
		return nil, err
	}

	// One store row per (site, tag). Sites without tags contribute
	// nothing to the tag-aggregation use case and aren't written.
	var out []store.UseExtensionUsage
	for _, site := range sites {
		for idx, tag := range site.Tags {
			attrsJSON, jerr := json.Marshal(tag.Attrs)
			if jerr != nil {
				// Malformed attrs shouldn't poison the whole site;
				// fall back to an empty object so the row still
				// represents "the tag was called."
				attrsJSON = []byte(`{}`)
			}
			out = append(out, store.UseExtensionUsage{
				ExtensionFile: site.ExtensionFile,
				ExtensionName: site.ExtensionName,
				TagIndex:      idx,
				TagName:       tag.Name,
				TagAttrsJSON:  string(attrsJSON),
				DevDependency: site.DevDependency,
				Isolate:       site.Isolate,
			})
		}
	}
	return out, nil
}

// relevantFiles returns the deduplicated workspace-relative paths of
// every .bzl file that carries a repository_rule or module_extension
// declaration per assay's bzlwalk pass. Empty when the report has no
// rules to drive, which short-circuits Analyze to a no-op.
func relevantFiles(rep *report.ModuleReport) []string {
	if rep == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(rep.RepositoryRules)+len(rep.ModuleExtensions))
	for _, r := range rep.RepositoryRules {
		if f := r.Provenance.File; f != "" {
			seen[f] = struct{}{}
		}
	}
	for _, e := range rep.ModuleExtensions {
		if f := e.Provenance.File; f != "" {
			seen[f] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	return out
}
