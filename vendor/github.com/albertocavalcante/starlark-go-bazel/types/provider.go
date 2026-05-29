package types

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Provider represents a Bazel provider schema created by provider().
type Provider struct {
	name   string
	fields []string
	doc    string
	init   starlark.Callable
	frozen bool
}

var (
	_ starlark.Value    = (*Provider)(nil)
	_ starlark.Callable = (*Provider)(nil)
	_ starlark.HasAttrs = (*Provider)(nil)
)

// NewProvider creates a new Provider with the given name and fields.
func NewProvider(name string, fields []string, doc string, init starlark.Callable) *Provider {
	return &Provider{
		name:   name,
		fields: fields,
		doc:    doc,
		init:   init,
	}
}

// String returns the Starlark representation.
func (p *Provider) String() string {
	return fmt.Sprintf("<provider %s>", p.name)
}

// Type returns "provider".
func (p *Provider) Type() string { return "provider" }

// Freeze marks the provider as frozen.
func (p *Provider) Freeze() { p.frozen = true }

// Truth returns true.
func (p *Provider) Truth() starlark.Bool { return true }

// Hash returns an error (providers are not hashable).
func (p *Provider) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: provider")
}

// Name returns the provider's name.
func (p *Provider) Name() string { return p.name }

// Fields returns the provider's declared fields.
func (p *Provider) Fields() []string { return p.fields }

// Doc returns the provider's documentation.
func (p *Provider) Doc() string { return p.doc }

// CallInternal implements starlark.Callable, creating a ProviderInstance.
func (p *Provider) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// If there's a custom init function, call it with the same args/kwargs
	if p.init != nil {
		// Call init with the exact same arguments
		result, err := starlark.Call(thread, p.init, args, kwargs)
		if err != nil {
			return nil, err
		}

		// The init function must return a dict
		dict, ok := result.(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("%s: init must return a dict, got %s", p.name, result.Type())
		}

		// Convert dict to provider instance values
		values := make(map[string]starlark.Value)
		for _, item := range dict.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("%s: init returned dict with non-string key: %s", p.name, item[0].Type())
			}
			keyStr := string(key)

			// Validate field if fields are specified
			if len(p.fields) > 0 && !slices.Contains(p.fields, keyStr) {
				return nil, fmt.Errorf("%s: unexpected field %q", p.name, keyStr)
			}
			values[keyStr] = item[1]
		}

		return &ProviderInstance{provider: p, values: values}, nil
	}

	// Without init, providers only accept keyword arguments
	if len(args) > 0 {
		return nil, fmt.Errorf("%s: unexpected positional arguments", p.name)
	}

	// Build values dict from kwargs
	values := make(map[string]starlark.Value)
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))

		// Validate field if fields are specified
		if len(p.fields) > 0 && !slices.Contains(p.fields, key) {
			return nil, fmt.Errorf("%s: unexpected field %q", p.name, key)
		}
		values[key] = kv[1]
	}

	return &ProviderInstance{provider: p, values: values}, nil
}

// Attr returns an attribute of the provider.
func (p *Provider) Attr(name string) (starlark.Value, error) {
	switch name {
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("provider has no attribute %q", name))
	}
}

// AttrNames returns the list of attribute names.
func (p *Provider) AttrNames() []string {
	return []string{}
}

// ProviderInstance represents an instance of a provider with values.
type ProviderInstance struct {
	provider *Provider
	values   map[string]starlark.Value
	frozen   bool
}

// NewProviderInstance creates a new provider instance with the given values.
func NewProviderInstance(provider *Provider, values map[string]starlark.Value) *ProviderInstance {
	return &ProviderInstance{provider: provider, values: values}
}

var (
	_ starlark.Value      = (*ProviderInstance)(nil)
	_ starlark.HasAttrs   = (*ProviderInstance)(nil)
	_ starlark.Comparable = (*ProviderInstance)(nil)
)

// String returns the Starlark representation.
func (pi *ProviderInstance) String() string {
	var sb strings.Builder
	sb.WriteString(pi.provider.name)
	sb.WriteString("(")

	keys := make([]string, 0, len(pi.values))
	for k := range pi.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		sb.WriteString(" = ")
		sb.WriteString(pi.values[k].String())
	}
	sb.WriteString(")")
	return sb.String()
}

// Type returns the provider name.
func (pi *ProviderInstance) Type() string { return pi.provider.name }

// Freeze marks the instance as frozen.
func (pi *ProviderInstance) Freeze() {
	if pi.frozen {
		return
	}
	pi.frozen = true
	for _, v := range pi.values {
		v.Freeze()
	}
}

// Truth returns true.
func (pi *ProviderInstance) Truth() starlark.Bool { return true }

// Hash returns an error (provider instances are not hashable).
func (pi *ProviderInstance) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", pi.provider.name)
}

// Provider returns the provider schema.
func (pi *ProviderInstance) Provider() *Provider { return pi.provider }

// Get returns a field value.
func (pi *ProviderInstance) Get(name string) (starlark.Value, bool) {
	v, ok := pi.values[name]
	return v, ok
}

// Attr returns an attribute of the instance.
func (pi *ProviderInstance) Attr(name string) (starlark.Value, error) {
	if v, ok := pi.values[name]; ok {
		return v, nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("%s has no attribute %q", pi.provider.name, name))
}

// AttrNames returns the list of attribute names.
func (pi *ProviderInstance) AttrNames() []string {
	names := make([]string, 0, len(pi.values))
	for k := range pi.values {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// CompareSameType implements comparison (only equality).
func (pi *ProviderInstance) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	other, ok := y.(*ProviderInstance)
	if !ok {
		return false, nil
	}

	switch op {
	case syntax.EQL:
		return pi == other, nil // Identity comparison
	case syntax.NEQ:
		return pi != other, nil
	default:
		return false, fmt.Errorf("provider instances support only == and !=")
	}
}

// ToDict converts the instance to a Starlark dict.
func (pi *ProviderInstance) ToDict() *starlark.Dict {
	d := starlark.NewDict(len(pi.values))
	for k, v := range pi.values {
		_ = d.SetKey(starlark.String(k), v)
	}
	return d
}
