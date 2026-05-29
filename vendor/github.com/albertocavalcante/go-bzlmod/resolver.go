package gobzlmod

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/albertocavalcante/go-bzlmod/bazeltools"
	"github.com/albertocavalcante/go-bzlmod/graph"
	"github.com/albertocavalcante/go-bzlmod/internal/compat"
	"github.com/albertocavalcante/go-bzlmod/selection"
	"github.com/albertocavalcante/go-bzlmod/selection/version"
)

const (
	defaultMaxConcurrency            = 5
	builtinBazelToolsModule          = "bazel_tools"
	builtinLocalConfigPlatformModule = "local_config_platform"

	overrideTypeSingleVersion = "single_version"
	overrideTypeMultiple      = "multiple_version"
	overrideTypeGit           = "git"
	overrideTypeLocalPath     = "local_path"
	overrideTypeArchive       = "archive"
)

func includeVisibleLocalConfigPlatform(bazelVersion string) bool {
	if bazelVersion == "" {
		return false
	}
	return version.Compare(bazelVersion, "9.0.0") < 0
}

func isNotFound(err error) bool {
	var regErr *RegistryError
	return errors.As(err, &regErr) && regErr.StatusCode == http.StatusNotFound
}

// bazelToolsRootDeps returns Bazel's built-in MODULE.tools dependencies as
// implicit root deps. They participate in selection but are hidden from the
// default visible graph.
func bazelToolsRootDeps(
	bazelVersion string,
	lookup BazelToolsLookup,
	transformer BazelToolsTransformer,
) []Dependency {
	if lookup == nil {
		lookup = bazeltools.LookupDeps
	}
	deps := lookup(bazelVersion)
	if transformer != nil {
		deps = transformer(bazelVersion, slices.Clone(deps))
	}
	if deps == nil {
		return nil
	}
	rootDeps := make([]Dependency, 0, len(deps))
	for _, toolDep := range deps {
		rootDeps = append(rootDeps, Dependency{
			Name:       toolDep.Name,
			Version:    toolDep.Version,
			IsNodepDep: true,
		})
	}
	return rootDeps
}

// checkFieldCompatibility checks if bzlmod fields used in the root module are
// compatible with the target Bazel version.
func checkFieldCompatibility(rootModule *ModuleInfo, bazelVersion string) []string {
	if bazelVersion == "" {
		return nil
	}
	var warnings []string
	for _, dep := range rootModule.Dependencies {
		if dep.MaxCompatibilityLevel > 0 {
			if w := compat.CheckField(bazelVersion, "max_compatibility_level"); w != nil {
				warnings = append(warnings,
					fmt.Sprintf("bazel_dep(%s): %s", dep.Name, w.String()))
				break
			}
		}
	}
	return warnings
}

// selectionResolver resolves dependencies using Bazel's complete selection algorithm.
// This is the sole resolution engine used by all public API functions.
type selectionResolver struct {
	registry Registry
	options  ResolutionOptions
}

// buildRegistry constructs a Registry from ResolutionOptions.
// Uses BCR as default when no registries are configured.
func buildRegistry(opts ResolutionOptions) Registry {
	urls := opts.Registries
	if len(urls) == 0 {
		urls = DefaultRegistries
	}
	return registryWithAllOptionsAndTrace(
		opts.HTTPClient,
		opts.Cache,
		opts.Timeout,
		opts.Logger,
		newRegistryTraceIfEnabled(opts.TraceRegistryFiles),
		urls...,
	)
}

// Resolve performs dependency resolution using Bazel's selection algorithm.
// It returns a ResolutionList with the resolved modules and optionally an
// unpruned view for debugging.
func (r *selectionResolver) Resolve(ctx context.Context, rootModule *ModuleInfo) (*ResolutionList, error) {
	if rootModule == nil {
		return nil, fmt.Errorf("root module is nil")
	}

	// Phase 1: Build the raw dependency graph by fetching all transitive deps
	depGraph, moduleInfoCache, err := r.buildDepGraph(ctx, rootModule)
	if err != nil {
		return nil, fmt.Errorf("build dependency graph: %w", err)
	}

	// Phase 2: Convert overrides to selection package format
	overrides := convertOverrides(rootModule.Overrides)

	// Phase 3: Run Bazel's selection algorithm
	result, err := selection.Run(depGraph, overrides)
	if err != nil {
		return nil, fmt.Errorf("selection algorithm: %w", err)
	}

	// Phase 4: Convert result to ResolutionList
	return r.buildResult(ctx, result, rootModule, moduleInfoCache)
}

