package builtins

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
)

// Depset represents a Bazel depset (dependency set).
// A depset is an immutable collection that supports efficient union operations.
// It's used to accumulate data across a transitive dependency graph.
//
// Depsets have an "order" that determines the traversal order:
//   - "default" (or "stable"): unspecified stable order
//   - "postorder": children before parents (good for linking)
//   - "preorder": parents before children
//   - "topological": dependencies before dependents
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/collect/nestedset/Depset.java
type Depset struct {
	order      string           // Traversal order
	direct     []starlark.Value // Direct elements
	transitive []*Depset        // Transitive depsets
	frozen     bool
}

var (
	_ starlark.Value    = (*Depset)(nil)
	_ starlark.Iterable = (*Depset)(nil)
)

// ValidDepsetOrders lists the valid order strings for depsets.
var ValidDepsetOrders = []string{"default", "postorder", "preorder", "topological"}

// String returns the Starlark representation.
func (d *Depset) String() string {
	var sb strings.Builder
	sb.WriteString("depset([")

	elements := d.ToList()
	for i, elem := range elements {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(elem.String())
	}
	sb.WriteString("]")

	if d.order != "default" {
		sb.WriteString(fmt.Sprintf(", order = %q", d.order))
	}
	sb.WriteString(")")
	return sb.String()
}

// Type returns "depset".
func (d *Depset) Type() string { return "depset" }

// Freeze marks the depset as frozen.
func (d *Depset) Freeze() {
	if d.frozen {
		return
	}
	d.frozen = true
	for _, v := range d.direct {
		v.Freeze()
	}
	for _, t := range d.transitive {
		t.Freeze()
	}
}

// Truth returns true if the depset is non-empty.
func (d *Depset) Truth() starlark.Bool {
	return starlark.Bool(len(d.direct) > 0 || len(d.transitive) > 0)
}

// Hash returns an error (depsets are not hashable).
func (d *Depset) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: depset")
}

// Order returns the depset's traversal order.
func (d *Depset) Order() string { return d.order }

// Iterate implements starlark.Iterable.
func (d *Depset) Iterate() starlark.Iterator {
	return &depsetIterator{elements: d.ToList()}
}

// ToList returns all elements in the depset as a list.
// The elements are returned in the order specified by the depset's order.
func (d *Depset) ToList() []starlark.Value {
	seen := make(map[uintptr]bool)
	var result []starlark.Value

	switch d.order {
	case "postorder":
		d.collectPostorder(seen, &result)
	case "preorder":
		d.collectPreorder(seen, &result)
	case "topological":
		d.collectTopological(seen, &result)
	default: // "default" order
		d.collectDefault(seen, &result)
	}

	return result
}

func (d *Depset) collectDefault(seen map[uintptr]bool, result *[]starlark.Value) {
	// For default order, we add direct elements first, then transitive
	for _, elem := range d.direct {
		// Use identity comparison for deduplication
		ptr := uintptr(0)
		if h, ok := elem.(interface{ Hash() (uint32, error) }); ok {
			if hash, err := h.Hash(); err == nil {
				ptr = uintptr(hash)
			}
		}
		if ptr == 0 {
			// Fall back to always including non-hashable elements
			*result = append(*result, elem)
		} else if !seen[ptr] {
			seen[ptr] = true
			*result = append(*result, elem)
		}
	}
	for _, t := range d.transitive {
		t.collectDefault(seen, result)
	}
}

func (d *Depset) collectPostorder(seen map[uintptr]bool, result *[]starlark.Value) {
	// Postorder: transitive before direct (children before parents)
	for _, t := range d.transitive {
		t.collectPostorder(seen, result)
	}
	for _, elem := range d.direct {
		ptr := hashKey(elem)
		if ptr == 0 || !seen[ptr] {
			if ptr != 0 {
				seen[ptr] = true
			}
			*result = append(*result, elem)
		}
	}
}

func (d *Depset) collectPreorder(seen map[uintptr]bool, result *[]starlark.Value) {
	// Preorder: direct before transitive (parents before children)
	for _, elem := range d.direct {
		ptr := hashKey(elem)
		if ptr == 0 || !seen[ptr] {
			if ptr != 0 {
				seen[ptr] = true
			}
			*result = append(*result, elem)
		}
	}
	for _, t := range d.transitive {
		t.collectPreorder(seen, result)
	}
}

