// Package types provides core Starlark types for Bazel's dialect.
//
// This file implements RuleClass, which represents the schema created by rule().
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/RuleClass.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/analysis/starlark/StarlarkRuleClassFunctions.java
package types

import (
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
)

// AttrType represents the type of a rule attribute.
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/Type.java
type AttrType string

const (
	AttrTypeString     AttrType = "string"
	AttrTypeInt        AttrType = "int"
	AttrTypeBool       AttrType = "bool"
	AttrTypeLabel      AttrType = "label"
	AttrTypeLabelList  AttrType = "label_list"
	AttrTypeStringList AttrType = "string_list"
	AttrTypeStringDict AttrType = "string_dict"
	AttrTypeOutput     AttrType = "output"
	AttrTypeOutputList AttrType = "output_list"
)

// AttrDescriptor describes a rule attribute's schema.
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/Attribute.java
type AttrDescriptor struct {
	Name            string         // The attribute name
	Type            AttrType       // The attribute type
	Default         starlark.Value // Default value (or nil if mandatory)
	Mandatory       bool           // Whether the attribute is required
	Doc             string         // Documentation string
	AllowedFiles    []string       // File extensions allowed (for label types)
	AllowedRules    []string       // Rule classes allowed (for label types)
	Configurable    bool           // Whether the attribute is configurable (select-able)
	NonConfigurable bool           // Explicitly non-configurable
	Executable      bool           // Whether this is an executable label
	SingleFile      bool           // Whether label must reference single file
	AllowEmpty      bool           // Whether empty list is allowed
	Providers       []string       // Required providers for label attributes
}

// RuleClass represents a rule schema created by rule().
// When called in a BUILD file, it creates a RuleInstance (target).
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/RuleClass.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/StarlarkRuleFunctionsApi.java
// The rule() function parameters are documented in StarlarkRuleFunctionsApi.java lines 764-788.
type RuleClass struct {
	// Core identity
	name     string // The rule class name (e.g., "my_library")
	exported bool   // Whether the rule has been exported (assigned to a global)

	// Implementation function called during analysis phase.
	// Reference: StarlarkRuleFunctionsApi.java - "implementation" parameter
	implementation starlark.Callable

	// Declared attributes (user-defined via attrs parameter).
	// Reference: RuleClass.java lines 1890-1920 - attribute handling
	attrs map[string]*AttrDescriptor

	// Provider references that this rule advertises it will return.
	// Reference: StarlarkRuleFunctionsApi.java - "provides" parameter
	provides []*Provider

	// Whether this rule produces an executable target.
	// Reference: StarlarkRuleFunctionsApi.java - "executable" parameter, lines 543-555
	executable bool

	// Whether this is a test rule.
	// Reference: StarlarkRuleFunctionsApi.java - "test" parameter
	// Reference: RuleClass.java RuleClassType.TEST
	test bool

	// Configuration fragments required by this rule.
	// Reference: StarlarkRuleFunctionsApi.java - "fragments" parameter, lines 565-573
	fragments []string

	// Host configuration fragments (deprecated).
	// Reference: StarlarkRuleFunctionsApi.java - "host_fragments" parameter, lines 574-582
	hostFragments []string

	// Toolchains required by this rule.
	// Reference: StarlarkRuleFunctionsApi.java - "toolchains" parameter, lines 596-605
	toolchains []starlark.Value

	// Execution platform constraints.
	// Reference: StarlarkRuleFunctionsApi.java - "exec_compatible_with" parameter, lines 631-638
	execCompatibleWith []starlark.Value

	// Documentation string for the rule.
	// Reference: StarlarkRuleFunctionsApi.java - "doc" parameter, lines 606-617
	doc string

	// Configuration transition (cfg parameter).
	// Reference: StarlarkRuleFunctionsApi.java - "cfg" parameter, lines 675-682
	cfg starlark.Value

	// Execution groups for the rule.
	// Reference: StarlarkRuleFunctionsApi.java - "exec_groups" parameter, lines 683-697
	execGroups *starlark.Dict

	// Build setting configuration (for build settings rules).
	// Reference: StarlarkRuleFunctionsApi.java - "build_setting" parameter, lines 659-674
	buildSetting starlark.Value

	// Whether this is an analysis test rule.
	// Reference: StarlarkRuleFunctionsApi.java - "analysis_test" parameter, lines 639-658
	analysisTest bool

	// Output to genfiles directory instead of bin.
	// Reference: StarlarkRuleFunctionsApi.java - "output_to_genfiles" parameter, lines 556-564
	outputToGenfiles bool

	// Implicit outputs function/dict.
	// Reference: StarlarkRuleFunctionsApi.java - "outputs" parameter (deprecated)
	implicitOutputs starlark.Value

	// Parent rule for rule extension.
	// Reference: StarlarkRuleFunctionsApi.java - "parent" parameter, lines 724-737
	parent *RuleClass

	// Initializer function.
	// Reference: StarlarkRuleFunctionsApi.java - "initializer" parameter, lines 698-723
	initializer starlark.Callable

	// Whether this rule can be extended.
	// Reference: StarlarkRuleFunctionsApi.java - "extendable" parameter, lines 738-752
	extendable starlark.Value

	// Subrules used by this rule.
	// Reference: StarlarkRuleFunctionsApi.java - "subrules" parameter, lines 753-761
	subrules []starlark.Value

	// For dependency resolution rules.
	// Reference: StarlarkRuleFunctionsApi.java - "dependency_resolution_rule" parameter
	dependencyResolutionRule bool

	frozen bool
}

