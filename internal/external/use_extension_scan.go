package external

import (
	"fmt"

	ast "github.com/albertocavalcante/go-bzlmod-ast"
)

// UseExtensionSite is one `use_extension(...)` call site in a
// MODULE.bazel, with the tag-class invocations attached to its
// returned proxy.
//
// Used by canopy's cross-module index: every canopy-indexed
// consumer's MODULE.bazel contributes UseExtensionSites; when
// re-analyzing a producer ruleset (rules_go etc.) for its airgap
// surface, the producer's module_extension impls get driven with
// REAL tag values aggregated across the consumer corpus, rather
// than synthetic attr defaults.
type UseExtensionSite struct {
	// ExtensionFile is the apparent label of the .bzl declaring the
	// extension (e.g. "@rules_go//go:extensions.bzl"). Stored as a
	// string to keep this type JSON-friendly and decoupled from
	// go-bzlmod's label.ApparentLabel internal type.
	ExtensionFile string

	// ExtensionName is the bare identifier of the module_extension
	// global within ExtensionFile (e.g. "go_sdk").
	ExtensionName string

	// Tags are the proxy.<tag_class>(...) invocations attached to
	// this site, in source order.
	Tags []UseExtensionTag

	// DevDependency mirrors `use_extension(..., dev_dependency=True)`.
	// Dev-only sites typically shouldn't contribute to the production
	// airgap surface; the consumer of this index decides how to use
	// the flag.
	DevDependency bool

	// Isolate mirrors `use_extension(..., isolate=True)`. Isolated
	// extension usages don't share state across modules; the
	// resolver may want to handle these separately.
	Isolate bool
}

// UseExtensionTag is one tag_class invocation on a use_extension proxy.
type UseExtensionTag struct {
	Name  string         // The tag_class name (e.g. "download", "toolchain")
	Attrs map[string]any // Kwargs to the tag call, preserving Starlark types
}

// ScanUseExtensions parses a MODULE.bazel byte payload and returns
// every use_extension declaration plus the tag-class invocations
// attached to it.
//
// go-bzlmod-ast pre-links each ExtensionTagCall into its parent
// UseExtension.Tags as part of ParseContent — we just walk the
// top-level UseExtension statements and shape the result.
//
// Returns an empty slice (not nil) when the MODULE.bazel has no
// extensions. Parse errors propagate; the caller decides whether
// to log + skip or abort the ingest.
func ScanUseExtensions(moduleBazel []byte) ([]UseExtensionSite, error) {
	result, err := ast.ParseContent("MODULE.bazel", moduleBazel)
	if err != nil {
		return nil, fmt.Errorf("parse MODULE.bazel: %w", err)
	}
	if result == nil || result.File == nil {
		return []UseExtensionSite{}, nil
	}

	sites := make([]UseExtensionSite, 0)
	for _, stmt := range result.File.Statements {
		ue, ok := stmt.(*ast.UseExtension)
		if !ok {
			continue
		}
		site := UseExtensionSite{
			ExtensionFile: ue.ExtensionFile.String(),
			ExtensionName: ue.ExtensionName.String(),
			DevDependency: ue.DevDependency,
			Isolate:       ue.Isolate,
		}
		for _, tag := range ue.Tags {
			site.Tags = append(site.Tags, UseExtensionTag{
				Name:  tag.Name,
				Attrs: tag.Attributes,
			})
		}
		sites = append(sites, site)
	}
	return sites, nil
}
