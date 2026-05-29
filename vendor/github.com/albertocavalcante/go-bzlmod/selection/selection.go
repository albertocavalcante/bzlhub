package selection

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/albertocavalcante/go-bzlmod/selection/version"
)

// Run executes module selection (version resolution).
//
// This implements Bazel's Selection.run() from Selection.java lines 266-353.
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L266
//
// The algorithm:
// 1. Compute allowed version sets for multiple-version overrides
// 2. Compute selection groups for each module
// 3. Select highest version for each selection group
// 4. Enumerate all possible resolution strategies (for max_compatibility_level edge cases)
// 5. Walk graph from root, trying each strategy until one succeeds
//
// Reference: Selection.java lines 44-84 describes the algorithm in detail.
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L44
//
// Strategy Enumeration (Phase 6):
//
// When max_compatibility_level allows multiple valid versions for a DepSpec, we enumerate
// the cartesian product of all possible resolutions via computePossibleResolutionResultsForOneDepSpec()
// (Selection.java lines 182-228) and enumerateStrategies() (lines 249-264).
//
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L182
// Reference: https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L249
//
// For each DepSpec with max_compatibility_level, we compute all modules where
// version >= dep.version AND compatLevel <= max_compatibility_level, then enumerate
// the cartesian product of all these possibilities across all deps. Each strategy is
// tried in turn until one succeeds, or we return the first error if all fail.
func Run(graph *DepGraph, overrides map[string]Override) (*Result, error) {
	// Step 1: For any multiple-version overrides, build a mapping from
	// (moduleName, compatibilityLevel) to the set of allowed versions.
	//
	// Reference: Selection.java lines 271-274
	// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L271
	allowedVersionSets, err := computeAllowedVersionSets(overrides, graph)
	if err != nil {
		return nil, err
	}

	// Step 2: For each module in the dep graph, pre-compute its selection group.
	// For most modules this is simply its (moduleName, compatibilityLevel) tuple.
	// For modules with multiple-version overrides, it additionally includes the
	// targetAllowedVersion.
	//
	// Reference: Selection.java lines 276-283
	// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L276
	selectionGroups := make(map[ModuleKey]SelectionGroup)
	for key, module := range graph.Modules {
		selectionGroups[key] = computeSelectionGroup(module, allowedVersionSets)
	}

	// Step 3: Figure out the version to select for every selection group.
	// This is the core of MVS: select the HIGHEST version in each group.
	//
	// Reference: Selection.java lines 285-291
	// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L285
	// "selectedVersions.merge(selectionGroup, key.version(), Comparators::max)"
	selectedVersions := make(map[SelectionGroup]string)
	for key, group := range selectionGroups {
		existing, ok := selectedVersions[group]
		if !ok || version.Compare(key.Version, existing) > 0 {
			selectedVersions[group] = key.Version
		}
	}

	// Step 4: Enumerate all possible resolution strategies.
	//
	// Reference: Selection.java lines 249-264 (enumerateStrategies)
	// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L249
	//
	// When max_compatibility_level allows multiple valid versions for a DepSpec,
	// we enumerate the cartesian product of all possible resolutions and try each
	// strategy until one succeeds.
	strategies := enumerateStrategies(graph, selectionGroups, selectedVersions)

	// Step 5: Two-phase graph walking with strategy enumeration (Bazel 7.6+ behavior)
	//
	// Try each strategy until one succeeds. Return first success or first error if all fail.
	//
	// Reference: Selection.java lines 317-353, 397-403
	// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L317
	var firstError error
	for _, strategy := range strategies {
		result, err := tryStrategy(graph, overrides, selectionGroups, strategy)
		if err == nil {
			return result, nil
		}
		if firstError == nil {
			firstError = err
		}
	}

	// All strategies failed - return first error
	return nil, firstError
}

