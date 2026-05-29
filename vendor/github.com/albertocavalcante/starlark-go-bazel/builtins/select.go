package builtins

import (
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// SelectorValue represents a single select() call's value.
// It holds a dict mapping configuration conditions to values.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/SelectorValue.java
type SelectorValue struct {
	conditions   map[string]starlark.Value // Condition label -> value
	noMatchError string                    // Custom error message for no match
	frozen       bool
}

var (
	_ starlark.Value     = (*SelectorValue)(nil)
	_ starlark.HasBinary = (*SelectorValue)(nil)
)

// String returns the Starlark representation.
func (s *SelectorValue) String() string {
	var sb strings.Builder
	sb.WriteString("select({")

	keys := make([]string, 0, len(s.conditions))
	for k := range s.conditions {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%q: ", k))
		sb.WriteString(s.conditions[k].String())
	}
	sb.WriteString("})")
	return sb.String()
}

// Type returns "selector".
func (s *SelectorValue) Type() string { return "selector" }

// Freeze marks the selector as frozen.
func (s *SelectorValue) Freeze() {
	if s.frozen {
		return
	}
	s.frozen = true
	for _, v := range s.conditions {
		v.Freeze()
	}
}

// Truth returns true.
func (s *SelectorValue) Truth() starlark.Bool { return true }

// Hash returns an error (selectors are not hashable).
func (s *SelectorValue) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: selector")
}

// Conditions returns the conditions dict.
func (s *SelectorValue) Conditions() map[string]starlark.Value { return s.conditions }

// NoMatchError returns the custom error message.
func (s *SelectorValue) NoMatchError() string { return s.noMatchError }

// Binary implements HasBinary, allowing select() + list and select() | dict.
func (s *SelectorValue) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	// Create a SelectorList from this selector, then delegate to it
	list := &SelectorList{
		elements: []starlark.Value{s},
	}
	return list.Binary(op, y, side)
}

// SelectorList represents a concatenation of selects and native values.
// Example: [":default"] + select({...}) + select({...})
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/SelectorList.java
type SelectorList struct {
	elements []starlark.Value // Mix of native values and SelectorValues
	frozen   bool
}

var (
	_ starlark.Value     = (*SelectorList)(nil)
	_ starlark.HasBinary = (*SelectorList)(nil)
)

// String returns the Starlark representation.
func (sl *SelectorList) String() string {
	if len(sl.elements) == 0 {
		return "[]"
	}
	if len(sl.elements) == 1 {
		return sl.elements[0].String()
	}

	var parts []string
	for _, elem := range sl.elements {
		parts = append(parts, elem.String())
	}
	return strings.Join(parts, " + ")
}

// Type returns "select".
func (sl *SelectorList) Type() string { return "select" }

// Freeze marks the selector list as frozen.
func (sl *SelectorList) Freeze() {
	if sl.frozen {
		return
	}
	sl.frozen = true
	for _, v := range sl.elements {
		v.Freeze()
	}
}

// Truth returns true.
func (sl *SelectorList) Truth() starlark.Bool { return true }

// Hash returns an error (selector lists are not hashable).
func (sl *SelectorList) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: select")
}

// Elements returns the list of elements.
func (sl *SelectorList) Elements() []starlark.Value { return sl.elements }

// Binary implements HasBinary, allowing concatenation with + (for lists) and | (for dicts).
func (sl *SelectorList) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	// For lists, we support +; for dicts, we support |
	if op != syntax.PLUS && op != syntax.PIPE {
		return nil, nil // Let starlark handle the error
	}

	var newElements []starlark.Value

	if side == starlark.Left {
		// sl op y
		newElements = append(newElements, sl.elements...)
		switch v := y.(type) {
		case *SelectorList:
			newElements = append(newElements, v.elements...)
		case *SelectorValue:
			newElements = append(newElements, v)
		default:
			newElements = append(newElements, y)
		}
	} else {
		// y op sl
		switch v := y.(type) {
		case *SelectorList:
			newElements = append(newElements, v.elements...)
		case *SelectorValue:
			newElements = append(newElements, v)
		default:
			newElements = append(newElements, y)
		}
		newElements = append(newElements, sl.elements...)
	}

	return &SelectorList{elements: newElements}, nil
}

// Select is the Starlark select() builtin function.
//
// Signature:
//
//	select(x, no_match_error = "")
//
// The select() function creates a configurable attribute value.
// The dict maps configuration conditions (labels) to values.
// At analysis time, Bazel evaluates which condition matches and
// uses the corresponding value.
//
// Special keys:
//   - "//conditions:default" - matches when no other condition matches
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/SelectorList.java
func Select(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		x            *starlark.Dict
		noMatchError string
	)

	if err := starlark.UnpackArgs("select", args, kwargs,
		"x", &x,
		"no_match_error?", &noMatchError,
	); err != nil {
		return nil, err
	}

	if x.Len() == 0 {
		return nil, fmt.Errorf("select({}) with an empty dictionary can never resolve because it includes no conditions to match")
	}

	// Convert the dict to our internal representation
	conditions := make(map[string]starlark.Value)
	for _, item := range x.Items() {
		key := item[0]
		val := item[1]

		var keyStr string
		switch k := key.(type) {
		case starlark.String:
			keyStr = string(k)
		default:
			// Labels can also be passed directly
			// For now, convert to string
			keyStr = key.String()
		}

		conditions[keyStr] = val
	}

	selector := &SelectorValue{
		conditions:   conditions,
		noMatchError: noMatchError,
	}

	// Return a SelectorList wrapping the single selector
	return &SelectorList{elements: []starlark.Value{selector}}, nil
}