var (
	_ starlark.Value    = (*RuleClass)(nil)
	_ starlark.Callable = (*RuleClass)(nil)
	_ starlark.HasAttrs = (*RuleClass)(nil)
)

// NewRuleClass creates a new RuleClass with the given parameters.
// This mirrors the rule() builtin function.
func NewRuleClass(
	name string,
	implementation starlark.Callable,
	attrs map[string]*AttrDescriptor,
	opts ...RuleClassOption,
) *RuleClass {
	rc := &RuleClass{
		name:           name,
		implementation: implementation,
		attrs:          attrs,
	}

	// Apply options
	for _, opt := range opts {
		opt(rc)
	}

	// Add implicit attributes that all rules have.
	// Reference: RuleClass.java lines 119-124 - NAME_ATTRIBUTE
	// Reference: BaseRuleClasses.java lines 221-293 - commonCoreAndStarlarkAttributes
	rc.addImplicitAttributes()

	return rc
}

// RuleClassOption is a functional option for configuring RuleClass.
type RuleClassOption func(*RuleClass)

// WithExecutable sets whether the rule produces an executable.
func WithExecutable(executable bool) RuleClassOption {
	return func(rc *RuleClass) {
		rc.executable = executable
	}
}

// WithTest sets whether this is a test rule.
func WithTest(test bool) RuleClassOption {
	return func(rc *RuleClass) {
		rc.test = test
	}
}

// WithProvides sets the providers this rule advertises.
func WithProvides(provides []*Provider) RuleClassOption {
	return func(rc *RuleClass) {
		rc.provides = provides
	}
}

// WithFragments sets the configuration fragments.
func WithFragments(fragments []string) RuleClassOption {
	return func(rc *RuleClass) {
		rc.fragments = fragments
	}
}

// WithToolchains sets the required toolchains.
func WithToolchains(toolchains []starlark.Value) RuleClassOption {
	return func(rc *RuleClass) {
		rc.toolchains = toolchains
	}
}

// WithDoc sets the documentation string.
func WithDoc(doc string) RuleClassOption {
	return func(rc *RuleClass) {
		rc.doc = doc
	}
}

// WithCfg sets the configuration transition.
func WithCfg(cfg starlark.Value) RuleClassOption {
	return func(rc *RuleClass) {
		rc.cfg = cfg
	}
}

// WithExecGroups sets the execution groups.
func WithExecGroups(execGroups *starlark.Dict) RuleClassOption {
	return func(rc *RuleClass) {
		rc.execGroups = execGroups
	}
}

// WithBuildSetting sets the build setting configuration.
func WithBuildSetting(buildSetting starlark.Value) RuleClassOption {
	return func(rc *RuleClass) {
		rc.buildSetting = buildSetting
	}
}

// WithAnalysisTest sets whether this is an analysis test rule.
func WithAnalysisTest(analysisTest bool) RuleClassOption {
	return func(rc *RuleClass) {
		rc.analysisTest = analysisTest
	}
}

// WithParent sets the parent rule for extension.
func WithParent(parent *RuleClass) RuleClassOption {
	return func(rc *RuleClass) {
		rc.parent = parent
	}
}

// WithInitializer sets the initializer function.
func WithInitializer(initializer starlark.Callable) RuleClassOption {
	return func(rc *RuleClass) {
		rc.initializer = initializer
	}
}

