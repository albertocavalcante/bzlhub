package builtins

import (
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
)

// RuleClass represents a Starlark-defined rule created by rule().
// It holds the rule's schema (attributes, flags) and implementation function.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/analysis/starlark/StarlarkRuleClassFunctions.java
type RuleClass struct {
	name               string                     // Assigned when exported
	implementation     starlark.Callable          // The rule implementation function
	attrs              map[string]*AttrDescriptor // Attribute schemas (keyed by name)
	test               bool                       // Whether this is a test rule
	executable         bool                       // Whether this rule produces an executable
	outputToGenfiles   bool                       // Deprecated: output to genfiles instead of bin
	fragments          []string                   // Required configuration fragments
	toolchains         []starlark.Value           // Required toolchains
	provides           []starlark.Value           // Providers this rule advertises
	execCompatibleWith []string                   // Execution platform constraints
	doc                string                     // Documentation string
	frozen             bool                       // Whether the rule has been frozen
}

var (
	_ starlark.Value    = (*RuleClass)(nil)
	_ starlark.Callable = (*RuleClass)(nil)
)

// String returns the Starlark representation.
func (r *RuleClass) String() string {
	if r.name != "" {
		return fmt.Sprintf("<rule %s>", r.name)
	}
	return "<rule>"
}

// Type returns "rule".
func (r *RuleClass) Type() string { return "rule" }

// Freeze marks the rule as frozen.
func (r *RuleClass) Freeze() { r.frozen = true }

// Truth returns true.
func (r *RuleClass) Truth() starlark.Bool { return true }

// Hash returns an error (rules are not hashable).
func (r *RuleClass) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: rule")
}

// Name returns the rule's name (set after export).
func (r *RuleClass) Name() string { return r.name }

// SetName sets the rule's name. Called during export.
func (r *RuleClass) SetName(name string) { r.name = name }

// Implementation returns the rule's implementation function.
func (r *RuleClass) Implementation() starlark.Callable { return r.implementation }

// Attrs returns the rule's attribute schemas.
func (r *RuleClass) Attrs() map[string]*AttrDescriptor { return r.attrs }

// IsTest returns whether this is a test rule.
func (r *RuleClass) IsTest() bool { return r.test }

// IsExecutable returns whether this rule produces an executable.
func (r *RuleClass) IsExecutable() bool { return r.executable }

// CallInternal implements starlark.Callable.
// Calling a rule creates a target instance.
// This is what happens when you call my_rule(name = "foo", ...) in a BUILD file.
func (r *RuleClass) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Rules only accept keyword arguments
	if len(args) > 0 {
		return nil, fmt.Errorf("%s: unexpected positional arguments", r.name)
	}

	// Check that this rule has been exported (assigned to a global variable)
	if r.name == "" {
		return nil, fmt.Errorf("rule has not been exported (assign it to a global variable in the .bzl where it's defined)")
	}

	// This would normally create a target in the package being built.
	// For now, we return a dict representing the target's attributes.
	// The actual target creation logic would be in the package builder.
	attrs := make(map[string]starlark.Value)
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		attrs[key] = kv[1]
	}

	// Check for required 'name' attribute
	if _, ok := attrs["name"]; !ok {
		return nil, fmt.Errorf("%s: missing required attribute 'name'", r.name)
	}

	// Validate that all provided attributes are declared
	for attrName := range attrs {
		if attrName == "name" || attrName == "visibility" || attrName == "tags" ||
			attrName == "testonly" || attrName == "deprecation" || attrName == "features" {
			// Built-in attributes are always allowed
			continue
		}
		if _, ok := r.attrs[attrName]; !ok {
			return nil, fmt.Errorf("%s: unexpected attribute %q", r.name, attrName)
		}
	}

	// For now, return None (target instantiation is a side effect)
	return starlark.None, nil
}

