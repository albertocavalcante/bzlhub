package gobzlmod

import "fmt"

// computeSummary populates aggregate statistics on a ResolutionList.
// Must be called after the Modules slice is fully populated and metadata
// (yanked, deprecated, bazel compat) has been applied.
func computeSummary(list *ResolutionList) {
	list.Summary.TotalModules = len(list.Modules)
	list.Summary.ProductionModules = 0
	list.Summary.DevModules = 0
	list.Summary.YankedModules = 0
	list.Summary.DeprecatedModules = 0
	list.Summary.IncompatibleModules = 0

	for _, m := range list.Modules {
		if m.DevDependency {
			list.Summary.DevModules++
		} else {
			list.Summary.ProductionModules++
		}
		if m.Yanked {
			list.Summary.YankedModules++
		}
		if m.IsDeprecated {
			list.Summary.DeprecatedModules++
		}
		if m.IsBazelIncompatible {
			list.Summary.IncompatibleModules++
		}
	}
}

// applyYankedBehavior adds warnings or returns an error based on the
// configured YankedBehavior. computeSummary must be called first so that
// YankedModules is populated.
func applyYankedBehavior(list *ResolutionList, opts ResolutionOptions) error {
	if list.Summary.YankedModules == 0 {
		return nil
	}
	switch opts.YankedBehavior {
	case YankedVersionAllow:
		// Yanked info is populated but no warnings or errors.
	case YankedVersionWarn:
		for _, m := range list.Modules {
			if m.Yanked {
				list.Warnings = append(list.Warnings,
					fmt.Sprintf("module %s@%s is yanked: %s", m.Name, m.Version, m.YankReason))
			}
		}
	case YankedVersionError:
		yankedModules := make([]ModuleToResolve, 0, list.Summary.YankedModules)
		for _, m := range list.Modules {
			if m.Yanked {
				yankedModules = append(yankedModules, m)
			}
		}
		return &YankedVersionsError{Modules: yankedModules}
	}
	return nil
}

// applyDeprecatedWarnings appends deprecation warnings when WarnDeprecated is
// enabled and deprecated modules are present. computeSummary must be called first.
func applyDeprecatedWarnings(list *ResolutionList, opts ResolutionOptions) {
	if !opts.WarnDeprecated || list.Summary.DeprecatedModules == 0 {
		return
	}
	for _, m := range list.Modules {
		if m.IsDeprecated {
			list.Warnings = append(list.Warnings,
				fmt.Sprintf("module %s is deprecated: %s", m.Name, m.DeprecationReason))
		}
	}
}

// applyBazelCompatBehavior adds warnings or returns an error based on the
// configured BazelCompatibilityMode. computeSummary must be called first.
func applyBazelCompatBehavior(list *ResolutionList, opts ResolutionOptions) error {
	if list.Summary.IncompatibleModules == 0 {
		return nil
	}
	switch opts.BazelCompatibilityMode {
	case BazelCompatibilityOff:
		// Incompatibility info is populated but no warnings or errors.
	case BazelCompatibilityWarn:
		for _, m := range list.Modules {
			if m.IsBazelIncompatible {
				list.Warnings = append(list.Warnings,
					fmt.Sprintf("module %s@%s is incompatible with Bazel %s: %s",
						m.Name, m.Version, opts.BazelVersion, m.BazelIncompatibilityReason))
			}
		}
	case BazelCompatibilityError:
		incompatible := make([]ModuleToResolve, 0, list.Summary.IncompatibleModules)
		for _, m := range list.Modules {
			if m.IsBazelIncompatible {
				incompatible = append(incompatible, m)
			}
		}
		return &BazelIncompatibilityError{
			BazelVersion: opts.BazelVersion,
			Modules:      incompatible,
		}
	}
	return nil
}