// addImplicitAttributes adds the standard implicit attributes to all rules.
// Reference: RuleClass.java NAME_ATTRIBUTE (line 120)
// Reference: BaseRuleClasses.java commonCoreAndStarlarkAttributes (lines 221-293)
func (rc *RuleClass) addImplicitAttributes() {
	if rc.attrs == nil {
		rc.attrs = make(map[string]*AttrDescriptor)
	}

	// name attribute - mandatory for all rules
	// Reference: RuleClass.java lines 119-124
	if _, exists := rc.attrs["name"]; !exists {
		rc.attrs["name"] = &AttrDescriptor{
			Name:            "name",
			Type:            AttrTypeString,
			Mandatory:       true,
			NonConfigurable: true,
			Doc:             "A unique name for this target.",
		}
	}

	// visibility attribute
	// Reference: BaseRuleClasses.java lines 227-232
	if _, exists := rc.attrs["visibility"]; !exists {
		rc.attrs["visibility"] = &AttrDescriptor{
			Name:            "visibility",
			Type:            AttrTypeLabelList,
			Default:         starlark.None,
			NonConfigurable: true,
			Doc:             "The visibility of this target.",
		}
	}

	// tags attribute
	// Reference: BaseRuleClasses.java lines 242-245
	// Reference: RuleClass.Builder.REQUIRED_ATTRIBUTES_FOR_NORMAL_RULES (line 666)
	if _, exists := rc.attrs["tags"]; !exists {
		rc.attrs["tags"] = &AttrDescriptor{
			Name:            "tags",
			Type:            AttrTypeStringList,
			Default:         starlark.NewList(nil),
			NonConfigurable: true,
			AllowEmpty:      true,
			Doc:             "Tags for this target.",
		}
	}

	// testonly attribute
	// Reference: BaseRuleClasses.java lines 259-261
	if _, exists := rc.attrs["testonly"]; !exists {
		rc.attrs["testonly"] = &AttrDescriptor{
			Name:            "testonly",
			Type:            AttrTypeBool,
			Default:         starlark.Bool(false),
			NonConfigurable: true,
			Doc:             "If True, only test targets can depend on this target.",
		}
	}

	// deprecation attribute
	// Reference: BaseRuleClasses.java lines 238-240
	if _, exists := rc.attrs["deprecation"]; !exists {
		rc.attrs["deprecation"] = &AttrDescriptor{
			Name:            "deprecation",
			Type:            AttrTypeString,
			Default:         starlark.None,
			NonConfigurable: true,
			Doc:             "Deprecation message for this target.",
		}
	}

	// features attribute
	// Reference: BaseRuleClasses.java line 262
	if _, exists := rc.attrs["features"]; !exists {
		rc.attrs["features"] = &AttrDescriptor{
			Name:       "features",
			Type:       AttrTypeStringList,
			Default:    starlark.NewList(nil),
			AllowEmpty: true,
			Doc:        "Features to enable for this target.",
		}
	}

	// Add test-specific attributes if this is a test rule
	// Reference: RuleClass.Builder.REQUIRED_ATTRIBUTES_FOR_TESTS (lines 670-677)
	// Reference: StarlarkRuleClassFunctions.getTestBaseRule (lines 254-307)
	if rc.test {
		rc.addTestAttributes()
	}

	// Add executable-specific attributes
	// Reference: StarlarkRuleClassFunctions.binaryBaseRule (lines 228-237)
	if rc.executable && !rc.test {
		rc.addExecutableAttributes()
	}
}