// Rule is the Starlark rule() builtin function.
//
// Signature:
//
//	rule(
//	    implementation,
//	    test = False,
//	    attrs = {},
//	    outputs = None,           # Deprecated
//	    executable = False,
//	    output_to_genfiles = False,  # Deprecated
//	    fragments = [],
//	    host_fragments = [],      # Deprecated
//	    _skylark_testable = False,
//	    toolchains = [],
//	    doc = None,
//	    provides = [],
//	    dependency_resolution_rule = False,
//	    exec_compatible_with = [],
//	    analysis_test = False,
//	    build_setting = None,
//	    cfg = None,
//	    exec_groups = None,
//	    initializer = None,
//	    parent = None,
//	    extendable = None,
//	    subrules = [],
//	)
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/StarlarkRuleFunctionsApi.java
func Rule(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		implementation           starlark.Callable
		test                     bool
		attrs                    *starlark.Dict
		outputs                  starlark.Value = starlark.None // Deprecated
		executable               starlark.Value = starlark.None // Can be bool or unbound
		outputToGenfiles         bool
		fragments                *starlark.List
		hostFragments            *starlark.List // Deprecated
		skylarkTestable          bool
		toolchains               *starlark.List
		doc                      starlark.Value = starlark.None
		provides                 *starlark.List
		dependencyResolutionRule bool
		execCompatibleWith       *starlark.List
		analysisTest             bool
		buildSetting             starlark.Value = starlark.None
		cfg                      starlark.Value = starlark.None
		execGroups               starlark.Value = starlark.None
		initializer              starlark.Value = starlark.None
		parent                   starlark.Value = starlark.None
		extendable               starlark.Value = starlark.None
		subrules                 *starlark.List
	)

	if err := starlark.UnpackArgs("rule", args, kwargs,
		"implementation", &implementation,
		"test?", &test,
		"attrs?", &attrs,
		"outputs?", &outputs,
		"executable?", &executable,
		"output_to_genfiles?", &outputToGenfiles,
		"fragments?", &fragments,
		"host_fragments?", &hostFragments,
		"_skylark_testable?", &skylarkTestable,
		"toolchains?", &toolchains,
		"doc?", &doc,
		"provides?", &provides,
		"dependency_resolution_rule?", &dependencyResolutionRule,
		"exec_compatible_with?", &execCompatibleWith,
		"analysis_test?", &analysisTest,
		"build_setting?", &buildSetting,
		"cfg?", &cfg,
		"exec_groups?", &execGroups,
		"initializer?", &initializer,
		"parent?", &parent,
		"extendable?", &extendable,
		"subrules?", &subrules,
	); err != nil {
		return nil, err
	}

	// Parse attributes
	attrMap := make(map[string]*AttrDescriptor)
	if attrs != nil {
		for _, item := range attrs.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("rule: attrs keys must be strings, got %s", item[0].Type())
			}
			name := string(key)

			// Validate attribute name
			if !isValidAttrName(name) {
				return nil, fmt.Errorf("rule: attribute name %q is not a valid identifier", name)
			}

			// Reserved attribute names
			if name == "name" {
				return nil, fmt.Errorf("rule: 'name' is an implicit attribute and cannot be declared")
			}

			desc, ok := item[1].(*AttrDescriptor)
			if !ok {
				return nil, fmt.Errorf("rule: attrs values must be attr objects, got %s for %q", item[1].Type(), name)
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
				return nil, fmt.Errorf("rule: fragments must be strings, got %s", x.Type())
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

	// Parse exec_compatible_with
	var execCompatList []string
	if execCompatibleWith != nil {
		iter := execCompatibleWith.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			s, ok := x.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("rule: exec_compatible_with must be strings, got %s", x.Type())
			}
			execCompatList = append(execCompatList, string(s))
		}
	}

	// Parse doc
	var docStr string
	if doc != starlark.None {
		s, ok := doc.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("rule: doc must be a string, got %s", doc.Type())
		}
		docStr = string(s)
	}

	// Determine executable status
	isExecutable := false
	if executable != starlark.None {
		b, ok := executable.(starlark.Bool)
		if !ok {
			return nil, fmt.Errorf("rule: executable must be a bool, got %s", executable.Type())
		}
		isExecutable = bool(b)
	}

	// analysis_test=True implies test=True
	if analysisTest {
		test = true
	}

	// Test rules are automatically executable
	if test {
		isExecutable = true
	}

	return &RuleClass{
		implementation:     implementation,
		attrs:              attrMap,
		test:               test,
		executable:         isExecutable,
		outputToGenfiles:   outputToGenfiles,
		fragments:          fragmentList,
		toolchains:         toolchainList,
		provides:           providesList,
		execCompatibleWith: execCompatList,
		doc:                docStr,
	}, nil
}

