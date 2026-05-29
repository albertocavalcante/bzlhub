// Package gobzlmod provides a Go library for Bazel module dependency resolution
// using Minimal Version Selection (MVS).
//
// This library implements pure MVS as described in Russ Cox's research
// (https://research.swtch.com/vgo-mvs), which selects the minimum version that
// satisfies all requirements in the dependency graph.
//
// # Overview
//
// The package provides three main components:
//
//   - Parser: Parses MODULE.bazel files to extract module information
//   - Registry: Fetches module metadata from Bazel Central Registry (BCR)
//   - Resolver: Resolves transitive dependencies using MVS or Bazel's selection algorithm
//
// # Quick Start
//
// The simplest way to resolve dependencies:
//
//	// From MODULE.bazel content
//	result, err := gobzlmod.Resolve(ctx, gobzlmod.ContentSource(content), gobzlmod.WithDevDeps())
//
//	// From a file
//	result, err := gobzlmod.Resolve(ctx, gobzlmod.FileSource("MODULE.bazel"))
//
//	// From a registry module
//	result, err := gobzlmod.Resolve(ctx, gobzlmod.RegistrySource{Name: "rules_go", Version: "0.50.0"})
//
//	// With options
//	result, err := gobzlmod.Resolve(ctx, gobzlmod.ContentSource(content),
//	    gobzlmod.WithRegistries("https://registry.example.com", gobzlmod.DefaultRegistry),
//	    gobzlmod.WithTimeout(30*time.Second),
//	)
//
// # Registry Configuration
//
// By default, the Bazel Central Registry (BCR) is used. For private registries:
//
//	result, err := gobzlmod.Resolve(ctx, src,
//	    gobzlmod.WithRegistries(
//	        "https://private.example.com",  // Try first
//	        gobzlmod.DefaultRegistry,        // Fall back to BCR
//	    ),
//	)
//
// # Yanked Version Handling
//
// The library supports detecting and handling yanked versions:
//
//	result, err := gobzlmod.Resolve(ctx, src,
//	    gobzlmod.WithYankedCheck(true),
//	    gobzlmod.WithYankedBehavior(gobzlmod.YankedVersionError),
//	)
//
// # Thread Safety
//
// All public types in this package are safe for concurrent use.
package gobzlmod

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
)

// ModuleSource represents a source of MODULE.bazel content for resolution.
// This is a marker interface implemented by ContentSource, FileSource, and RegistrySource.
type ModuleSource interface {
	moduleSource() // marker method
}

// ContentSource resolves from MODULE.bazel content provided as a string.
type ContentSource string

func (ContentSource) moduleSource() {}

// FileSource resolves from a MODULE.bazel file at the given path.
type FileSource string

func (FileSource) moduleSource() {}

// RegistrySource resolves a module from a registry by name and version.
type RegistrySource struct {
	Name    string
	Version string
}

func (RegistrySource) moduleSource() {}

// Resolve resolves dependencies from the given module source.
// This is the primary API for dependency resolution.
//
// Example usage:
//
//	// From MODULE.bazel content
//	result, err := Resolve(ctx, ContentSource(content), WithDevDeps())
//
//	// From a file
//	result, err := Resolve(ctx, FileSource("MODULE.bazel"), WithTimeout(30*time.Second))
//
//	// From a registry module
//	result, err := Resolve(ctx, RegistrySource{Name: "rules_go", Version: "0.50.0"})
func Resolve(ctx context.Context, src ModuleSource, opts ...Option) (*ResolutionList, error) {
	o, err := applyOptions(opts...)
	if err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	switch s := src.(type) {
	case ContentSource:
		return resolveInternal(ctx, string(s), o)
	case FileSource:
		return ResolveFile(ctx, string(s), o)
	case RegistrySource:
		return resolveModuleInternal(ctx, s.Name, s.Version, o)
	default:
		return nil, fmt.Errorf("unsupported module source type: %T", src)
	}
}

// ResolveContent resolves dependencies from MODULE.bazel content.
//
// Deprecated: Use Resolve with ContentSource instead.
//
// Uses BCR by default if opts.Registries is empty.
func ResolveContent(ctx context.Context, moduleContent string, opts ResolutionOptions) (*ResolutionList, error) {
	return resolveInternal(ctx, moduleContent, opts)
}

// resolveInternal is the internal implementation for content-based resolution.
func resolveInternal(ctx context.Context, moduleContent string, opts ResolutionOptions) (*ResolutionList, error) {
	moduleInfo, err := ParseModuleContent(moduleContent)
	if err != nil {
		return nil, fmt.Errorf("parse module content: %w", err)
	}

	reg := buildRegistry(opts)
	return (&selectionResolver{registry: reg, options: opts}).Resolve(ctx, moduleInfo)
}

// ResolveFile resolves dependencies from a MODULE.bazel file.
//
// Deprecated: Use Resolve with FileSource instead.
//
// Uses BCR by default if opts.Registries is empty.
func ResolveFile(ctx context.Context, moduleFilePath string, opts ResolutionOptions) (*ResolutionList, error) {
	moduleInfo, err := ParseModuleFile(moduleFilePath)
	if err != nil {
		return nil, fmt.Errorf("parse module file: %w", err)
	}

	// Resolve relative local_path_override paths against the MODULE.bazel file location.
	resolveLocalPathOverrides(moduleInfo, moduleFilePath)

	reg := buildRegistry(opts)
	return (&selectionResolver{registry: reg, options: opts}).Resolve(ctx, moduleInfo)
}