// addTestAttributes adds attributes required for test rules.
// Reference: RuleClass.Builder.REQUIRED_ATTRIBUTES_FOR_TESTS (lines 670-677)
// Reference: StarlarkRuleClassFunctions.getTestBaseRule (lines 254-307)
func (rc *RuleClass) addTestAttributes() {
	// size attribute
	if _, exists := rc.attrs["size"]; !exists {
		rc.attrs["size"] = &AttrDescriptor{
			Name:            "size",
			Type:            AttrTypeString,
			Default:         starlark.String("medium"),
			NonConfigurable: true,
			Doc:             "Test size: small, medium, large, or enormous.",
		}
	}

	// timeout attribute
	if _, exists := rc.attrs["timeout"]; !exists {
		rc.attrs["timeout"] = &AttrDescriptor{
			Name:            "timeout",
			Type:            AttrTypeString,
			Default:         starlark.None, // Computed from size
			NonConfigurable: true,
			Doc:             "Test timeout: short, moderate, long, or eternal.",
		}
	}

	// flaky attribute
	if _, exists := rc.attrs["flaky"]; !exists {
		rc.attrs["flaky"] = &AttrDescriptor{
			Name:            "flaky",
			Type:            AttrTypeBool,
			Default:         starlark.Bool(false),
			NonConfigurable: true,
			Doc:             "If True, test is considered flaky.",
		}
	}

	// shard_count attribute
	if _, exists := rc.attrs["shard_count"]; !exists {
		rc.attrs["shard_count"] = &AttrDescriptor{
			Name:    "shard_count",
			Type:    AttrTypeInt,
			Default: starlark.MakeInt(-1),
			Doc:     "Number of test shards.",
		}
	}

	// local attribute
	if _, exists := rc.attrs["local"]; !exists {
		rc.attrs["local"] = &AttrDescriptor{
			Name:            "local",
			Type:            AttrTypeBool,
			Default:         starlark.Bool(false),
			NonConfigurable: true,
			Doc:             "If True, test runs locally without sandboxing.",
		}
	}

	// args attribute for test rules
	if _, exists := rc.attrs["args"]; !exists {
		rc.attrs["args"] = &AttrDescriptor{
			Name:       "args",
			Type:       AttrTypeStringList,
			Default:    starlark.NewList(nil),
			AllowEmpty: true,
			Doc:        "Command line arguments to pass to the test.",
		}
	}
}

// addExecutableAttributes adds attributes for executable (non-test) rules.
// Reference: StarlarkRuleClassFunctions.binaryBaseRule (lines 228-237)
func (rc *RuleClass) addExecutableAttributes() {
	// args attribute
	if _, exists := rc.attrs["args"]; !exists {
		rc.attrs["args"] = &AttrDescriptor{
			Name:       "args",
			Type:       AttrTypeStringList,
			Default:    starlark.NewList(nil),
			AllowEmpty: true,
			Doc:        "Command line arguments to pass when running.",
		}
	}

	// output_licenses attribute
	if _, exists := rc.attrs["output_licenses"]; !exists {
		rc.attrs["output_licenses"] = &AttrDescriptor{
			Name:       "output_licenses",
			Type:       AttrTypeStringList,
			Default:    starlark.NewList(nil),
			AllowEmpty: true,
			Doc:        "Licenses for output files.",
		}
	}
}

// String returns the Starlark representation.
func (rc *RuleClass) String() string {
	if rc.name != "" {
		return fmt.Sprintf("<rule %s>", rc.name)
	}
	return "<rule>"
}

// Type returns "rule".
func (rc *RuleClass) Type() string { return "rule" }

// Freeze marks the rule class as frozen.
func (rc *RuleClass) Freeze() { rc.frozen = true }

// Truth returns true (rules are always truthy).
func (rc *RuleClass) Truth() starlark.Bool { return true }

// Hash returns an error (rule classes are not hashable).
func (rc *RuleClass) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: rule")
}

// Name returns the rule class name.
func (rc *RuleClass) Name() string { return rc.name }

// SetName sets the rule class name (called during export).
func (rc *RuleClass) SetName(name string) {
	rc.name = name
	rc.exported = true
}

// IsExported returns whether the rule has been exported.
func (rc *RuleClass) IsExported() bool { return rc.exported }

// Implementation returns the implementation function.
func (rc *RuleClass) Implementation() starlark.Callable { return rc.implementation }

// Attrs returns the attribute descriptors.
func (rc *RuleClass) Attrs() map[string]*AttrDescriptor { return rc.attrs }

// IsExecutable returns whether this rule produces executables.
func (rc *RuleClass) IsExecutable() bool { return rc.executable }

// IsTest returns whether this is a test rule.
func (rc *RuleClass) IsTest() bool { return rc.test }

// Provides returns the providers this rule advertises.
func (rc *RuleClass) Provides() []*Provider { return rc.provides }

// Doc returns the documentation string.
func (rc *RuleClass) Doc() string { return rc.doc }