// buildDepGraph fetches all transitive dependencies and builds a selection.DepGraph.
// It also returns a moduleInfoCache mapping "name@version" to *ModuleInfo for modules
// that declare bazel_compatibility constraints, enabling post-resolution compat checks.
func (r *selectionResolver) buildDepGraph(ctx context.Context, rootModule *ModuleInfo) (*selection.DepGraph, map[string]*ModuleInfo, error) {
	modules := make(map[selection.ModuleKey]*selection.Module)
	moduleInfoCache := make(map[string]*ModuleInfo)
	overrideMap := overrideIndex(rootModule.Overrides)

	buildDepSpecs := func(deps []Dependency, isRoot bool) []selection.DepSpec {
		specs := make([]selection.DepSpec, 0, len(deps))
		for _, dep := range deps {
			// Match Bazel: non-root modules always ignore dev dependencies.
			if dep.DevDependency && (!isRoot || !r.options.IncludeDevDeps) {
				continue
			}

			depVersion := dep.Version
			if override, ok := overrideMap[dep.Name]; ok {
				switch override.Type {
				case overrideTypeSingleVersion:
					if override.Version != "" {
						depVersion = override.Version
					}
				case overrideTypeGit, overrideTypeLocalPath, overrideTypeArchive:
					// Match Bazel: non-registry overrides use empty version.
					depVersion = ""
				}
			}

			maxCL := dep.MaxCompatibilityLevel
			if maxCL == 0 {
				maxCL = -1
			}
			specs = append(specs, selection.DepSpec{
				Name:                  dep.Name,
				Version:               depVersion,
				MaxCompatibilityLevel: maxCL,
			})
		}
		return specs
	}

	parseLocalPathOverrideModule := func(path string) (*ModuleInfo, error) {
		moduleFile := path
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			moduleFile = filepath.Join(path, "MODULE.bazel")
		}
		return ParseModuleFile(moduleFile)
	}

	// Create root module entry
	rootKey := selection.ModuleKey{
		Name:    rootModule.Name,
		Version: rootModule.Version,
	}

	rootDeps := buildDepSpecs(rootModule.Dependencies, true)
	if r.options.BazelVersion != "" {
		builtinToolSpecs := buildDepSpecs(
			bazelToolsRootDeps(
				r.options.BazelVersion,
				r.options.BazelToolsLookup,
				r.options.BazelToolsTransformer,
			),
			true,
		)
		builtinDeps := make([]selection.DepSpec, 0, len(builtinToolSpecs)+1)
		if includeVisibleLocalConfigPlatform(r.options.BazelVersion) {
			localConfigKey := selection.ModuleKey{Name: builtinLocalConfigPlatformModule, Version: ""}
			modules[localConfigKey] = &selection.Module{Key: localConfigKey}
			builtinDeps = append(builtinDeps, selection.DepSpec{Name: builtinLocalConfigPlatformModule})
		}
		builtinDeps = append(builtinDeps, builtinToolSpecs...)

		bazelToolsKey := selection.ModuleKey{Name: builtinBazelToolsModule, Version: ""}
		modules[bazelToolsKey] = &selection.Module{
			Key:  bazelToolsKey,
			Deps: builtinDeps,
		}
		rootDeps = append(rootDeps, selection.DepSpec{Name: builtinBazelToolsModule})
	}

	rootNodepDeps := buildDepSpecs(rootModule.NodepDependencies, true)
	modules[rootKey] = &selection.Module{
		Key:         rootKey,
		Deps:        rootDeps,
		NodepDeps:   rootNodepDeps,
		CompatLevel: rootModule.CompatibilityLevel,
	}

	// BFS to fetch all transitive dependencies
	visited := make(map[selection.ModuleKey]bool)
	visited[rootKey] = true

	queue := make([]selection.DepSpec, len(rootDeps))
	copy(queue, rootDeps)

	var mu sync.Mutex
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Worker pool for concurrent fetching
	sem := make(chan struct{}, defaultMaxConcurrency)

	for {
		// Process all current queue items
		for {
			mu.Lock()
			if len(queue) == 0 {
				mu.Unlock()
				break
			}
			dep := queue[0]
			queue = queue[1:]
			mu.Unlock()

			key := dep.ToModuleKey()

			if predefinedModule, ok := modules[key]; ok {
				mu.Lock()
				if !visited[key] {
					visited[key] = true
					for _, d := range predefinedModule.Deps {
						dk := d.ToModuleKey()
						if !visited[dk] {
							queue = append(queue, d)
						}
					}
					for _, d := range predefinedModule.NodepDeps {
						dk := d.ToModuleKey()
						if !visited[dk] {
							queue = append(queue, d)
						}
					}
				}
				mu.Unlock()
				continue
			}

			// Check if this should skip registry fetch (git/local/archive override)
			if override, ok := overrideMap[dep.Name]; ok {
				switch override.Type {
				case overrideTypeGit, overrideTypeLocalPath, overrideTypeArchive:
					// Match Bazel: non-registry overrides resolve to empty version.
					key = selection.ModuleKey{Name: dep.Name, Version: ""}

					if override.Type == overrideTypeLocalPath && override.Path != "" {
						localModule, err := parseLocalPathOverrideModule(override.Path)
						if err != nil {
							cancel()
							wg.Wait()
							return nil, nil, fmt.Errorf("parse local_path override for %s: %w", dep.Name, err)
						}
						localDeps := buildDepSpecs(localModule.Dependencies, false)
						localNodepDeps := buildDepSpecs(localModule.NodepDependencies, false)

						mu.Lock()
						if !visited[key] {
							visited[key] = true
							modules[key] = &selection.Module{
								Key:         key,
								Deps:        localDeps,
								NodepDeps:   localNodepDeps,
								CompatLevel: localModule.CompatibilityLevel,
							}
							for _, d := range localDeps {
								dk := d.ToModuleKey()
								if !visited[dk] {
									queue = append(queue, d)
								}
							}
							for _, d := range localNodepDeps {
								dk := d.ToModuleKey()
								if !visited[dk] {
									queue = append(queue, d)
								}
							}
						}
						mu.Unlock()
						continue
					}

					mu.Lock()
					if !visited[key] {
						visited[key] = true
						// Add placeholder for non-registry modules
						modules[key] = &selection.Module{
							Key:         key,
							Deps:        nil,
							CompatLevel: 0,
						}
					}
					mu.Unlock()
					continue
				case overrideTypeSingleVersion:
					if override.Version != "" {
						key = selection.ModuleKey{Name: dep.Name, Version: override.Version}
					}
				}
			}

			mu.Lock()
			if visited[key] {
				mu.Unlock()
				continue
			}
			visited[key] = true
			mu.Unlock()

			// Fetch module info from registry
			wg.Add(1)
			go func(k selection.ModuleKey) {
				defer wg.Done()

				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}

				// Check if there's a registry override for this module.
				registryToUse := r.registry
				if override, ok := overrideMap[k.Name]; ok && override.Registry != "" {
					registryToUse = registryWithAllOptionsAndTrace(
						r.options.HTTPClient,
						r.options.Cache,
						r.options.Timeout,
						r.options.Logger,
						sharedRegistryFileTrace(r.registry),
						override.Registry,
					)
				}
				moduleInfo, err := registryToUse.GetModuleFile(ctx, k.Name, k.Version)
				if err != nil {
					if !isNotFound(err) {
						select {
						case errCh <- fmt.Errorf("fetch %s@%s: %w", k.Name, k.Version, err):
							cancel()
						default:
						}
					}
					return
				}

				deps := buildDepSpecs(moduleInfo.Dependencies, false)
				nodepDeps := buildDepSpecs(moduleInfo.NodepDependencies, false)

				mu.Lock()
				// Cache module info for post-resolution Bazel compatibility checking.
				if len(moduleInfo.BazelCompatibility) > 0 {
					moduleInfoCache[k.Name+"@"+k.Version] = moduleInfo
				}
				modules[k] = &selection.Module{
					Key:         k,
					Deps:        deps,
					NodepDeps:   nodepDeps,
					CompatLevel: moduleInfo.CompatibilityLevel,
				}
				// Add new deps to queue
				for _, d := range deps {
					dk := d.ToModuleKey()
					if !visited[dk] {
						queue = append(queue, d)
					}
				}
				for _, d := range nodepDeps {
					dk := d.ToModuleKey()
					if !visited[dk] {
						queue = append(queue, d)
					}
				}
				mu.Unlock()
			}(key)
		}

		// Wait for all workers to finish processing current batch
		wg.Wait()

		// Check if any new items were added to the queue
		mu.Lock()
		hasMore := len(queue) > 0
		mu.Unlock()

		if !hasMore {
			break
		}
	}

	// Check for errors
	select {
	case err := <-errCh:
		return nil, nil, err
	default:
	}

	// Propagate context cancellation/timeout.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// Verify that direct root production dependencies were found.
	// Missing direct deps should fail resolution (matching Bazel behavior).
	for _, dep := range rootModule.Dependencies {
		if dep.DevDependency && !r.options.IncludeDevDeps {
			continue
		}
		// Non-registry overrides (git/local/archive) don't need registry lookup.
		if override, ok := overrideMap[dep.Name]; ok {
			switch override.Type {
			case overrideTypeGit, overrideTypeLocalPath, overrideTypeArchive:
				continue
			}
		}
		depKey := selection.ModuleKey{Name: dep.Name, Version: dep.Version}
		if override, ok := overrideMap[dep.Name]; ok && override.Type == overrideTypeSingleVersion && override.Version != "" {
			depKey.Version = override.Version
		}
		if _, found := modules[depKey]; !found {
			return nil, nil, fmt.Errorf("fetch %s@%s: registry returned status 404 for module %s@%s",
				dep.Name, depKey.Version, dep.Name, depKey.Version)
		}
	}

	return &selection.DepGraph{
		Modules: modules,
		RootKey: rootKey,
	}, moduleInfoCache, nil
}

