package external

import (
	"fmt"

	"github.com/albertocavalcante/go-bzlmod/ast"
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
// Walks `result.File.Statements` directly rather than `ast.Walk`
// because go-bzlmod's parser emits `*ast.ExtensionTagCall` as
// separate top-level statements (not nested in `UseExtension.Tags`).
// We link each ExtensionTagCall to its UseExtension by matching
// ExtensionTagCall.Extension (the LHS variable in the source) to
// UseExtension.Variable (added upstream specifically for this).
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

	var sites []UseExtensionSite
	// indexByVar maps the LHS variable name → index into sites, so
	// ExtensionTagCall.Extension lookups are O(1).
	indexByVar := map[string]int{}

	for _, stmt := range result.File.Statements {
		switch s := stmt.(type) {
		case *ast.UseExtension:
			sites = append(sites, UseExtensionSite{
				ExtensionFile: s.ExtensionFile.String(),
				ExtensionName: s.ExtensionName.String(),
				DevDependency: s.DevDependency,
				Isolate:       s.Isolate,
			})
			if s.Variable != "" {
				indexByVar[s.Variable] = len(sites) - 1
			}

		case *ast.ExtensionTagCall:
			idx, ok := indexByVar[s.Extension]
			if !ok {
				// Tag call references an unknown extension variable —
				// usually means the MODULE.bazel is malformed or
				// references a use_extension declared in another
				// MODULE block. Skip silently; bookkeeping not the
				// caller's concern.
				continue
			}
			sites[idx].Tags = append(sites[idx].Tags, UseExtensionTag{
				Name:  s.TagName,
				Attrs: s.Attributes,
			})
		}
	}
	if sites == nil {
		return []UseExtensionSite{}, nil
	}
	return sites, nil
}
