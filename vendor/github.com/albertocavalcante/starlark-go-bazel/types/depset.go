// Package types provides core Starlark types for Bazel's dialect.
//
// This file implements Bazel's depset (nested set) type.
// Reference: bazel/src/main/java/com/google/devtools/build/lib/collect/nestedset/Depset.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/collect/nestedset/Order.java
// Reference: bazel/src/main/java/com/google/devtools/build/lib/collect/nestedset/NestedSet.java
package types

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Order represents the traversal order of a depset.
// Reference: Order.java lines 104-108
type Order int

const (
	// OrderDefault is an unspecified (but deterministic) traversal order.
	// In Starlark it is called "default"; its older deprecated name is "stable".
	// Reference: Order.java line 105 - STABLE_ORDER("default")
	OrderDefault Order = iota

	// OrderPostorder is a left-to-right post-ordering.
	// Recursively traverses all children leftmost-first, then the direct elements leftmost-first.
	// In Starlark it is called "postorder"; its older deprecated name is "compile".
	// Reference: Order.java lines 26-35, line 106 - COMPILE_ORDER("postorder")
	OrderPostorder

	// OrderPreorder is a left-to-right pre-ordering.
	// Traverses the direct elements leftmost-first, then recursively traverses the children leftmost-first.
	// In Starlark it is called "preorder"; its older deprecated name is "naive_link".
	// Reference: Order.java lines 89-101, line 108 - NAIVE_LINK_ORDER("preorder")
	OrderPreorder

	// OrderTopological is a topological ordering from the root down to the leaves.
	// There is no left-to-right guarantee.
	// In Starlark it is called "topological"; its older deprecated name is "link".
	// Reference: Order.java lines 37-87, line 107 - LINK_ORDER("topological")
	OrderTopological
)

// String returns the Starlark name for the order.
// Reference: Order.java getStarlarkName()
func (o Order) String() string {
	switch o {
	case OrderDefault:
		return "default"
	case OrderPostorder:
		return "postorder"
	case OrderPreorder:
		return "preorder"
	case OrderTopological:
		return "topological"
	default:
		return "unknown"
	}
}

// ParseOrder parses a string into an Order.
// Reference: Order.java parse()
func ParseOrder(s string) (Order, error) {
	switch s {
	case "default":
		return OrderDefault, nil
	case "postorder":
		return OrderPostorder, nil
	case "preorder":
		return OrderPreorder, nil
	case "topological":
		return OrderTopological, nil
	default:
		return OrderDefault, fmt.Errorf("Invalid order: %s", s)
	}
}

// IsCompatible returns true if two orders can be merged.
// An order is compatible with itself (reflexivity) and all orders are compatible
// with OrderDefault; the rest of the combinations are incompatible.
// Reference: Order.java isCompatible() lines 184-186
func (o Order) IsCompatible(other Order) bool {
	return o == other || o == OrderDefault || other == OrderDefault
}

// Depset represents a Bazel depset (nested set).
// A depset is an immutable collection that supports efficient union operations.
// Elements are deduplicated during iteration (to_list), not at construction time.
//
// Reference: Depset.java lines 43-56
// "A Depset is a Starlark value that wraps a NestedSet"
// "Every call to depset returns a distinct instance equal to no other"
type Depset struct {
	order      Order
	direct     []starlark.Value // Direct elements (leaves)
	transitive []*Depset        // Transitive depsets (non-leaves)
	elemType   string           // Type name of elements ("" for empty)
}

// Compile-time interface checks
var (
	_ starlark.Value      = (*Depset)(nil)
	_ starlark.HasAttrs   = (*Depset)(nil)
	_ starlark.Comparable = (*Depset)(nil)
)