// isValidAttrName checks if the name is a valid Starlark identifier.
func isValidAttrName(name string) bool {
	if len(name) == 0 {
		return false
	}

	// First character must be letter or underscore
	c := name[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
		return false
	}

	// Remaining characters must be letter, digit, or underscore
	for i := 1; i < len(name); i++ {
		c := name[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}

	return true
}

// AttrDescriptor represents an attribute schema created by attr.* functions.
type AttrDescriptor struct {
	attrType        string           // "label", "string", "int", "bool", etc.
	defaultValue    starlark.Value   // Default value
	doc             string           // Documentation
	mandatory       bool             // Whether the attribute is required
	allowEmpty      bool             // For lists: allow empty list
	allowFiles      starlark.Value   // For labels: file type filter
	allowRules      []string         // For labels: allowed rule kinds
	providers       []starlark.Value // For labels: required providers
	allowSingleFile bool             // For labels: must be single file
	executable      bool             // For labels: must be executable
	cfg             starlark.Value   // Configuration transition
	aspects         []starlark.Value // Aspects to apply
	values          []string         // For strings: allowed values
	frozen          bool
}

var (
	_ starlark.Value = (*AttrDescriptor)(nil)
)

// String returns the Starlark representation.
func (a *AttrDescriptor) String() string {
	return fmt.Sprintf("<attr.%s>", a.attrType)
}

// Type returns the type name.
func (a *AttrDescriptor) Type() string { return "Attribute" }

// Freeze marks the descriptor as frozen.
func (a *AttrDescriptor) Freeze() { a.frozen = true }

// Truth returns true.
func (a *AttrDescriptor) Truth() starlark.Bool { return true }

// Hash returns an error.
func (a *AttrDescriptor) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: Attribute")
}

// AttrType returns the attribute type name.
func (a *AttrDescriptor) AttrType() string { return a.attrType }

// DefaultValue returns the default value.
func (a *AttrDescriptor) DefaultValue() starlark.Value { return a.defaultValue }

// IsMandatory returns whether the attribute is required.
func (a *AttrDescriptor) IsMandatory() bool { return a.mandatory }

// attrModule is the attr module containing attribute definition functions.
// Reference: bazel/src/main/java/com/google/devtools/build/lib/analysis/starlark/StarlarkAttrModule.java
type attrModule struct{}

var attrModuleInstance = &attrModule{}

var (
	_ starlark.Value    = (*attrModule)(nil)
	_ starlark.HasAttrs = (*attrModule)(nil)
)

func (a *attrModule) String() string        { return "<module attr>" }
func (a *attrModule) Type() string          { return "module" }
func (a *attrModule) Freeze()               {}
func (a *attrModule) Truth() starlark.Bool  { return true }
func (a *attrModule) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: module") }

func (a *attrModule) Attr(name string) (starlark.Value, error) {
	switch name {
	case "bool":
		return starlark.NewBuiltin("attr.bool", attrBool), nil
	case "int":
		return starlark.NewBuiltin("attr.int", attrInt), nil
	case "int_list":
		return starlark.NewBuiltin("attr.int_list", attrIntList), nil
	case "label":
		return starlark.NewBuiltin("attr.label", attrLabel), nil
	case "label_keyed_string_dict":
		return starlark.NewBuiltin("attr.label_keyed_string_dict", attrLabelKeyedStringDict), nil
	case "label_list":
		return starlark.NewBuiltin("attr.label_list", attrLabelList), nil
	case "output":
		return starlark.NewBuiltin("attr.output", attrOutput), nil
	case "output_list":
		return starlark.NewBuiltin("attr.output_list", attrOutputList), nil
	case "string":
		return starlark.NewBuiltin("attr.string", attrString), nil
	case "string_dict":
		return starlark.NewBuiltin("attr.string_dict", attrStringDict), nil
	case "string_list":
		return starlark.NewBuiltin("attr.string_list", attrStringList), nil
	case "string_list_dict":
		return starlark.NewBuiltin("attr.string_list_dict", attrStringListDict), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("attr has no attribute %q", name))
	}
}

func (a *attrModule) AttrNames() []string {
	return []string{
		"bool",
		"int",
		"int_list",
		"label",
		"label_keyed_string_dict",
		"label_list",
		"output",
		"output_list",
		"string",
		"string_dict",
		"string_list",
		"string_list_dict",
	}
}

// AttrModule returns the attr module containing attribute definition functions.
func AttrModule() starlark.Value {
	return attrModuleInstance
}

// Common parameters for all attr.* functions
func parseCommonAttrParams(kwargs []starlark.Tuple) (defaultValue starlark.Value, doc string, mandatory bool, err error) {
	defaultValue = starlark.None
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		val := kv[1]
		switch key {
		case "default":
			defaultValue = val
		case "doc":
			if s, ok := val.(starlark.String); ok {
				doc = string(s)
			} else if val != starlark.None {
				err = fmt.Errorf("doc must be a string, got %s", val.Type())
				return
			}
		case "mandatory":
			if b, ok := val.(starlark.Bool); ok {
				mandatory = bool(b)
			} else {
				err = fmt.Errorf("mandatory must be a bool, got %s", val.Type())
				return
			}
		}
	}
	return
}