func (d *Depset) collectTopological(seen map[uintptr]bool, result *[]starlark.Value) {
	// Topological: like postorder but reversed (dependencies before dependents)
	// For now, use same as postorder
	d.collectPostorder(seen, result)
}

func hashKey(v starlark.Value) uintptr {
	if h, ok := v.(interface{ Hash() (uint32, error) }); ok {
		if hash, err := h.Hash(); err == nil {
			return uintptr(hash)
		}
	}
	return 0
}

// depsetIterator implements starlark.Iterator for Depset.
type depsetIterator struct {
	elements []starlark.Value
	i        int
}

func (it *depsetIterator) Next(p *starlark.Value) bool {
	if it.i >= len(it.elements) {
		return false
	}
	*p = it.elements[it.i]
	it.i++
	return true
}

func (it *depsetIterator) Done() {}

// DepsetBuiltin is the Starlark depset() builtin function.
//
// Signature:
//
//	depset(direct = None, order = "default", transitive = None)
//
// Creates a new depset. If called with a positional argument, that argument
// becomes the direct elements.
//
// Parameters:
//   - direct: A list of elements to include directly in the depset
//   - order: Traversal order ("default", "postorder", "preorder", "topological")
//   - transitive: A list of depsets whose elements will be included transitively
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/collect/nestedset/Depset.java
func DepsetBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		direct     starlark.Value = starlark.None
		order      string         = "default"
		transitive starlark.Value = starlark.None
	)

	// Handle the deprecated positional argument (the old 'items' parameter)
	if len(args) > 0 {
		if len(args) > 1 {
			return nil, fmt.Errorf("depset: got %d positional arguments, want at most 1", len(args))
		}
		direct = args[0]
	}

	// Parse keyword arguments
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		val := kv[1]
		switch key {
		case "direct":
			direct = val
		case "order":
			s, ok := val.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("depset: order must be a string, got %s", val.Type())
			}
			order = string(s)
		case "transitive":
			transitive = val
		case "items":
			// Deprecated alias for direct
			if direct == starlark.None {
				direct = val
			}
		default:
			return nil, fmt.Errorf("depset: unexpected keyword argument %q", key)
		}
	}

	// Validate order
	validOrder := false
	for _, o := range ValidDepsetOrders {
		if order == o {
			validOrder = true
			break
		}
	}
	if !validOrder {
		return nil, fmt.Errorf("depset: invalid order %q (valid values: %v)", order, ValidDepsetOrders)
	}

	// Parse direct elements
	var directList []starlark.Value
	if direct != starlark.None {
		switch v := direct.(type) {
		case *starlark.List:
			iter := v.Iterate()
			defer iter.Done()
			var x starlark.Value
			for iter.Next(&x) {
				directList = append(directList, x)
			}
		case starlark.Tuple:
			for i := range v.Len() {
				directList = append(directList, v.Index(i))
			}
		default:
			return nil, fmt.Errorf("depset: direct must be a list, got %s", direct.Type())
		}
	}

	// Parse transitive depsets
	var transitiveList []*Depset
	if transitive != starlark.None {
		switch v := transitive.(type) {
		case *starlark.List:
			iter := v.Iterate()
			defer iter.Done()
			var x starlark.Value
			for iter.Next(&x) {
				d, ok := x.(*Depset)
				if !ok {
					return nil, fmt.Errorf("depset: transitive elements must be depsets, got %s", x.Type())
				}
				// Check order compatibility
				if d.order != order && d.order != "default" && order != "default" {
					return nil, fmt.Errorf("depset: cannot merge depset with order %q into depset with order %q", d.order, order)
				}
				transitiveList = append(transitiveList, d)
			}
		default:
			return nil, fmt.Errorf("depset: transitive must be a list, got %s", transitive.Type())
		}
	}

	return &Depset{
		order:      order,
		direct:     directList,
		transitive: transitiveList,
	}, nil
}