// NewDepset creates a new Depset with the given order, direct elements, and transitive depsets.
// This is the Go API for creating depsets programmatically.
// Reference: Depset.java fromDirectAndTransitive() lines 353-405
func NewDepset(order Order, direct []starlark.Value, transitive []*Depset) (*Depset, error) {
	d := &Depset{
		order:      order,
		direct:     make([]starlark.Value, len(direct)),
		transitive: make([]*Depset, len(transitive)),
	}
	copy(d.direct, direct)
	copy(d.transitive, transitive)

	// Determine and validate element type
	// Reference: Depset.java fromDirectAndTransitive() lines 359-377
	for _, elem := range d.direct {
		if err := d.checkAndSetType(elem); err != nil {
			return nil, err
		}
	}

	// Add transitive sets, checking type and order compatibility
	// Reference: Depset.java fromDirectAndTransitive() lines 379-390
	for _, t := range d.transitive {
		if !t.isEmpty() {
			// Check order compatibility
			// Reference: Depset.java lines 383-387
			if !order.IsCompatible(t.order) {
				return nil, fmt.Errorf("Order '%s' is incompatible with order '%s'",
					order.String(), t.order.String())
			}
			// Check type compatibility
			if d.elemType == "" {
				d.elemType = t.elemType
			} else if t.elemType != "" && d.elemType != t.elemType {
				return nil, fmt.Errorf("cannot add an item of type '%s' to a depset of '%s'",
					t.elemType, d.elemType)
			}
		}
	}

	return d, nil
}

// checkAndSetType validates an element and updates the depset's element type.
// Reference: Depset.java checkElement() lines 125-152 and checkType() lines 181-193
func (d *Depset) checkAndSetType(elem starlark.Value) error {
	// Lists and dicts are forbidden as top-level elements
	// Reference: Depset.java checkElement() lines 150-152
	switch elem.(type) {
	case *starlark.List:
		return fmt.Errorf("depsets cannot contain items of type 'list'")
	case *starlark.Dict:
		return fmt.Errorf("depsets cannot contain items of type 'dict'")
	}

	// All elements must be of the same type
	// Reference: Depset.java checkType() lines 181-193
	elemType := elem.Type()
	if d.elemType == "" {
		d.elemType = elemType
	} else if d.elemType != elemType {
		return fmt.Errorf("cannot add an item of type '%s' to a depset of '%s'",
			elemType, d.elemType)
	}
	return nil
}

// isEmpty returns true if the depset has no elements.
// Reference: Depset.java isEmpty() line 283
func (d *Depset) isEmpty() bool {
	if len(d.direct) > 0 {
		return false
	}
	for _, t := range d.transitive {
		if !t.isEmpty() {
			return false
		}
	}
	return true
}

// String returns the Starlark representation.
// Reference: Depset.java repr() lines 319-328
func (d *Depset) String() string {
	var sb strings.Builder
	sb.WriteString("depset(")

	// Get flattened list for repr
	list := d.ToList()
	sb.WriteString("[")
	for i, v := range list {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(v.String())
	}
	sb.WriteString("]")

	// Only show order if not default
	// Reference: Depset.java repr() lines 322-325
	if d.order != OrderDefault {
		sb.WriteString(", order = ")
		sb.WriteString("\"")
		sb.WriteString(d.order.String())
		sb.WriteString("\"")
	}
	sb.WriteString(")")
	return sb.String()
}

// Type returns "depset".
func (d *Depset) Type() string { return "depset" }

// Freeze is a no-op since depsets are immutable from creation.
// Reference: Depset.java is marked @Immutable (line 113-114) and isImmutable() returns true (line 314)
func (d *Depset) Freeze() {}

// Truth returns true if the depset is non-empty.
// Reference: Depset.java truth() line 288-290
// "a depset is True if and only if it is non-empty; this check is an O(1) operation"
func (d *Depset) Truth() starlark.Bool {
	return starlark.Bool(!d.isEmpty())
}

// Hash returns an error since depsets are not hashable in Starlark.
// While depsets are immutable, they are not designed to be used as dict keys.
func (d *Depset) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: depset")
}

