package builtins

import (
	"fmt"

	"go.starlark.net/starlark"
)

// AspectClass represents a Starlark-defined aspect created by aspect().
// Aspects are used to traverse the dependency graph and collect information
// from multiple targets.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/StarlarkDefinedAspect.java
type AspectClass struct {
	name                    string                     // Assigned when exported
	implementation          starlark.Callable          // The aspect implementation function
	attrAspects             []string                   // Attributes to propagate along
	toolchainsAspects       []starlark.Value           // Toolchain types to propagate to
	attrs                   map[string]*AttrDescriptor // Aspect's own attributes
	requiredProviders       []starlark.Value           // Providers that targets must have
	requiredAspectProviders []starlark.Value           // Providers that other aspects must have
	provides                []starlark.Value           // Providers this aspect produces
	requiredAspects         []starlark.Value           // Other aspects that must run first
	propagationPredicate    starlark.Callable          // Function to filter propagation
	fragments               []string                   // Required configuration fragments
	toolchains              []starlark.Value           // Required toolchains
	applyToGeneratingRules  bool                       // Whether to apply to generating rules
	execCompatibleWith      []string                   // Execution platform constraints
	execGroups              map[string]starlark.Value  // Execution groups
	doc                     string                     // Documentation string
	frozen                  bool
}

var (
	_ starlark.Value = (*AspectClass)(nil)
)

// String returns the Starlark representation.
func (a *AspectClass) String() string {
	if a.name != "" {
		return fmt.Sprintf("<aspect %s>", a.name)
	}
	return "<aspect>"
}

// Type returns "aspect".
func (a *AspectClass) Type() string { return "aspect" }

// Freeze marks the aspect as frozen.
func (a *AspectClass) Freeze() { a.frozen = true }

// Truth returns true.
func (a *AspectClass) Truth() starlark.Bool { return true }

// Hash returns an error (aspects are not hashable).
func (a *AspectClass) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: aspect")
}

// Name returns the aspect's name (set after export).
func (a *AspectClass) Name() string { return a.name }

// SetName sets the aspect's name. Called during export.
func (a *AspectClass) SetName(name string) { a.name = name }

// Implementation returns the aspect's implementation function.
func (a *AspectClass) Implementation() starlark.Callable { return a.implementation }

// AttrAspects returns the list of attributes to propagate along.
func (a *AspectClass) AttrAspects() []string { return a.attrAspects }

// Attrs returns the aspect's own attribute schemas.
func (a *AspectClass) Attrs() map[string]*AttrDescriptor { return a.attrs }

