// Package types provides core Starlark types for Bazel's dialect.
//
// This file implements RuleInstance, representing a target declared in a BUILD file.
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/Rule.java
package types

import (
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// RuleInstance represents a target (rule instance) declared in a BUILD file.
// It is created when a RuleClass is called with arguments.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/Rule.java
// Rule.java documents:
// - A rule has a name, a package, a class (e.g., cc_library), and typed attributes
// - The set of attribute names and types is a property of the rule's class
// - Example: cc_library(name = 'foo', defines = ['-Dkey=value'], srcs = ['foo.cc'], deps = ['bar'])
type RuleInstance struct {
	// The rule class (schema) that defines this target.
	// Reference: Rule.java line 86 - ruleClass field
	ruleClass *RuleClass

	// The target name (value of the 'name' attribute).
	// Reference: Rule.java - label field in parent class
	name string

	// The label for this target (package + name).
	// Reference: Rule.java line 35 - Label import, used throughout
	label *Label

	// Attribute values provided when the rule was instantiated.
	// Reference: Rule.java lines 369-405 - getAttrWithIndex
	attrValues map[string]starlark.Value

	// Location where this rule was declared.
	// Reference: Rule.java line 87 - location field
	location string

	// Whether this instance has been frozen.
	frozen bool
}

var (
	_ starlark.Value      = (*RuleInstance)(nil)
	_ starlark.HasAttrs   = (*RuleInstance)(nil)
	_ starlark.Comparable = (*RuleInstance)(nil)
)

// NewRuleInstance creates a RuleInstance from a RuleClass and attribute values.
func NewRuleInstance(ruleClass *RuleClass, name string, attrValues map[string]starlark.Value) *RuleInstance {
	return &RuleInstance{
		ruleClass:  ruleClass,
		name:       name,
		attrValues: attrValues,
	}
}

// String returns the Starlark representation.
// Reference: Rule.java line 666 - toString returns "cc_binary rule //foo:foo"
func (ri *RuleInstance) String() string {
	if ri.label != nil {
		return fmt.Sprintf("<%s rule %s>", ri.ruleClass.name, ri.label.String())
	}
	return fmt.Sprintf("<%s rule %s>", ri.ruleClass.name, ri.name)
}

// Type returns the rule class name.
// Reference: Rule.java line 159 - getRuleClass returns the class name
func (ri *RuleInstance) Type() string {
	return ri.ruleClass.name
}

// Freeze marks the instance as frozen.
func (ri *RuleInstance) Freeze() {
	if ri.frozen {
		return
	}
	ri.frozen = true
	for _, v := range ri.attrValues {
		v.Freeze()
	}
}

// Truth returns true (targets are always truthy).
func (ri *RuleInstance) Truth() starlark.Bool { return true }

// Hash returns an error (rule instances are not hashable).
func (ri *RuleInstance) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", ri.ruleClass.name)
}

// Name returns the target name.
func (ri *RuleInstance) Name() string { return ri.name }

// Label returns the target's label.
func (ri *RuleInstance) Label() *Label { return ri.label }

// SetLabel sets the target's label.
func (ri *RuleInstance) SetLabel(label *Label) { ri.label = label }

// RuleClass returns the rule class that defines this target.
// Reference: Rule.java line 147 - getRuleClassObject
func (ri *RuleInstance) RuleClass() *RuleClass { return ri.ruleClass }

// RuleClassName returns the name of the rule class.
// Reference: Rule.java line 159 - getRuleClass
func (ri *RuleInstance) RuleClassName() string { return ri.ruleClass.name }

// TargetKind returns the target kind string.
// Reference: Rule.java lines 152-154 - getTargetKind returns "cc_library rule"
func (ri *RuleInstance) TargetKind() string {
	return ri.ruleClass.name + " rule"
}

// Location returns the location where this rule was declared.
// Reference: Rule.java lines 308-311 - getLocation
func (ri *RuleInstance) Location() string { return ri.location }

// SetLocation sets the location string.
func (ri *RuleInstance) SetLocation(loc string) { ri.location = loc }

// GetAttrValue returns the value of an attribute.
// Reference: Rule.java lines 369-405 - getAttrWithIndex
func (ri *RuleInstance) GetAttrValue(name string) (starlark.Value, bool) {
	v, ok := ri.attrValues[name]
	return v, ok
}

// AttrValues returns all attribute values.
func (ri *RuleInstance) AttrValues() map[string]starlark.Value {
	return ri.attrValues
}

// IsExecutable returns whether this target is executable.
// Reference: Rule.java lines 871-879 - isExecutable
func (ri *RuleInstance) IsExecutable() bool {
	return ri.ruleClass.executable
}