// tryStrategy attempts to resolve the dependency graph using a single resolution strategy.
// Returns the result if successful, or an error if the strategy fails validation.
func tryStrategy(
	graph *DepGraph,
	overrides map[string]Override,
	selectionGroups map[ModuleKey]SelectionGroup,
	strategy resolutionStrategy,
) (*Result, error) {
	// Phase 1: Walk with nodep deps included (validation only).
	// This validates that all nodep dependencies exist and have valid versions,
	// but doesn't include them in the final resolved graph.
	walkerPhase1 := &depGraphWalker{
		oldGraph:        graph,
		overrides:       overrides,
		selectionGroups: selectionGroups,
		ignoreNodepDeps: false, // Include nodep deps for validation
	}

	// Run phase 1 to validate (we discard the result)
	_, _, err := walkerPhase1.walk(strategy)
	if err != nil {
		return nil, err
	}

	// Phase 2: Walk without nodep deps (final pruning).
	// Modules only reachable via nodep edges are excluded from final graph.
	walkerPhase2 := &depGraphWalker{
		oldGraph:        graph,
		overrides:       overrides,
		selectionGroups: selectionGroups,
		ignoreNodepDeps: true, // Exclude nodep deps for pruning
	}

	resolvedGraph, bfsOrder, err := walkerPhase2.walk(strategy)
	if err != nil {
		return nil, err
	}

	// Build unpruned graph with updated deps (includes all modules from phase 1)
	unprunedGraph := make(map[ModuleKey]*Module, len(graph.Modules))
	for key, module := range graph.Modules {
		newDeps := make([]DepSpec, len(module.Deps))
		for i, dep := range module.Deps {
			newDeps[i] = DepSpec{
				Name:                  dep.Name,
				Version:               strategy(dep),
				MaxCompatibilityLevel: dep.MaxCompatibilityLevel,
			}
		}
		var newNodepDeps []DepSpec
		if len(module.NodepDeps) > 0 {
			newNodepDeps = make([]DepSpec, len(module.NodepDeps))
			for i, dep := range module.NodepDeps {
				newNodepDeps[i] = DepSpec{
					Name:                  dep.Name,
					Version:               strategy(dep),
					MaxCompatibilityLevel: dep.MaxCompatibilityLevel,
				}
			}
		}
		unprunedGraph[key] = &Module{
			Key:         key,
			Deps:        newDeps,
			NodepDeps:   newNodepDeps,
			CompatLevel: module.CompatLevel,
		}
	}

	return &Result{
		ResolvedGraph: resolvedGraph,
		UnprunedGraph: unprunedGraph,
		BFSOrder:      bfsOrder,
	}, nil
}

// computeAllowedVersionSets computes a mapping from (moduleName, compatLevel)
// to the set of allowed versions for modules with multiple-version overrides.
//
// Reference: Selection.java lines 117-152
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L117
func computeAllowedVersionSets(overrides map[string]Override, graph *DepGraph) (map[moduleNameAndCompatLevel][]string, error) {
	result := make(map[moduleNameAndCompatLevel][]string)

	for moduleName, override := range overrides {
		mvo, ok := override.(*MultipleVersionOverride)
		if !ok {
			continue
		}

		for _, allowedVersion := range mvo.Versions {
			key := ModuleKey{Name: moduleName, Version: allowedVersion}
			module, ok := graph.Modules[key]
			if !ok {
				return nil, &SelectionError{
					Code: "VERSION_RESOLUTION_ERROR",
					Message: fmt.Sprintf(
						"multiple_version_override for module %s contains version %s, "+
							"but it doesn't exist in the dependency graph",
						moduleName, allowedVersion),
				}
			}

			nameAndCompat := moduleNameAndCompatLevel{
				moduleName:  moduleName,
				compatLevel: module.CompatLevel,
			}
			result[nameAndCompat] = append(result[nameAndCompat], allowedVersion)
		}
	}

	// Sort allowed versions for each group
	for k := range result {
		version.Sort(result[k])
	}

	return result, nil
}

type moduleNameAndCompatLevel struct {
	moduleName  string
	compatLevel int
}

// computeSelectionGroup computes the SelectionGroup for a module.
//
// Reference: Selection.java lines 154-180
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L154
// "If the module has a multiple-version override, information in there will be
// used to compute its targetAllowedVersion."
func computeSelectionGroup(module *Module, allowedVersionSets map[moduleNameAndCompatLevel][]string) SelectionGroup {
	nameAndCompat := moduleNameAndCompatLevel{
		moduleName:  module.Key.Name,
		compatLevel: module.CompatLevel,
	}

	allowedVersions, hasOverride := allowedVersionSets[nameAndCompat]
	if !hasOverride {
		// No multiple-version override - just use module name and compat level
		return SelectionGroup{
			ModuleName:           module.Key.Name,
			CompatLevel:          module.CompatLevel,
			TargetAllowedVersion: "",
		}
	}

	// Find the lowest allowed version >= module's version (ceiling).
	//
	// Reference: Selection.java lines 174-179
	// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L174
	// "We use the ceiling method here to quickly locate the lowest allowed version
	// that's still no lower than this module's version."
	targetVersion := ""
	for _, av := range allowedVersions {
		if version.Compare(av, module.Key.Version) >= 0 {
			targetVersion = av
			break
		}
	}

	return SelectionGroup{
		ModuleName:           module.Key.Name,
		CompatLevel:          module.CompatLevel,
		TargetAllowedVersion: targetVersion,
	}
}