// ResolveModule resolves a module from the registry and returns its complete dependency graph.
//
// Deprecated: Use Resolve with RegistrySource instead.
//
// Unlike Resolve/ResolveFile which resolve dependencies OF a root module (excluding the root),
// ResolveModule resolves a specific module BY its coordinate and includes it in the result.
//
// The target module appears first with Depth=0. Its direct dependencies have Depth=1, etc.
//
// This is useful for:
//   - Exploring what a module will bring into your project
//   - Pre-fetching all modules needed before adding a dependency
//   - Analyzing the complete dependency tree of any BCR module
//
// Example:
//
//	result, err := gobzlmod.ResolveModule(ctx, "rules_go", "0.50.0", opts)
//	// result.Modules[0] is rules_go@0.50.0 (Depth=0)
//	// result.Modules[1:] are its transitive dependencies (Depth>=1)
func ResolveModule(ctx context.Context, name, version string, opts ResolutionOptions) (*ResolutionList, error) {
	return resolveModuleInternal(ctx, name, version, opts)
}

// resolveModuleInternal is the internal implementation for registry-based resolution.
func resolveModuleInternal(ctx context.Context, name, version string, opts ResolutionOptions) (*ResolutionList, error) {
	reg := buildRegistry(opts)
	resolver := &selectionResolver{registry: reg, options: opts}

	// Fetch the module's MODULE.bazel from registry
	moduleInfo, err := reg.GetModuleFile(ctx, name, version)
	if err != nil {
		return nil, fmt.Errorf("fetch module %s@%s: %w", name, version, err)
	}

	// Ensure the module info has the correct name/version
	// (Some MODULE.bazel files may not have module() directive)
	if moduleInfo.Name == "" {
		moduleInfo.Name = name
	}
	if moduleInfo.Version == "" {
		moduleInfo.Version = version
	}

	// Resolve dependencies (treats moduleInfo as root)
	result, err := resolver.Resolve(ctx, moduleInfo)
	if err != nil {
		return nil, fmt.Errorf("resolve dependencies for %s@%s: %w", name, version, err)
	}


	// Determine the registry URL for the target module
	registryURL := reg.BaseURL()
	if chain, ok := reg.(*registryChain); ok {
		if moduleReg := chain.GetRegistryForModule(name); moduleReg != "" {
			registryURL = moduleReg
		}
	}

	// Build the target module's direct dependencies list (modules with Depth=1)
	var directDeps []string
	for _, m := range result.Modules {
		if m.Depth == 1 {
			directDeps = append(directDeps, m.Name)
		}
	}
	slices.Sort(directDeps)

	// Create the target module entry
	targetModule := ModuleToResolve{
		Name:         name,
		Version:      version,
		Registry:     registryURL,
		Depth:        0,
		Dependencies: directDeps,
		RequiredBy:   nil, // Root module isn't required by anything
	}

	// Apply metadata checks to the target module as well.
	if opts.CheckYanked || opts.WarnDeprecated {
		targetList := &ResolutionList{Modules: []ModuleToResolve{targetModule}}
		checkModuleMetadata(ctx, reg, opts, targetList)
		targetModule = targetList.Modules[0]
	}

	// Apply Bazel compatibility checks to the target module as well.
	if opts.BazelCompatibilityMode != BazelCompatibilityOff &&
		opts.BazelVersion != "" &&
		len(moduleInfo.BazelCompatibility) > 0 {
		targetModule.BazelCompatibility = moduleInfo.BazelCompatibility
		compatible, reason, _ := checkBazelCompatibility(opts.BazelVersion, moduleInfo.BazelCompatibility)
		if !compatible {
			targetModule.IsBazelIncompatible = true
			targetModule.BazelIncompatibilityReason = reason
		}
	}

	// Insert target module, recompute summary, and apply behaviors.
	// Use a single-module list for the target so shared functions don't
	// duplicate warnings already emitted for the resolved modules.
	result.Modules = append(result.Modules, targetModule)
	slices.SortFunc(result.Modules, func(a, b ModuleToResolve) int {
		return cmp.Compare(a.Name, b.Name)
	})
	computeSummary(result)

	targetList := &ResolutionList{Modules: []ModuleToResolve{targetModule}}
	computeSummary(targetList)
	if err := applyYankedBehavior(targetList, opts); err != nil {
		return nil, err
	}
	applyDeprecatedWarnings(targetList, opts)
	if err := applyBazelCompatBehavior(targetList, opts); err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, targetList.Warnings...)

	return result, nil
}

// resolveLocalPathOverrides resolves relative local_path_override paths
// against the MODULE.bazel file location. This ensures the selection
// resolver can find local override modules regardless of working directory.
func resolveLocalPathOverrides(moduleInfo *ModuleInfo, moduleFilePath string) {
	baseDir := filepath.Dir(moduleFilePath)
	for i := range moduleInfo.Overrides {
		o := &moduleInfo.Overrides[i]
		if o.Type == overrideTypeLocalPath && o.Path != "" && !filepath.IsAbs(o.Path) {
			o.Path = filepath.Join(baseDir, o.Path)
		}
	}
}

// registryFromOptions creates a registry from ResolutionOptions.
// Uses BCR if no registries are specified.
func registryFromOptions(opts ResolutionOptions) Registry {
	if len(opts.Registries) == 0 {
		return registryWithAllOptions(opts.HTTPClient, opts.Cache, opts.Timeout, opts.Logger)
	}
	return registryWithAllOptions(opts.HTTPClient, opts.Cache, opts.Timeout, opts.Logger, opts.Registries...)
}