// CallInternal implements starlark.Callable.
// When called in a BUILD file, it creates a RuleInstance (target).
// Reference: StarlarkRuleClassFunctions.StarlarkRuleFunction.call (lines 1755-1807)
func (rc *RuleClass) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Rule invocations only accept keyword arguments
	// Reference: StarlarkRuleClassFunctions.java line 1757
	if len(args) > 0 {
		return nil, fmt.Errorf("%s: unexpected positional arguments", rc.name)
	}

	// Check that the rule has been exported
	// Reference: StarlarkRuleClassFunctions.java lines 1760-1761
	if !rc.exported {
		return nil, fmt.Errorf("rule has not been exported by a bzl file")
	}

	// Parse and validate keyword arguments
	attrValues := make(map[string]starlark.Value)
	providedAttrs := make(map[string]bool)

	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		value := kv[1]

		// Check if the attribute exists
		attr, exists := rc.attrs[key]
		if !exists {
			return nil, fmt.Errorf("%s: unexpected attribute %q", rc.name, key)
		}

		// Validate the value type (basic validation)
		if err := rc.validateAttrValue(attr, value); err != nil {
			return nil, fmt.Errorf("%s: attribute %q: %v", rc.name, key, err)
		}

		attrValues[key] = value
		providedAttrs[key] = true
	}

	// Check mandatory attributes and apply defaults
	for name, attr := range rc.attrs {
		if _, provided := providedAttrs[name]; !provided {
			if attr.Mandatory {
				return nil, fmt.Errorf("%s: missing mandatory attribute %q", rc.name, name)
			}
			// Apply default value
			if attr.Default != nil {
				attrValues[name] = attr.Default
			}
		}
	}

	// Create the target instance
	nameVal, hasName := attrValues["name"]
	if !hasName {
		return nil, fmt.Errorf("%s: missing mandatory attribute 'name'", rc.name)
	}
	nameStr, ok := nameVal.(starlark.String)
	if !ok {
		return nil, fmt.Errorf("%s: attribute 'name' must be a string, got %s", rc.name, nameVal.Type())
	}

	instance := &RuleInstance{
		ruleClass:  rc,
		name:       string(nameStr),
		attrValues: attrValues,
	}

	return instance, nil
}

// validateAttrValue performs basic type validation for an attribute value.
func (rc *RuleClass) validateAttrValue(attr *AttrDescriptor, value starlark.Value) error {
	// Allow None for optional attributes
	if value == starlark.None {
		if attr.Mandatory {
			return fmt.Errorf("mandatory attribute cannot be None")
		}
		return nil
	}

	switch attr.Type {
	case AttrTypeString:
		if _, ok := value.(starlark.String); !ok {
			return fmt.Errorf("expected string, got %s", value.Type())
		}
	case AttrTypeInt:
		if _, ok := value.(starlark.Int); !ok {
			return fmt.Errorf("expected int, got %s", value.Type())
		}
	case AttrTypeBool:
		if _, ok := value.(starlark.Bool); !ok {
			return fmt.Errorf("expected bool, got %s", value.Type())
		}
	case AttrTypeLabel:
		// Labels can be strings or Label objects
		switch value.(type) {
		case starlark.String, *Label:
			// OK
		default:
			return fmt.Errorf("expected label (string or Label), got %s", value.Type())
		}
	case AttrTypeLabelList, AttrTypeStringList, AttrTypeOutputList:
		if _, ok := value.(*starlark.List); !ok {
			// Also accept tuples
			if _, ok := value.(starlark.Tuple); !ok {
				return fmt.Errorf("expected list, got %s", value.Type())
			}
		}
	case AttrTypeStringDict:
		if _, ok := value.(*starlark.Dict); !ok {
			return fmt.Errorf("expected dict, got %s", value.Type())
		}
	case AttrTypeOutput:
		if _, ok := value.(starlark.String); !ok {
			return fmt.Errorf("expected string (output name), got %s", value.Type())
		}
	}

	return nil
}