func attrBool(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.bool: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.False
	}
	return &AttrDescriptor{
		attrType:     "bool",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrInt(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.int: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.MakeInt(0)
	}
	// Parse values restriction
	var values []string
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		if key == "values" {
			if list, ok := kv[1].(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var x starlark.Value
				for iter.Next(&x) {
					if s, ok := x.(starlark.Int); ok {
						values = append(values, s.String())
					}
				}
			}
		}
	}
	return &AttrDescriptor{
		attrType:     "int",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
		values:       values,
	}, nil
}

func attrIntList(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.int_list: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewList(nil)
	}
	return &AttrDescriptor{
		attrType:     "int_list",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrLabel(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.label: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}

	desc := &AttrDescriptor{
		attrType:     "label",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}

	// Parse label-specific parameters
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		val := kv[1]
		switch key {
		case "allow_files":
			desc.allowFiles = val
		case "allow_single_file":
			if b, ok := val.(starlark.Bool); ok {
				desc.allowSingleFile = bool(b)
			}
		case "executable":
			if b, ok := val.(starlark.Bool); ok {
				desc.executable = bool(b)
			}
		case "cfg":
			desc.cfg = val
		case "providers":
			if list, ok := val.(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var x starlark.Value
				for iter.Next(&x) {
					desc.providers = append(desc.providers, x)
				}
			}
		case "aspects":
			if list, ok := val.(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var x starlark.Value
				for iter.Next(&x) {
					desc.aspects = append(desc.aspects, x)
				}
			}
		}
	}

	return desc, nil
}

func attrLabelList(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.label_list: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewList(nil)
	}

	desc := &AttrDescriptor{
		attrType:     "label_list",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
		allowEmpty:   true, // Default for lists
	}

	// Parse label-specific parameters
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		val := kv[1]
		switch key {
		case "allow_files":
			desc.allowFiles = val
		case "allow_empty":
			if b, ok := val.(starlark.Bool); ok {
				desc.allowEmpty = bool(b)
			}
		case "cfg":
			desc.cfg = val
		case "providers":
			if list, ok := val.(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var x starlark.Value
				for iter.Next(&x) {
					desc.providers = append(desc.providers, x)
				}
			}
		case "aspects":
			if list, ok := val.(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var x starlark.Value
				for iter.Next(&x) {
					desc.aspects = append(desc.aspects, x)
				}
			}
		}
	}

	return desc, nil
}

func attrLabelKeyedStringDict(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.label_keyed_string_dict: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewDict(0)
	}
	return &AttrDescriptor{
		attrType:     "label_keyed_string_dict",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrOutput(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.output: unexpected positional arguments")
	}
	_, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	return &AttrDescriptor{
		attrType:     "output",
		defaultValue: starlark.None,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrOutputList(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.output_list: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewList(nil)
	}
	return &AttrDescriptor{
		attrType:     "output_list",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrString(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.string: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.String("")
	}
	// Parse values restriction
	var values []string
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		if key == "values" {
			if list, ok := kv[1].(*starlark.List); ok {
				iter := list.Iterate()
				defer iter.Done()
				var x starlark.Value
				for iter.Next(&x) {
					if s, ok := x.(starlark.String); ok {
						values = append(values, string(s))
					}
				}
			}
		}
	}
	return &AttrDescriptor{
		attrType:     "string",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
		values:       values,
	}, nil
}

func attrStringDict(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.string_dict: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewDict(0)
	}
	return &AttrDescriptor{
		attrType:     "string_dict",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrStringList(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.string_list: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewList(nil)
	}
	return &AttrDescriptor{
		attrType:     "string_list",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

func attrStringListDict(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("attr.string_list_dict: unexpected positional arguments")
	}
	defaultVal, doc, mandatory, err := parseCommonAttrParams(kwargs)
	if err != nil {
		return nil, err
	}
	if defaultVal == starlark.None {
		defaultVal = starlark.NewDict(0)
	}
	return &AttrDescriptor{
		attrType:     "string_list_dict",
		defaultValue: defaultVal,
		doc:          doc,
		mandatory:    mandatory,
	}, nil
}

// RuleInfo provides information about a rule instance for testing.
type RuleInfo struct {
	RuleClass *RuleClass
	Name      string
	Attrs     map[string]starlark.Value
}

// String returns a string representation.
func (ri *RuleInfo) String() string {
	var sb strings.Builder
	sb.WriteString(ri.RuleClass.name)
	sb.WriteString("(")
	var keys []string
	for k := range ri.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		sb.WriteString(" = ")
		sb.WriteString(ri.Attrs[k].String())
	}
	sb.WriteString(")")
	return sb.String()
}