// depGraphWalker walks the dependency graph from the root, collecting reachable nodes.
//
// Reference: Selection.java lines 355-479, DepGraphWalker class
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L355
type depGraphWalker struct {
	oldGraph        *DepGraph
	overrides       map[string]Override
	selectionGroups map[ModuleKey]SelectionGroup
	// ignoreNodepDeps when true excludes nodep dependencies from graph traversal.
	// This is used in the second phase of two-phase walking to prune modules
	// that are only reachable via nodep edges.
	ignoreNodepDeps bool
}

// walk traverses the graph from root, building a new graph with only reachable modules.
// Returns the resolved graph, BFS order, and any error.
//
// Reference: Selection.java lines 374-408
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L374
func (w *depGraphWalker) walk(resolutionStrategy func(DepSpec) string) (map[ModuleKey]*Module, []ModuleKey, error) {
	moduleByName := make(map[string]existingModule)
	newGraph := make(map[ModuleKey]*Module)
	known := make(map[ModuleKey]bool)
	bfsOrder := []ModuleKey{}

	// BFS queue: (moduleKey, dependent)
	type queueItem struct {
		key       ModuleKey
		dependent *ModuleKey
	}
	queue := []queueItem{{key: w.oldGraph.RootKey, dependent: nil}}
	known[w.oldGraph.RootKey] = true

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		oldModule := w.oldGraph.Modules[item.key]
		if oldModule == nil {
			continue
		}

		// Transform deps using resolution strategy
		newDeps := make([]DepSpec, len(oldModule.Deps))
		for i, dep := range oldModule.Deps {
			resolvedVersion := resolutionStrategy(dep)
			newDeps[i] = DepSpec{
				Name:                  dep.Name,
				Version:               resolvedVersion,
				MaxCompatibilityLevel: dep.MaxCompatibilityLevel,
			}

			// Validate MaxCompatibilityLevel constraint
			// Reference: Bazel enforces that resolved module's compat level <= max_compatibility_level
			// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L182
			// See computePossibleResolutionResultsForOneDepSpec() for the full constraint logic.
			if dep.MaxCompatibilityLevel >= 0 {
				resolvedKey := ModuleKey{Name: dep.Name, Version: resolvedVersion}
				if resolvedModule, ok := w.oldGraph.Modules[resolvedKey]; ok {
					if resolvedModule.CompatLevel > dep.MaxCompatibilityLevel {
						return nil, nil, &SelectionError{
							Code: "VERSION_RESOLUTION_ERROR",
							Message: fmt.Sprintf(
								"%v depends on %s with max_compatibility_level %d, "+
									"but %s@%s has compatibility_level %d which is higher",
								item.key, dep.Name, dep.MaxCompatibilityLevel,
								dep.Name, resolvedVersion, resolvedModule.CompatLevel),
						}
					}
				}
			}
		}

		// Transform nodep deps using resolution strategy (for validation and consistency)
		var newNodepDeps []DepSpec
		if len(oldModule.NodepDeps) > 0 {
			newNodepDeps = make([]DepSpec, len(oldModule.NodepDeps))
			for i, dep := range oldModule.NodepDeps {
				resolvedVersion := resolutionStrategy(dep)
				newNodepDeps[i] = DepSpec{
					Name:                  dep.Name,
					Version:               resolvedVersion,
					MaxCompatibilityLevel: dep.MaxCompatibilityLevel,
				}

				// Validate MaxCompatibilityLevel for nodep deps too
				if dep.MaxCompatibilityLevel >= 0 {
					resolvedKey := ModuleKey{Name: dep.Name, Version: resolvedVersion}
					if resolvedModule, ok := w.oldGraph.Modules[resolvedKey]; ok {
						if resolvedModule.CompatLevel > dep.MaxCompatibilityLevel {
							return nil, nil, &SelectionError{
								Code: "VERSION_RESOLUTION_ERROR",
								Message: fmt.Sprintf(
									"%v has nodep_dep on %s with max_compatibility_level %d, "+
										"but %s@%s has compatibility_level %d which is higher",
									item.key, dep.Name, dep.MaxCompatibilityLevel,
									dep.Name, resolvedVersion, resolvedModule.CompatLevel),
							}
						}
					}
				}
			}
		}

		module := &Module{
			Key:         item.key,
			Deps:        newDeps,
			NodepDeps:   newNodepDeps,
			CompatLevel: oldModule.CompatLevel,
		}

		// Check for conflicts
		if err := w.visit(item.key, module, item.dependent, moduleByName); err != nil {
			return nil, nil, err
		}

		// Add deps to queue (regular deps are always followed)
		for _, dep := range module.Deps {
			depKey := dep.ToModuleKey()
			if !known[depKey] {
				known[depKey] = true
				queue = append(queue, queueItem{key: depKey, dependent: &item.key})
			}
		}

		// Add nodep deps to queue only in phase 1 (when not ignoring them)
		// In phase 2, nodep deps are ignored for pruning
		if !w.ignoreNodepDeps {
			for _, dep := range module.NodepDeps {
				depKey := dep.ToModuleKey()
				if !known[depKey] {
					known[depKey] = true
					queue = append(queue, queueItem{key: depKey, dependent: &item.key})
				}
			}
		}

		newGraph[item.key] = module
		bfsOrder = append(bfsOrder, item.key)
	}

	return newGraph, bfsOrder, nil
}