// Attr returns an attribute of the rule class.
func (rc *RuleClass) Attr(name string) (starlark.Value, error) {
	switch name {
	case "kind":
		return starlark.String(rc.name), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("rule has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (rc *RuleClass) AttrNames() []string {
	return []string{"kind"}
}

// RuleBuiltin creates the rule() builtin function.
// Reference: StarlarkRuleFunctionsApi.java lines 764-788
func RuleBuiltin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		implementation starlark.Callable
		test           bool
		attrs          *starlark.Dict
		outputs        starlark.Value
		executable     bool
		outputToGF     bool
		fragments      *starlark.List
		hostFragments  *starlark.List
		testable       bool
		toolchains     *starlark.List
		doc            string
		provides       *starlark.List
		depResRule     bool
		execCompat     *starlark.List
		analysisTest   bool
		buildSetting   starlark.Value
		cfg            starlark.Value
		execGroups     starlark.Value
		initializer    starlark.Value
		parent         starlark.Value
		extendable     starlark.Value
		subrules       *starlark.List
	)

	if err := starlark.UnpackArgs("rule", args, kwargs,
		"implementation", &implementation,
		"test?", &test,
		"attrs?", &attrs,
		"outputs?", &outputs,
		"executable?", &executable,
		"output_to_genfiles?", &outputToGF,
		"fragments?", &fragments,
		"host_fragments?", &hostFragments,
		"_skylark_testable?", &testable,
		"toolchains?", &toolchains,
		"doc?", &doc,
		"provides?", &provides,
		"dependency_resolution_rule?", &depResRule,
		"exec_compatible_with?", &execCompat,
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

	// Convert attrs dict to AttrDescriptor map
	attrMap := make(map[string]*AttrDescriptor)
	if attrs != nil {
		for _, item := range attrs.Items() {
			name := string(item[0].(starlark.String))
			// For now, we create a basic descriptor
			// The full attr module will provide proper AttrDescriptor values
			attrMap[name] = &AttrDescriptor{
				Name: name,
				Type: AttrTypeString, // Default, will be overridden by attr module
			}
		}
	}

	// Build options
	var opts []RuleClassOption
	opts = append(opts, WithExecutable(executable))
	opts = append(opts, WithTest(test))
	opts = append(opts, WithDoc(doc))
	opts = append(opts, WithAnalysisTest(analysisTest))

	if cfg != nil && cfg != starlark.None {
		opts = append(opts, WithCfg(cfg))
	}

	if buildSetting != nil && buildSetting != starlark.None {
		opts = append(opts, WithBuildSetting(buildSetting))
	}

	if execGroups != nil {
		if d, ok := execGroups.(*starlark.Dict); ok {
			opts = append(opts, WithExecGroups(d))
		}
	}

	if fragments != nil {
		var frags []string
		iter := fragments.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			if s, ok := x.(starlark.String); ok {
				frags = append(frags, string(s))
			}
		}
		opts = append(opts, WithFragments(frags))
	}

	if toolchains != nil {
		var tcs []starlark.Value
		iter := toolchains.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			tcs = append(tcs, x)
		}
		opts = append(opts, WithToolchains(tcs))
	}

	if provides != nil {
		var provs []*Provider
		iter := provides.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			if p, ok := x.(*Provider); ok {
				provs = append(provs, p)
			}
		}
		opts = append(opts, WithProvides(provs))
	}

	if parent != nil && parent != starlark.None {
		if p, ok := parent.(*RuleClass); ok {
			opts = append(opts, WithParent(p))
		}
	}

	if initializer != nil && initializer != starlark.None {
		if init, ok := initializer.(starlark.Callable); ok {
			opts = append(opts, WithInitializer(init))
		}
	}

	// Create the rule class (name will be set when exported)
	rc := NewRuleClass("", implementation, attrMap, opts...)
	rc.implicitOutputs = outputs
	rc.outputToGenfiles = outputToGF
	rc.dependencyResolutionRule = depResRule
	rc.extendable = extendable

	if execCompat != nil {
		var constraints []starlark.Value
		iter := execCompat.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			constraints = append(constraints, x)
		}
		rc.execCompatibleWith = constraints
	}

	if subrules != nil {
		var subs []starlark.Value
		iter := subrules.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			subs = append(subs, x)
		}
		rc.subrules = subs
	}

	return rc, nil
}

// AttrDescriptorList returns a sorted list of attribute names.
func (rc *RuleClass) AttrDescriptorList() []string {
	names := make([]string, 0, len(rc.attrs))
	for name := range rc.attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetAttr returns the attribute descriptor for the given name.
func (rc *RuleClass) GetAttr(name string) (*AttrDescriptor, bool) {
	attr, ok := rc.attrs[name]
	return attr, ok
}

// String representation helpers
func attrTypeToString(at AttrType) string {
	return string(at)
}

// DebugString returns a detailed string representation for debugging.
func (rc *RuleClass) DebugString() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("RuleClass(%s):\n", rc.name))
	sb.WriteString(fmt.Sprintf("  executable: %v\n", rc.executable))
	sb.WriteString(fmt.Sprintf("  test: %v\n", rc.test))
	sb.WriteString("  attrs:\n")
	for _, name := range rc.AttrDescriptorList() {
		attr := rc.attrs[name]
		sb.WriteString(fmt.Sprintf("    %s: %s (mandatory=%v)\n", name, attr.Type, attr.Mandatory))
	}
	return sb.String()
}