// convertOverrides converts gobzlmod.Override to selection.Override.
func convertOverrides(overrides []Override) map[string]selection.Override {
	result := make(map[string]selection.Override)
	for _, o := range overrides {
		switch o.Type {
		case overrideTypeSingleVersion:
			result[o.ModuleName] = &selection.SingleVersionOverride{
				Version:  o.Version,
				Registry: o.Registry,
			}
		case overrideTypeMultiple:
			result[o.ModuleName] = &selection.MultipleVersionOverride{
				Versions: o.Versions,
				Registry: o.Registry,
			}
		case overrideTypeGit, overrideTypeLocalPath, overrideTypeArchive:
			result[o.ModuleName] = &selection.NonRegistryOverride{
				Type: o.Type,
				Path: o.Path,
			}
		}
	}
	return result
}

// buildResult converts selection.Result to selectionResult.
func (r *selectionResolver) buildResult(ctx context.Context, result *selection.Result, rootModule *ModuleInfo, moduleInfoCache map[string]*ModuleInfo) (*ResolutionList, error) {
	defaultRegistry := r.registry.BaseURL()
	overridesByModule := overrideIndex(rootModule.Overrides)

	// Build sets of root direct dependency types.
	rootDevDeps := make(map[string]bool)
	rootProdDeps := make(map[string]bool)
	for _, dep := range rootModule.Dependencies {
		if dep.DevDependency {
			rootDevDeps[dep.Name] = true
		} else {
			rootProdDeps[dep.Name] = true
		}
	}

	sourceGraph := result.ResolvedGraph
	if r.options.IncludeUnusedModules {
		sourceGraph = result.UnprunedGraph
	}

	// Compute reachability from root's production, dev, and builtin fronts.
	// A module is dev-only iff reachable from dev deps and not from prod deps.
	var prodStarts, devStarts, builtinStarts []selection.ModuleKey
	rootKey := selection.ModuleKey{Name: rootModule.Name, Version: rootModule.Version}
	if rootNode, ok := sourceGraph[rootKey]; ok {
		for _, dep := range rootNode.Deps {
			depKey := dep.ToModuleKey()
			if rootProdDeps[dep.Name] {
				prodStarts = append(prodStarts, depKey)
			}
			if rootDevDeps[dep.Name] && !rootProdDeps[dep.Name] {
				devStarts = append(devStarts, depKey)
			}
			if dep.Name == builtinBazelToolsModule {
				builtinStarts = append(builtinStarts, depKey)
			}
		}
	}
	prodReachable := computeReachableKeys(sourceGraph, prodStarts)
	devReachable := computeReachableKeys(sourceGraph, devStarts)
	builtinReachable := computeReachableKeys(sourceGraph, builtinStarts)
	usedKeys := make(map[selection.ModuleKey]bool, len(result.ResolvedGraph))
	for key := range result.ResolvedGraph {
		usedKeys[key] = true
	}
	allVisible := make(map[selection.ModuleKey]bool, len(sourceGraph))
	if r.options.IncludeUnusedModules {
		for key := range sourceGraph {
			if key != rootKey {
				allVisible[key] = true
			}
		}
	}

	// isVisible returns whether a module key should appear in the output.
	isVisible := func(key selection.ModuleKey) bool {
		if key == rootKey {
			return false
		}
		if r.options.IncludeUnusedModules {
			if !allVisible[key] {
				return false
			}
		} else if !prodReachable[key] && !devReachable[key] && !builtinReachable[key] {
			return false
		}
		if !r.options.IncludeBuiltinModules && builtinReachable[key] && !prodReachable[key] && !devReachable[key] {
			return false
		}
		return true
	}

	// Pre-compute reverse-dependency index in O(n*d) for efficient requiredBy lookup.
	reverseIndex := make(map[selection.ModuleKey][]selection.ModuleKey)
	for depKey, depModule := range sourceGraph {
		if depKey == rootKey {
			continue
		}
		for _, dep := range depModule.Deps {
			dk := dep.ToModuleKey()
			reverseIndex[dk] = append(reverseIndex[dk], depKey)
		}
	}

	resolved := &ResolutionList{
		Modules: make([]ModuleToResolve, 0, len(sourceGraph)),
	}

	for key, module := range sourceGraph {
		if !isVisible(key) {
			continue
		}

		registryURL := registryURLForModule(defaultRegistry, key.Name, overridesByModule)
		// For multi-registry chains, get the actual registry that provided this module.
		if chain, ok := r.registry.(*registryChain); ok && registryURL == defaultRegistry {
			if moduleRegistry := chain.GetRegistryForModule(key.Name); moduleRegistry != "" {
				registryURL = moduleRegistry
			}
		}
		if key.Name == builtinBazelToolsModule || key.Name == builtinLocalConfigPlatformModule {
			registryURL = ""
		}

		dependencies := make([]string, 0, len(module.Deps))
		dependencyKeys := make([]string, 0, len(module.Deps))
		for _, dep := range module.Deps {
			depKey := dep.ToModuleKey()
			if !isVisible(depKey) {
				continue
			}
			dependencies = append(dependencies, dep.Name)
			dependencyKeys = append(dependencyKeys, depKey.String())
		}

		// Look up requiredBy from pre-computed reverse index.
		requiredBy := make([]string, 0)
		for _, reqKey := range reverseIndex[key] {
			if isVisible(reqKey) {
				requiredBy = append(requiredBy, reqKey.String())
			}
		}

		isDevDep := devReachable[key] && !prodReachable[key]
		unused := !usedKeys[key]

		resolved.Modules = append(resolved.Modules, ModuleToResolve{
			Name:           key.Name,
			Version:        key.Version,
			Registry:       registryURL,
			DevDependency:  isDevDep,
			Dependencies:   dependencies,
			DependencyKeys: dependencyKeys,
			RequiredBy:     requiredBy,
			Unused:         unused,
		})
	}

	slices.SortFunc(resolved.Modules, func(a, b ModuleToResolve) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Version, b.Version)
	})

	resolved.Graph = buildSelectionGraph(rootModule, sourceGraph, prodStarts, devStarts, builtinStarts, prodReachable, devReachable, builtinReachable, r.options.IncludeUnusedModules, r.options.IncludeBuiltinModules)
	moduleDepths := calculateModuleDepthsSelection(resolved.Graph)
	for i := range resolved.Modules {
		key := graph.ModuleKey{Name: resolved.Modules[i].Name, Version: resolved.Modules[i].Version}
		resolved.Modules[i].Depth = moduleDepths[key]
	}

	// Check yanked/deprecated versions if enabled
	if r.options.CheckYanked || r.options.WarnDeprecated {
		checkModuleMetadata(ctx, r.registry, r.options, resolved)
	}

	// Check Bazel compatibility if enabled and a Bazel version is specified
	if r.options.BazelCompatibilityMode != BazelCompatibilityOff && r.options.BazelVersion != "" {
		checkModuleBazelCompatibility(resolved.Modules, moduleInfoCache, r.options.BazelVersion)
	}

	// Check field version compatibility if a Bazel version is specified
	if r.options.BazelVersion != "" {
		fieldWarnings := checkFieldCompatibility(rootModule, r.options.BazelVersion)
		resolved.Summary.FieldWarnings = append(resolved.Summary.FieldWarnings, fieldWarnings...)
	}

	// Compute summary and apply configured behaviors.
	computeSummary(resolved)
	if err := applyYankedBehavior(resolved, r.options); err != nil {
		return nil, err
	}
	applyDeprecatedWarnings(resolved, r.options)
	if err := applyBazelCompatBehavior(resolved, r.options); err != nil {
		return nil, err
	}

	if err := enrichResolutionList(ctx, r.registry, r.options, rootModule.Overrides, resolved); err != nil {
		return nil, err
	}

	return resolved, nil
}

