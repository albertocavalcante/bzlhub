package builtins

import (
	"fmt"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Struct represents an immutable Starlark struct created by struct().
// A struct provides attribute access to its fields but is not iterable
// or indexable like a dict.
//
// Reference: bazel/src/main/java/net/starlark/java/eval/Starlark.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/StructProvider.java
type Struct struct {
	fields map[string]starlark.Value
	frozen bool
}

var (
	_ starlark.Value      = (*Struct)(nil)
	_ starlark.HasAttrs   = (*Struct)(nil)
	_ starlark.Comparable = (*Struct)(nil)
)

// NewStruct creates a new struct with the given fields.
func NewStruct(fields map[string]starlark.Value) *Struct {
	return &Struct{fields: fields}
}

// String returns the Starlark representation.
func (s *Struct) String() string {
	var sb strings.Builder
	sb.WriteString("struct(")

	keys := make([]string, 0, len(s.fields))
	for k := range s.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		sb.WriteString(" = ")
		sb.WriteString(s.fields[k].String())
	}
	sb.WriteString(")")
	return sb.String()
}

// Type returns "struct".
func (s *Struct) Type() string { return "struct" }

// Freeze marks the struct as frozen.
func (s *Struct) Freeze() {
	if s.frozen {
		return
	}
	s.frozen = true
	for _, v := range s.fields {
		v.Freeze()
	}
}

// Truth returns true.
func (s *Struct) Truth() starlark.Bool { return true }

// Hash returns an error (structs are not hashable).
func (s *Struct) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: struct")
}

// Attr returns an attribute of the struct.
func (s *Struct) Attr(name string) (starlark.Value, error) {
	// Special built-in method: to_json
	if name == "to_json" {
		return starlark.NewBuiltin("to_json", s.toJSON), nil
	}
	// Special built-in method: to_proto
	if name == "to_proto" {
		return starlark.NewBuiltin("to_proto", s.toProto), nil
	}

	if v, ok := s.fields[name]; ok {
		return v, nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("struct has no attribute %q", name))
}

// AttrNames returns the list of attribute names.
func (s *Struct) AttrNames() []string {
	names := make([]string, 0, len(s.fields)+2)
	for k := range s.fields {
		names = append(names, k)
	}
	// Add built-in methods
	names = append(names, "to_json", "to_proto")
	sort.Strings(names)
	return names
}

// CompareSameType implements comparison (only equality).
func (s *Struct) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	other, ok := y.(*Struct)
	if !ok {
		return false, nil
	}

	switch op {
	case syntax.EQL:
		if len(s.fields) != len(other.fields) {
			return false, nil
		}
		for k, v := range s.fields {
			ov, ok := other.fields[k]
			if !ok {
				return false, nil
			}
			eq, err := starlark.Equal(v, ov)
			if err != nil {
				return false, err
			}
			if !eq {
				return false, nil
			}
		}
		return true, nil
	case syntax.NEQ:
		eq, err := s.CompareSameType(syntax.EQL, y, depth)
		if err != nil {
			return false, err
		}
		return !eq, nil
	default:
		return false, fmt.Errorf("struct does not support %s", op)
	}
}

// Fields returns the struct's fields.
func (s *Struct) Fields() map[string]starlark.Value { return s.fields }

// Get returns a field value by name.
func (s *Struct) Get(name string) (starlark.Value, bool) {
	v, ok := s.fields[name]
	return v, ok
}

// toJSON is the struct.to_json() method.
func (s *Struct) toJSON(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("to_json: got unexpected arguments")
	}

	// Build JSON string
	var sb strings.Builder
	sb.WriteString("{")

	keys := make([]string, 0, len(s.fields))
	for k := range s.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%q: ", k))
		if err := writeJSON(&sb, s.fields[k]); err != nil {
			return nil, err
		}
	}
	sb.WriteString("}")

	return starlark.String(sb.String()), nil
}

// toProto is the struct.to_proto() method.
// Returns a text-format protocol buffer representation.
func (s *Struct) toProto(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 || len(kwargs) > 0 {
		return nil, fmt.Errorf("to_proto: got unexpected arguments")
	}

	var sb strings.Builder
	keys := make([]string, 0, len(s.fields))
	for k := range s.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if err := writeProto(&sb, k, s.fields[k], 0); err != nil {
			return nil, err
		}
	}

	return starlark.String(sb.String()), nil
}