// IsTest returns whether this is a test target.
// Reference: Rule.java - check ruleClass.test
func (ri *RuleInstance) IsTest() bool {
	return ri.ruleClass.test
}

// IsAnalysisTest returns whether this is an analysis test.
// Reference: Rule.java lines 174-176 - isAnalysisTest
func (ri *RuleInstance) IsAnalysisTest() bool {
	return ri.ruleClass.analysisTest
}

// ContainsErrors returns whether this target has errors.
// Reference: Rule.java lines 196-201 - containsErrors
// In our simplified implementation, we don't track errors at the instance level yet.
func (ri *RuleInstance) ContainsErrors() bool {
	return false
}

// GetVisibility returns the visibility attribute value.
// Reference: Rule.java lines 673-688 - getRawVisibilityLabels, getRawVisibility
func (ri *RuleInstance) GetVisibility() (starlark.Value, bool) {
	return ri.GetAttrValue("visibility")
}

// GetTags returns the tags attribute value.
// Reference: Rule.java lines 767-779 - getOnlyTagsAttribute
func (ri *RuleInstance) GetTags() []string {
	v, ok := ri.GetAttrValue("tags")
	if !ok || v == starlark.None {
		return nil
	}

	var tags []string
	if list, ok := v.(*starlark.List); ok {
		iter := list.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			if s, ok := x.(starlark.String); ok {
				tags = append(tags, string(s))
			}
		}
	}
	return tags
}

// IsTestOnly returns whether this target is testonly.
// Reference: Rule.java lines 809-815 - isTestOnly
func (ri *RuleInstance) IsTestOnly() bool {
	v, ok := ri.GetAttrValue("testonly")
	if !ok {
		return false
	}
	if b, ok := v.(starlark.Bool); ok {
		return bool(b)
	}
	return false
}

// GetDeprecation returns the deprecation message if any.
// Reference: Rule.java lines 803-806 - getDeprecationWarning
func (ri *RuleInstance) GetDeprecation() string {
	v, ok := ri.GetAttrValue("deprecation")
	if !ok || v == starlark.None {
		return ""
	}
	if s, ok := v.(starlark.String); ok {
		return string(s)
	}
	return ""
}

// Attr returns an attribute of the rule instance.
// Reference: Rule.java - attribute access patterns
func (ri *RuleInstance) Attr(name string) (starlark.Value, error) {
	// First check if it's a special pseudo-attribute
	switch name {
	case "label":
		if ri.label != nil {
			return ri.label, nil
		}
		return starlark.None, nil
	case "kind":
		return starlark.String(ri.ruleClass.name), nil
	case "name":
		return starlark.String(ri.name), nil
	}

	// Check regular attributes
	if v, ok := ri.attrValues[name]; ok {
		return v, nil
	}

	// Check if the attribute exists in the rule class
	if attr, ok := ri.ruleClass.attrs[name]; ok {
		// Return the default value if available
		if attr.Default != nil {
			return attr.Default, nil
		}
		return starlark.None, nil
	}

	return nil, starlark.NoSuchAttrError(fmt.Sprintf("%s has no attribute %q", ri.ruleClass.name, name))
}

// AttrNames returns the list of attribute names.
func (ri *RuleInstance) AttrNames() []string {
	// Include pseudo-attributes
	names := []string{"label", "kind", "name"}

	// Add all attributes from the rule class
	for name := range ri.ruleClass.attrs {
		if name != "name" { // Already included
			names = append(names, name)
		}
	}

	sort.Strings(names)
	return names
}

// CompareSameType implements comparison (only identity/equality).
// Reference: Rule instances are compared by identity in Bazel
func (ri *RuleInstance) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	other, ok := y.(*RuleInstance)
	if !ok {
		return false, nil
	}

	switch op {
	case syntax.EQL:
		return ri == other, nil // Identity comparison
	case syntax.NEQ:
		return ri != other, nil
	default:
		return false, fmt.Errorf("rule instances support only == and !=")
	}
}

// GetLabels returns all label-typed attribute values.
// Reference: Rule.java lines 438-442 - getLabels
func (ri *RuleInstance) GetLabels() []*Label {
	var labels []*Label

	for name, attr := range ri.ruleClass.attrs {
		if attr.Type != AttrTypeLabel && attr.Type != AttrTypeLabelList {
			continue
		}

		v, ok := ri.attrValues[name]
		if !ok || v == starlark.None {
			continue
		}

		switch val := v.(type) {
		case *Label:
			labels = append(labels, val)
		case starlark.String:
			// Parse string as label
			if l, err := ParseLabel(string(val)); err == nil {
				labels = append(labels, l)
			}
		case *starlark.List:
			iter := val.Iterate()
			var x starlark.Value
			for iter.Next(&x) {
				switch item := x.(type) {
				case *Label:
					labels = append(labels, item)
				case starlark.String:
					if l, err := ParseLabel(string(item)); err == nil {
						labels = append(labels, l)
					}
				}
			}
			iter.Done()
		}
	}

	return labels
}