func computeReachableKeys(
	graph map[selection.ModuleKey]*selection.Module,
	starts []selection.ModuleKey,
) map[selection.ModuleKey]bool {
	reachable := make(map[selection.ModuleKey]bool, len(starts))
	queue := make([]selection.ModuleKey, 0, len(starts))
	for _, k := range starts {
		if !reachable[k] {
			reachable[k] = true
			queue = append(queue, k)
		}
	}

	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		module, ok := graph[key]
		if !ok {
			continue
		}
		for _, dep := range module.Deps {
			depKey := dep.ToModuleKey()
			if !reachable[depKey] {
				reachable[depKey] = true
				queue = append(queue, depKey)
			}
		}
	}
	return reachable
}

func buildSelectionGraph(
	rootModule *ModuleInfo,
	sourceGraph map[selection.ModuleKey]*selection.Module,
	prodStarts, devStarts, builtinStarts []selection.ModuleKey,
	prodReachable, devReachable, builtinReachable map[selection.ModuleKey]bool,
	includeUnused bool,
	includeBuiltins bool,
) *graph.Graph {
	rootKey := graph.ModuleKey{Name: rootModule.Name, Version: rootModule.Version}
	visible := make(map[selection.ModuleKey]bool, len(sourceGraph))
	if includeUnused {
		for key := range sourceGraph {
			if key == (selection.ModuleKey{Name: rootModule.Name, Version: rootModule.Version}) {
				continue
			}
			if !includeBuiltins && builtinReachable[key] && !prodReachable[key] && !devReachable[key] {
				continue
			}
			visible[key] = true
		}
	} else {
		mapsCopyInto(visible, prodReachable)
		mapsCopyInto(visible, devReachable)
		mapsCopyInto(visible, builtinReachable)
	}

	rootDeps := make([]graph.ModuleKey, 0, len(prodStarts)+len(devStarts)+len(builtinStarts))
	rootSeen := make(map[graph.ModuleKey]bool)
	appendRootDep := func(key selection.ModuleKey) {
		gk := graph.ModuleKey{Name: key.Name, Version: key.Version}
		if !rootSeen[gk] {
			rootSeen[gk] = true
			rootDeps = append(rootDeps, gk)
		}
	}
	for _, key := range prodStarts {
		appendRootDep(key)
	}
	for _, key := range devStarts {
		appendRootDep(key)
	}
	if includeBuiltins {
		for _, key := range builtinStarts {
			appendRootDep(key)
		}
	}

	modules := []graph.SimpleModule{{
		Name:         rootModule.Name,
		Version:      rootModule.Version,
		Dependencies: rootDeps,
	}}
	for key, module := range sourceGraph {
		if key == (selection.ModuleKey{Name: rootModule.Name, Version: rootModule.Version}) {
			continue
		}
		if !visible[key] {
			continue
		}
		deps := make([]graph.ModuleKey, 0, len(module.Deps))
		for _, dep := range module.Deps {
			depKey := dep.ToModuleKey()
			if !visible[depKey] {
				continue
			}
			deps = append(deps, graph.ModuleKey{Name: depKey.Name, Version: depKey.Version})
		}
		modules = append(modules, graph.SimpleModule{
			Name:          key.Name,
			Version:       key.Version,
			Dependencies:  deps,
			DevDependency: false,
		})
	}
	return graph.Build(rootKey, modules)
}

func calculateModuleDepthsSelection(g *graph.Graph) map[graph.ModuleKey]int {
	depths := make(map[graph.ModuleKey]int, len(g.Modules))
	if g == nil {
		return depths
	}
	queue := []graph.ModuleKey{g.Root}
	depths[g.Root] = 0
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		node := g.Get(key)
		if node == nil {
			continue
		}
		for _, dep := range node.Dependencies {
			if _, seen := depths[dep]; seen {
				continue
			}
			depths[dep] = depths[key] + 1
			queue = append(queue, dep)
		}
	}
	return depths
}

func mapsCopyInto(dst, src map[selection.ModuleKey]bool) {
	for key := range src {
		dst[key] = true
	}
}