// CompareSameType implements equality comparison.
// Reference: Depset.java equals() lines 559-561 - delegates to underlying set equality
func (d *Depset) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	other := y.(*Depset)
	switch op {
	case syntax.EQL:
		return d.equal(other), nil
	case syntax.NEQ:
		return !d.equal(other), nil
	default:
		return false, fmt.Errorf("depset does not support %s", op)
	}
}

// equal checks if two depsets are equal.
// Two depsets are equal if they have the same order and produce the same elements.
// Reference: Depset.java equals() - delegates to NestedSet.equals()
func (d *Depset) equal(other *Depset) bool {
	if d == other {
		return true
	}
	if d.order != other.order {
		return false
	}
	// Compare flattened lists
	list1 := d.ToList()
	list2 := other.ToList()
	if len(list1) != len(list2) {
		return false
	}
	for i := range list1 {
		eq, err := starlark.Equal(list1[i], list2[i])
		if err != nil || !eq {
			return false
		}
	}
	return true
}

// Attr returns an attribute of the depset.
func (d *Depset) Attr(name string) (starlark.Value, error) {
	switch name {
	case "to_list":
		return starlark.NewBuiltin("depset.to_list", d.toListMethod), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("depset has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (d *Depset) AttrNames() []string {
	return []string{"to_list"}
}

// toListMethod is the Starlark method depset.to_list().
// Reference: Depset.java toListForStarlark() lines 348-350
func (d *Depset) toListMethod(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("depset.to_list", args, kwargs); err != nil {
		return nil, err
	}
	list := d.ToList()
	return starlark.NewList(list), nil
}

// ToList returns the elements of the depset as a slice, with duplicates removed.
// The order of elements depends on the depset's traversal order.
// Reference: NestedSet.java toList() line 484 and actualChildrenToList() lines 492-501
func (d *Depset) ToList() []starlark.Value {
	seen := make(map[uint32][]starlark.Value) // hash -> values with that hash
	var result []starlark.Value

	d.walk(d.order, seen, &result)

	// For topological order, reverse the result
	// Reference: NestedSet.java actualChildrenToList() line 500
	// "return getOrder() == Order.LINK_ORDER ? list.reverse() : list"
	if d.order == OrderTopological {
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}

	return result
}

// walk performs the depth-first traversal of the depset.
// Reference: NestedSet.java constructor lines 186-198 and walk() lines 621-631
//
// The order of visiting direct vs transitive depends on the order type:
//   - STABLE_ORDER (default), COMPILE_ORDER (postorder), LINK_ORDER (topological): preorder = false
//   - NAIVE_LINK_ORDER (preorder): preorder = true
func (d *Depset) walk(order Order, seen map[uint32][]starlark.Value, result *[]starlark.Value) {
	// Reference: NestedSet.java constructor lines 186-198
	// preorder means direct elements first, then transitive
	preorder := order == OrderPreorder

	if preorder {
		// Direct elements first, then transitive
		d.addDirect(seen, result)
		d.addTransitive(order, seen, result)
	} else {
		// Transitive first, then direct elements
		d.addTransitive(order, seen, result)
		d.addDirect(seen, result)
	}
}

// addDirect adds direct elements to the result, skipping duplicates.
func (d *Depset) addDirect(seen map[uint32][]starlark.Value, result *[]starlark.Value) {
	for _, elem := range d.direct {
		if !d.alreadySeen(seen, elem) {
			*result = append(*result, elem)
		}
	}
}

// addTransitive recursively adds transitive elements to the result.
func (d *Depset) addTransitive(order Order, seen map[uint32][]starlark.Value, result *[]starlark.Value) {
	for _, t := range d.transitive {
		t.walk(order, seen, result)
	}
}

// alreadySeen checks if an element has already been seen and adds it if not.
// Uses hash-based deduplication as in the reference implementation.
// Reference: NestedSet.java uses CompactHashSet for deduplication (line 610)
func (d *Depset) alreadySeen(seen map[uint32][]starlark.Value, elem starlark.Value) bool {
	h, err := elem.Hash()
	if err != nil {
		// Unhashable element - use string representation as fallback
		h = hashString(elem.String())
	}

	for _, v := range seen[h] {
		if eq, _ := starlark.Equal(v, elem); eq {
			return true
		}
	}
	seen[h] = append(seen[h], elem)
	return false
}

// hashString computes a simple hash for a string.
func hashString(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}

// Order returns the depset's traversal order.
func (d *Depset) Order() Order {
	return d.order
}

// ElementType returns the type name of the depset's elements.
// Returns "" for empty depsets.
// Reference: Depset.java getElementType() lines 292-297
func (d *Depset) ElementType() string {
	return d.elemType
}

// DirectItems returns the direct items in the depset.
// Reference: NestedSet.java getLeaves() lines 715-728
func (d *Depset) DirectItems() []starlark.Value {
	result := make([]starlark.Value, len(d.direct))
	copy(result, d.direct)
	return result
}

// TransitiveSets returns the transitive depsets.
// Reference: NestedSet.java getNonLeaves() lines 696-709
func (d *Depset) TransitiveSets() []*Depset {
	result := make([]*Depset, len(d.transitive))
	copy(result, d.transitive)
	return result
}

// DepsetBuiltin is the depset() constructor for Starlark.
// Signature: depset(direct=None, order="default", transitive=None)
// Reference: Depset.java DepsetLibrary.depset() lines 571-631
func DepsetBuiltin(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		direct     starlark.Value = starlark.None
		orderStr   string         = "default"
		transitive starlark.Value = starlark.None
	)

	// Reference: Depset.java DepsetLibrary parameter definitions lines 598-624
	// Parameters are: direct (positional or keyword), order (keyword), transitive (keyword-only)
	if err := starlark.UnpackArgs("depset", args, kwargs,
		"direct?", &direct,
		"order?", &orderStr,
		"transitive??", &transitive,
	); err != nil {
		return nil, err
	}

	// Parse order
	// Reference: Depset.java depset() lines 526-530
	order, err := ParseOrder(orderStr)
	if err != nil {
		return nil, err
	}

	// Convert direct to slice
	// Reference: Depset.java line 536 - Sequence.noneableCast(direct, Object.class, "direct")
	var directSlice []starlark.Value
	if direct != starlark.None {
		iter := starlark.Iterate(direct)
		if iter == nil {
			return nil, fmt.Errorf("depset: for parameter 'direct': got %s, want iterable", direct.Type())
		}
		defer iter.Done()
		var elem starlark.Value
		for iter.Next(&elem) {
			directSlice = append(directSlice, elem)
		}
	}

	// Convert transitive to slice of depsets
	// Reference: Depset.java line 537 - Sequence.noneableCast(transitive, Depset.class, "transitive")
	var transitiveSlice []*Depset
	if transitive != starlark.None {
		iter := starlark.Iterate(transitive)
		if iter == nil {
			return nil, fmt.Errorf("depset: for parameter 'transitive': got %s, want iterable", transitive.Type())
		}
		defer iter.Done()
		var elem starlark.Value
		for iter.Next(&elem) {
			ds, ok := elem.(*Depset)
			if !ok {
				return nil, fmt.Errorf("depset: for parameter 'transitive': got %s in list, want depset", elem.Type())
			}
			transitiveSlice = append(transitiveSlice, ds)
		}
	}

	return NewDepset(order, directSlice, transitiveSlice)
}

// DepsetUnion creates a new depset that is the union of two depsets.
// This is an O(1) operation that creates a new depset with both inputs as transitive members.
func DepsetUnion(a, b *Depset) (*Depset, error) {
	// Determine the resulting order
	order := a.order
	if order == OrderDefault {
		order = b.order
	}
	return NewDepset(order, nil, []*Depset{a, b})
}

// DepsetOf creates a depset from a slice of values with default order.
func DepsetOf(items []starlark.Value) (*Depset, error) {
	return NewDepset(OrderDefault, items, nil)
}