// writeJSON writes a Starlark value as JSON to the string builder.
func writeJSON(sb *strings.Builder, v starlark.Value) error {
	switch val := v.(type) {
	case starlark.NoneType:
		sb.WriteString("null")
	case starlark.Bool:
		if val {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case starlark.Int:
		sb.WriteString(val.String())
	case starlark.Float:
		sb.WriteString(fmt.Sprintf("%g", float64(val)))
	case starlark.String:
		sb.WriteString(fmt.Sprintf("%q", string(val)))
	case *starlark.List:
		sb.WriteString("[")
		for i := range val.Len() {
			if i > 0 {
				sb.WriteString(", ")
			}
			if err := writeJSON(sb, val.Index(i)); err != nil {
				return err
			}
		}
		sb.WriteString("]")
	case starlark.Tuple:
		sb.WriteString("[")
		for i := range val.Len() {
			if i > 0 {
				sb.WriteString(", ")
			}
			if err := writeJSON(sb, val.Index(i)); err != nil {
				return err
			}
		}
		sb.WriteString("]")
	case *starlark.Dict:
		sb.WriteString("{")
		first := true
		for _, item := range val.Items() {
			if !first {
				sb.WriteString(", ")
			}
			first = false
			key, ok := item[0].(starlark.String)
			if !ok {
				return fmt.Errorf("to_json: dict keys must be strings, got %s", item[0].Type())
			}
			sb.WriteString(fmt.Sprintf("%q: ", string(key)))
			if err := writeJSON(sb, item[1]); err != nil {
				return err
			}
		}
		sb.WriteString("}")
	case *Struct:
		sb.WriteString("{")
		keys := make([]string, 0, len(val.fields))
		for k := range val.fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(fmt.Sprintf("%q: ", k))
			if err := writeJSON(sb, val.fields[k]); err != nil {
				return err
			}
		}
		sb.WriteString("}")
	default:
		return fmt.Errorf("to_json: cannot convert %s to JSON", v.Type())
	}
	return nil
}

// writeProto writes a Starlark value as text-format proto to the string builder.
func writeProto(sb *strings.Builder, name string, v starlark.Value, indent int) error {
	prefix := strings.Repeat("  ", indent)

	switch val := v.(type) {
	case starlark.NoneType:
		// Skip None values
		return nil
	case starlark.Bool:
		if val {
			sb.WriteString(fmt.Sprintf("%s%s: true\n", prefix, name))
		} else {
			sb.WriteString(fmt.Sprintf("%s%s: false\n", prefix, name))
		}
	case starlark.Int:
		sb.WriteString(fmt.Sprintf("%s%s: %s\n", prefix, name, val.String()))
	case starlark.Float:
		sb.WriteString(fmt.Sprintf("%s%s: %g\n", prefix, name, float64(val)))
	case starlark.String:
		sb.WriteString(fmt.Sprintf("%s%s: %q\n", prefix, name, string(val)))
	case *starlark.List:
		for i := range val.Len() {
			if err := writeProto(sb, name, val.Index(i), indent); err != nil {
				return err
			}
		}
	case starlark.Tuple:
		for i := range val.Len() {
			if err := writeProto(sb, name, val.Index(i), indent); err != nil {
				return err
			}
		}
	case *starlark.Dict:
		sb.WriteString(fmt.Sprintf("%s%s {\n", prefix, name))
		for _, item := range val.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return fmt.Errorf("to_proto: dict keys must be strings, got %s", item[0].Type())
			}
			if err := writeProto(sb, string(key), item[1], indent+1); err != nil {
				return err
			}
		}
		sb.WriteString(fmt.Sprintf("%s}\n", prefix))
	case *Struct:
		sb.WriteString(fmt.Sprintf("%s%s {\n", prefix, name))
		keys := make([]string, 0, len(val.fields))
		for k := range val.fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := writeProto(sb, k, val.fields[k], indent+1); err != nil {
				return err
			}
		}
		sb.WriteString(fmt.Sprintf("%s}\n", prefix))
	default:
		return fmt.Errorf("to_proto: cannot convert %s to proto", v.Type())
	}
	return nil
}

// StructBuiltin is the Starlark struct() builtin function.
//
// Signature:
//
//	struct(**kwargs)
//
// Creates an immutable struct with the given fields. Structs are like dicts
// but with attribute access instead of indexing.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/packages/StructProvider.java
func StructBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// struct() only accepts keyword arguments
	if len(args) > 0 {
		return nil, fmt.Errorf("struct: got %d positional arguments, want 0", len(args))
	}

	fields := make(map[string]starlark.Value)
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		fields[key] = kv[1]
	}

	return &Struct{fields: fields}, nil
}