// GetDeps returns the dependencies (deps attribute).
// This is a convenience method for the common "deps" attribute.
func (ri *RuleInstance) GetDeps() []*Label {
	v, ok := ri.attrValues["deps"]
	if !ok || v == starlark.None {
		return nil
	}

	var deps []*Label
	if list, ok := v.(*starlark.List); ok {
		iter := list.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			switch item := x.(type) {
			case *Label:
				deps = append(deps, item)
			case starlark.String:
				if l, err := ParseLabel(string(item)); err == nil {
					deps = append(deps, l)
				}
			}
		}
	}
	return deps
}

// GetSrcs returns the sources (srcs attribute).
// This is a convenience method for the common "srcs" attribute.
func (ri *RuleInstance) GetSrcs() []starlark.Value {
	v, ok := ri.attrValues["srcs"]
	if !ok || v == starlark.None {
		return nil
	}

	var srcs []starlark.Value
	if list, ok := v.(*starlark.List); ok {
		iter := list.Iterate()
		defer iter.Done()
		var x starlark.Value
		for iter.Next(&x) {
			srcs = append(srcs, x)
		}
	}
	return srcs
}

// Validate validates all attribute values against the rule class schema.
// Returns an error if any attribute is invalid.
func (ri *RuleInstance) Validate() error {
	for name, attr := range ri.ruleClass.attrs {
		v, provided := ri.attrValues[name]

		// Check mandatory attributes
		if attr.Mandatory && (!provided || v == starlark.None) {
			return fmt.Errorf("missing mandatory attribute %q", name)
		}

		// Validate the value if provided
		if provided && v != starlark.None {
			if err := ri.ruleClass.validateAttrValue(attr, v); err != nil {
				return fmt.Errorf("attribute %q: %v", name, err)
			}
		}
	}
	return nil
}

// DebugString returns a detailed string representation for debugging.
func (ri *RuleInstance) DebugString() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("RuleInstance(%s):\n", ri.name))
	sb.WriteString(fmt.Sprintf("  rule_class: %s\n", ri.ruleClass.name))
	if ri.label != nil {
		sb.WriteString(fmt.Sprintf("  label: %s\n", ri.label.String()))
	}
	sb.WriteString("  attrs:\n")

	// Get sorted attribute names
	names := make([]string, 0, len(ri.attrValues))
	for name := range ri.attrValues {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		v := ri.attrValues[name]
		sb.WriteString(fmt.Sprintf("    %s = %s\n", name, v.String()))
	}
	return sb.String()
}

// OutputFiles returns the output files for this target.
// Reference: Rule.java lines 229-242 - getOutputFiles
// This is a placeholder - actual output file handling requires more infrastructure.
func (ri *RuleInstance) OutputFiles() []string {
	// For executable rules, the default output is the target name
	if ri.ruleClass.executable {
		return []string{ri.name}
	}
	return nil
}

// ExecProperties returns the exec_properties attribute value.
// Reference: RuleClass.java EXEC_PROPERTIES_ATTR
func (ri *RuleInstance) ExecProperties() map[string]string {
	v, ok := ri.attrValues["exec_properties"]
	if !ok || v == starlark.None {
		return nil
	}

	props := make(map[string]string)
	if d, ok := v.(*starlark.Dict); ok {
		for _, item := range d.Items() {
			if k, ok := item[0].(starlark.String); ok {
				if val, ok := item[1].(starlark.String); ok {
					props[string(k)] = string(val)
				}
			}
		}
	}
	return props
}

// ToDict converts the rule instance to a Starlark dict representation.
// Useful for serialization or debugging.
func (ri *RuleInstance) ToDict() *starlark.Dict {
	d := starlark.NewDict(len(ri.attrValues) + 2)
	_ = d.SetKey(starlark.String("name"), starlark.String(ri.name))
	_ = d.SetKey(starlark.String("kind"), starlark.String(ri.ruleClass.name))

	for k, v := range ri.attrValues {
		if k != "name" { // Already added
			_ = d.SetKey(starlark.String(k), v)
		}
	}
	return d
}