// Aspect is the Starlark aspect() builtin function.
//
// Signature:
//
//	aspect(
//	    implementation,
//	    attr_aspects = [],
//	    toolchains_aspects = [],
//	    attrs = {},
//	    required_providers = [],
//	    required_aspect_providers = [],
//	    provides = [],
//	    requires = [],
//	    propagation_predicate = None,
//	    fragments = [],
//	    host_fragments = [],       # Deprecated
//	    toolchains = [],
//	    doc = None,
//	    apply_to_generating_rules = False,
//	    exec_compatible_with = [],
//	    exec_groups = None,
//	    subrules = [],
//	)
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/StarlarkRuleFunctionsApi.java
func Aspect(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		implementation          starlark.Callable
		attrAspects             starlark.Value = starlark.NewList(nil)
		toolchainsAspects       starlark.Value = starlark.NewList(nil)
		attrs                   *starlark.Dict
		requiredProviders       *starlark.List
		requiredAspectProviders *starlark.List
		provides                *starlark.List
		requires                *starlark.List
		propagationPredicate    starlark.Value = starlark.None
		fragments               *starlark.List
		hostFragments           *starlark.List // Deprecated
		toolchains              *starlark.List
		doc                     starlark.Value = starlark.None
		applyToGeneratingRules  bool
		execCompatibleWith      *starlark.List
		execGroups              starlark.Value = starlark.None
		subrules                *starlark.List
	)

	if err := starlark.UnpackArgs("aspect", args, kwargs,
		"implementation", &implementation,
		"attr_aspects?", &attrAspects,
		"toolchains_aspects?", &toolchainsAspects,
		"attrs?", &attrs,
		"required_providers?", &requiredProviders,
		"required_aspect_providers?", &requiredAspectProviders,
		"provides?", &provides,
		"requires?", &requires,
		"propagation_predicate?", &propagationPredicate,
		"fragments?", &fragments,
		"host_fragments?", &hostFragments,
		"toolchains?", &toolchains,
		"doc?", &doc,
		"apply_to_generating_rules?", &applyToGeneratingRules,
		"exec_compatible_with?", &execCompatibleWith,
		"exec_groups?", &execGroups,
		"subrules?", &subrules,
	); err != nil {
		return nil, err
	}

	// Parse attr_aspects (list of attribute names or "*" or a function)
	var attrAspectsList []string
	switch v := attrAspects.(type) {
	case *starlark.List:
		iter := v.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			s, ok := x.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("aspect: attr_aspects must be strings, got %s", x.Type())
			}
			attrAspectsList = append(attrAspectsList, string(s))
		}
	case starlark.Callable:
		// attr_aspects can also be a function that returns the list dynamically
		// For now, we just record that it's a function
		// The actual evaluation happens at analysis time
	default:
		return nil, fmt.Errorf("aspect: attr_aspects must be a list or function, got %s", attrAspects.Type())
	}

	// Parse attrs
	attrMap := make(map[string]*AttrDescriptor)
	if attrs != nil {
		for _, item := range attrs.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("aspect: attrs keys must be strings, got %s", item[0].Type())
			}
			name := string(key)

			// Validate attribute name
			if !isValidAttrName(name) {
				return nil, fmt.Errorf("aspect: attribute name %q is not a valid identifier", name)
			}

			desc, ok := item[1].(*AttrDescriptor)
			if !ok {
				return nil, fmt.Errorf("aspect: attrs values must be attr objects, got %s for %q", item[1].Type(), name)
			}

			// Aspect attributes have restrictions:
			// - Implicit attributes (starting with _) must be label type and have defaults
			// - Explicit attributes must be string/int/bool type
			if name[0] == '_' {
				// Implicit attribute: must be label type with default
				if desc.attrType != "label" && desc.attrType != "label_list" {
					return nil, fmt.Errorf("aspect: implicit attribute %q must have type label or label_list", name)
				}
				if desc.defaultValue == nil || desc.defaultValue == starlark.None {
					return nil, fmt.Errorf("aspect: implicit attribute %q has no default value", name)
				}
			} else {
				// Explicit attribute: must be string, int, or bool
				if desc.attrType != "string" && desc.attrType != "int" && desc.attrType != "bool" {
					return nil, fmt.Errorf("aspect: explicit attribute %q must have type bool, int, or string", name)
				}
			}

			attrMap[name] = desc
		}
	}

	// Parse fragments
	var fragmentList []string
	if fragments != nil {
		iter := fragments.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			s, ok := x.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("aspect: fragments must be strings, got %s", x.Type())
			}
			fragmentList = append(fragmentList, string(s))
		}
	}

	// Parse toolchains
	var toolchainList []starlark.Value
	if toolchains != nil {
		iter := toolchains.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			toolchainList = append(toolchainList, x)
		}
	}

	// Parse toolchains_aspects
	var toolchainsAspectsList []starlark.Value
	switch v := toolchainsAspects.(type) {
	case *starlark.List:
		iter := v.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			toolchainsAspectsList = append(toolchainsAspectsList, x)
		}
	case starlark.Callable:
		// Can be a function
	}

	// Parse required_providers
	var requiredProvidersList []starlark.Value
	if requiredProviders != nil {
		iter := requiredProviders.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			requiredProvidersList = append(requiredProvidersList, x)
		}
	}

	// Parse required_aspect_providers
	var requiredAspectProvidersList []starlark.Value
	if requiredAspectProviders != nil {
		iter := requiredAspectProviders.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			requiredAspectProvidersList = append(requiredAspectProvidersList, x)
		}
	}

	// Parse provides
	var providesList []starlark.Value
	if provides != nil {
		iter := provides.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			providesList = append(providesList, x)
		}
	}

	// Parse requires (required aspects)
	var requiredAspectsList []starlark.Value
	if requires != nil {
		iter := requires.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			requiredAspectsList = append(requiredAspectsList, x)
		}
	}

	// Parse exec_compatible_with
	var execCompatList []string
	if execCompatibleWith != nil {
		iter := execCompatibleWith.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			s, ok := x.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("aspect: exec_compatible_with must be strings, got %s", x.Type())
			}
			execCompatList = append(execCompatList, string(s))
		}
	}

	// Parse exec_groups
	execGroupMap := make(map[string]starlark.Value)
	if execGroups != starlark.None {
		dict, ok := execGroups.(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("aspect: exec_groups must be a dict, got %s", execGroups.Type())
		}
		for _, item := range dict.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("aspect: exec_groups keys must be strings, got %s", item[0].Type())
			}
			execGroupMap[string(key)] = item[1]
		}
	}

	// Parse doc
	var docStr string
	if doc != starlark.None {
		s, ok := doc.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("aspect: doc must be a string, got %s", doc.Type())
		}
		docStr = string(s)
	}

	// Parse propagation_predicate
	var propPredicate starlark.Callable
	if propagationPredicate != starlark.None {
		fn, ok := propagationPredicate.(starlark.Callable)
		if !ok {
			return nil, fmt.Errorf("aspect: propagation_predicate must be callable, got %s", propagationPredicate.Type())
		}
		propPredicate = fn
	}

	// Validation: apply_to_generating_rules and required_providers are mutually exclusive
	if applyToGeneratingRules && len(requiredProvidersList) > 0 {
		return nil, fmt.Errorf("aspect: cannot have both apply_to_generating_rules=True and required_providers")
	}

	// Validation: apply_to_generating_rules and propagation_predicate are mutually exclusive
	if applyToGeneratingRules && propPredicate != nil {
		return nil, fmt.Errorf("aspect: cannot have both apply_to_generating_rules=True and propagation_predicate")
	}

	return &AspectClass{
		implementation:          implementation,
		attrAspects:             attrAspectsList,
		toolchainsAspects:       toolchainsAspectsList,
		attrs:                   attrMap,
		requiredProviders:       requiredProvidersList,
		requiredAspectProviders: requiredAspectProvidersList,
		provides:                providesList,
		requiredAspects:         requiredAspectsList,
		propagationPredicate:    propPredicate,
		fragments:               fragmentList,
		toolchains:              toolchainList,
		applyToGeneratingRules:  applyToGeneratingRules,
		execCompatibleWith:      execCompatList,
		execGroups:              execGroupMap,
		doc:                     docStr,
	}, nil
}