// existingModule tracks modules we've already visited, for conflict detection.
type existingModule struct {
	key         ModuleKey
	compatLevel int
	dependent   *ModuleKey
}

// visit checks for conflicts when adding a module to the resolved graph.
//
// Reference: Selection.java lines 410-472
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L410
func (w *depGraphWalker) visit(key ModuleKey, module *Module, from *ModuleKey, moduleByName map[string]existingModule) error {
	// Check for multiple-version override conflicts
	if override, ok := w.overrides[key.Name].(*MultipleVersionOverride); ok {
		group := w.selectionGroups[key]
		if group.TargetAllowedVersion == "" {
			// No valid target allowed version - error
			//
			// Reference: Selection.java lines 416-429
			// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L416
			return &SelectionError{
				Code: "VERSION_RESOLUTION_ERROR",
				Message: fmt.Sprintf(
					"%v depends on %v which is not allowed by the multiple_version_override on %s, "+
						"which allows only %v",
					from, key, key.Name, override.Versions),
			}
		}
	} else {
		// Check for compatibility level conflicts
		//
		// Reference: Selection.java lines 431-451
		// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L431
		// "This has to mean that a module with the same name but a different
		// compatibility level was also selected."
		existing, ok := moduleByName[module.Key.Name]
		if ok && existing.compatLevel != module.CompatLevel {
			return &SelectionError{
				Code: "VERSION_RESOLUTION_ERROR",
				Message: fmt.Sprintf(
					"%v depends on %v with compatibility level %d, but %v depends on %v "+
						"with compatibility level %d which is different",
					from, key, module.CompatLevel,
					existing.dependent, existing.key, existing.compatLevel),
			}
		}
		moduleByName[module.Key.Name] = existingModule{
			key:         key,
			compatLevel: module.CompatLevel,
			dependent:   from,
		}
	}

	return nil
}

// resolutionResult represents a possible resolution for a DepSpec.
// It contains the target module's version and compatibility level.
//
// Reference: Selection.java lines 182-228
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L182
type resolutionResult struct {
	Version     string
	CompatLevel int
}

// computePossibleResolutionResultsForOneDepSpec computes all valid versions a DepSpec
// can resolve to, considering max_compatibility_level constraints.
//
// Reference: Selection.java lines 182-228 (computePossibleResolutionResultsForOneDepSpec)
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L182
//
// The algorithm:
// 1. Get the minimum compatibility level from the target module
// 2. Set max_compatibility_level (defaults to min if not specified)
// 3. Filter selection groups matching name and compatibility level range
// 4. For each matching group, get the selected version
// 5. Return all valid versions (one per compatibility level to avoid duplicates)
func computePossibleResolutionResultsForOneDepSpec(
	depSpec DepSpec,
	graph *DepGraph,
	selectionGroups map[ModuleKey]SelectionGroup,
	selectedVersions map[SelectionGroup]string,
) []resolutionResult {
	// Get the target module to find its compatibility level
	targetKey := depSpec.ToModuleKey()
	targetModule, ok := graph.Modules[targetKey]
	if !ok {
		// Module not in graph - can only resolve to original version
		return []resolutionResult{{Version: depSpec.Version, CompatLevel: 0}}
	}

	minCompatLevel := targetModule.CompatLevel

	// Determine max_compatibility_level
	// -1 means no constraint (use min), otherwise use the specified value
	maxCompatLevel := minCompatLevel
	if depSpec.MaxCompatibilityLevel >= 0 {
		maxCompatLevel = depSpec.MaxCompatibilityLevel
	}

	// If min > max, no valid resolution exists
	if minCompatLevel > maxCompatLevel {
		return nil
	}

	// Collect results: one per compatibility level to avoid duplicates
	// Use a map keyed by compat level to get the selected version for each
	resultsByCompat := make(map[int]string)

	// Iterate through all selection groups to find matching ones
	for group, selectedVersion := range selectedVersions {
		// Must match module name
		if group.ModuleName != depSpec.Name {
			continue
		}

		// Compatibility level must be in range [minCompatLevel, maxCompatLevel]
		if group.CompatLevel < minCompatLevel || group.CompatLevel > maxCompatLevel {
			continue
		}

		// The selected version must be >= dep's version (MVS constraint)
		if version.Compare(selectedVersion, depSpec.Version) < 0 {
			continue
		}

		// Store this as a valid result for this compat level
		// If we already have one for this compat level, keep the lower version
		// (to try simpler resolutions first)
		existing, hasExisting := resultsByCompat[group.CompatLevel]
		if !hasExisting || version.Compare(selectedVersion, existing) < 0 {
			resultsByCompat[group.CompatLevel] = selectedVersion
		}
	}

	// Convert map to sorted list
	var results []resolutionResult
	for compatLevel, ver := range resultsByCompat {
		results = append(results, resolutionResult{
			Version:     ver,
			CompatLevel: compatLevel,
		})
	}

	// Sort by compatibility level (ascending) to prefer lower compat levels first
	slices.SortFunc(results, func(a, b resolutionResult) int {
		return cmp.Compare(a.CompatLevel, b.CompatLevel)
	})

	return results
}

// depSpecKey is used to deduplicate DepSpecs by name and version.
type depSpecKey struct {
	Name    string
	Version string
}

// computeAllPossibleResolutions computes possible resolutions for all distinct DepSpecs
// in the dependency graph that have max_compatibility_level constraints.
//
// Returns a map from DepSpec key to list of possible versions.
//
// Reference: Selection.java lines 230-248
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L230
func computeAllPossibleResolutions(
	graph *DepGraph,
	selectionGroups map[ModuleKey]SelectionGroup,
	selectedVersions map[SelectionGroup]string,
) map[depSpecKey][]resolutionResult {
	result := make(map[depSpecKey][]resolutionResult)

	// Collect all distinct DepSpecs with max_compatibility_level.
	// When multiple modules declare the same dep with different max_compat values,
	// keep the most permissive (highest) to maximize the strategy search space.
	//
	// Reference: Selection.java lines 230-248 (computeAllPossibleResolutionResults)
	// Bazel iterates all deps and groups by (name, version). The most permissive
	// max_compatibility_level must win to ensure the strategy enumeration covers
	// all valid compat-level upgrades (e.g., rules_swift compat 1→3 via max_compat=3).
	seen := make(map[depSpecKey]DepSpec)
	collectDep := func(dep DepSpec) {
		if dep.MaxCompatibilityLevel < 0 {
			return
		}
		key := depSpecKey{Name: dep.Name, Version: dep.Version}
		if existing, ok := seen[key]; !ok || dep.MaxCompatibilityLevel > existing.MaxCompatibilityLevel {
			seen[key] = dep
		}
	}
	for _, module := range graph.Modules {
		for _, dep := range module.Deps {
			collectDep(dep)
		}
		for _, dep := range module.NodepDeps {
			collectDep(dep)
		}
	}

	// Compute possible resolutions for each distinct DepSpec
	for key, depSpec := range seen {
		possibleResults := computePossibleResolutionResultsForOneDepSpec(
			depSpec, graph, selectionGroups, selectedVersions,
		)
		if len(possibleResults) > 1 {
			// Only include if there are multiple possibilities
			result[key] = possibleResults
		}
	}

	return result
}

// resolutionStrategy is a function that maps a DepSpec to its resolved version.
type resolutionStrategy func(DepSpec) string

// enumerateStrategies generates all possible resolution strategies based on the
// cartesian product of possible resolutions for DepSpecs with max_compatibility_level.
//
// Reference: Selection.java lines 249-264 (enumerateStrategies)
// https://github.com/bazelbuild/bazel/blob/master/src/main/java/com/google/devtools/build/lib/bazel/bzlmod/Selection.java#L249
//
// Returns a list of strategy functions. Each strategy represents one combination
// of version choices for DepSpecs with multiple valid versions.
func enumerateStrategies(
	graph *DepGraph,
	selectionGroups map[ModuleKey]SelectionGroup,
	selectedVersions map[SelectionGroup]string,
) []resolutionStrategy {
	// Compute all possible resolutions
	allPossible := computeAllPossibleResolutions(graph, selectionGroups, selectedVersions)

	if len(allPossible) == 0 {
		// No ambiguity - return single default strategy
		return []resolutionStrategy{
			makeDefaultStrategy(selectionGroups, selectedVersions),
		}
	}

	// Build list of keys in deterministic order
	var keys []depSpecKey
	for k := range allPossible {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b depSpecKey) int {
		if a.Name != b.Name {
			return cmp.Compare(a.Name, b.Name)
		}
		return cmp.Compare(a.Version, b.Version)
	})

	// Compute cartesian product
	combinations := cartesianProduct(keys, allPossible)

	// Convert each combination to a strategy function
	strategies := make([]resolutionStrategy, len(combinations))
	for i, combo := range combinations {
		strategies[i] = makeStrategyFromCombination(
			combo, selectionGroups, selectedVersions,
		)
	}

	return strategies
}

// makeDefaultStrategy creates the default resolution strategy that uses
// the highest selected version for each selection group.
func makeDefaultStrategy(
	selectionGroups map[ModuleKey]SelectionGroup,
	selectedVersions map[SelectionGroup]string,
) resolutionStrategy {
	return func(depSpec DepSpec) string {
		depKey := depSpec.ToModuleKey()
		group, ok := selectionGroups[depKey]
		if !ok {
			return depSpec.Version
		}
		return selectedVersions[group]
	}
}

// makeStrategyFromCombination creates a resolution strategy from a specific
// combination of version choices.
func makeStrategyFromCombination(
	combination map[depSpecKey]string,
	selectionGroups map[ModuleKey]SelectionGroup,
	selectedVersions map[SelectionGroup]string,
) resolutionStrategy {
	return func(depSpec DepSpec) string {
		// Check if this DepSpec has a specific override in the combination
		key := depSpecKey{Name: depSpec.Name, Version: depSpec.Version}
		if ver, ok := combination[key]; ok {
			return ver
		}

		// Fall back to default selection
		depKey := depSpec.ToModuleKey()
		group, ok := selectionGroups[depKey]
		if !ok {
			return depSpec.Version
		}
		return selectedVersions[group]
	}
}

// cartesianProduct computes the cartesian product of all possible resolutions.
// Returns a list of maps, where each map represents one complete assignment
// of versions to DepSpecs.
func cartesianProduct(
	keys []depSpecKey,
	allPossible map[depSpecKey][]resolutionResult,
) []map[depSpecKey]string {
	if len(keys) == 0 {
		return []map[depSpecKey]string{{}}
	}

	// Start with empty combination
	result := []map[depSpecKey]string{{}}

	for _, key := range keys {
		possibilities := allPossible[key]
		var newResult []map[depSpecKey]string

		for _, existing := range result {
			for _, poss := range possibilities {
				// Clone existing map and add new choice
				newCombo := make(map[depSpecKey]string, len(existing)+1)
				for k, v := range existing {
					newCombo[k] = v
				}
				newCombo[key] = poss.Version
				newResult = append(newResult, newCombo)
			}
		}

		result = newResult
	}

	return result
}
