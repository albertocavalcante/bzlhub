package builtins

import (
	"fmt"
	"slices"

	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// Provider is the Starlark provider() builtin function.
//
// Signature:
//
//	provider(doc = None, fields = None, init = None)
//
// Returns:
//   - If init is not specified: returns a Provider callable
//   - If init is specified: returns a tuple (Provider, raw_constructor)
//
// The Provider is used both as:
//   - A callable to create instances: MyInfo(x=1, y=2)
//   - A key to look up provider instances from a target: target[MyInfo]
//
// Parameters:
//   - doc: Optional documentation string for the provider
//   - fields: Either a list of field names, or a dict mapping field names to documentation.
//     If specified, the provider instances can only have these fields.
//   - init: Optional callback for preprocessing and validating field values during instantiation.
//     When specified, returns a tuple (provider, raw_constructor) instead of just the provider.
//
// Reference: bazel/src/main/java/com/google/devtools/build/lib/starlarkbuildapi/StarlarkRuleFunctionsApi.java
func Provider(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		doc    starlark.Value = starlark.None
		fields starlark.Value = starlark.None
		init   starlark.Value = starlark.None
	)

	if err := starlark.UnpackArgs("provider", args, kwargs,
		"doc?", &doc,
		"fields?", &fields,
		"init?", &init,
	); err != nil {
		return nil, err
	}

	// Parse doc
	var docStr string
	if doc != starlark.None {
		s, ok := doc.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("provider: doc must be a string, got %s", doc.Type())
		}
		docStr = string(s)
	}

	// Parse fields
	var fieldList []string
	var fieldDocs map[string]string
	if fields != starlark.None {
		switch f := fields.(type) {
		case *starlark.List:
			// fields is a list of field names
			iter := f.Iterate()
			defer iter.Done()
			var x starlark.Value
			for iter.Next(&x) {
				s, ok := x.(starlark.String)
				if !ok {
					return nil, fmt.Errorf("provider: fields list must contain strings, got %s", x.Type())
				}
				fieldList = append(fieldList, string(s))
			}
		case starlark.Tuple:
			// fields is a tuple of field names (also allowed)
			for i := range f.Len() {
				x := f.Index(i)
				s, ok := x.(starlark.String)
				if !ok {
					return nil, fmt.Errorf("provider: fields tuple must contain strings, got %s", x.Type())
				}
				fieldList = append(fieldList, string(s))
			}
		case *starlark.Dict:
			// fields is a dict mapping field name -> documentation
			fieldDocs = make(map[string]string)
			for _, item := range f.Items() {
				key, ok := item[0].(starlark.String)
				if !ok {
					return nil, fmt.Errorf("provider: fields dict keys must be strings, got %s", item[0].Type())
				}
				val, ok := item[1].(starlark.String)
				if !ok {
					return nil, fmt.Errorf("provider: fields dict values must be strings, got %s", item[1].Type())
				}
				name := string(key)
				fieldList = append(fieldList, name)
				fieldDocs[name] = string(val)
			}
		default:
			return nil, fmt.Errorf("provider: fields must be a list or dict, got %s", fields.Type())
		}
	}

	// Parse init
	var initFn starlark.Callable
	if init != starlark.None {
		fn, ok := init.(starlark.Callable)
		if !ok {
			return nil, fmt.Errorf("provider: init must be callable, got %s", init.Type())
		}
		initFn = fn
	}

	// Create the provider
	provider := types.NewProvider("", fieldList, docStr, initFn)

	// If init is specified, return a tuple (provider, raw_constructor)
	if initFn != nil {
		// The raw constructor bypasses the init function
		rawConstructor := &rawProviderConstructor{provider: provider}
		return starlark.Tuple{provider, rawConstructor}, nil
	}

	return provider, nil
}

// rawProviderConstructor is returned along with the provider when init is specified.
// It creates provider instances directly without going through the init function.
type rawProviderConstructor struct {
	provider *types.Provider
}

var (
	_ starlark.Value    = (*rawProviderConstructor)(nil)
	_ starlark.Callable = (*rawProviderConstructor)(nil)
)

// String returns the Starlark representation.
func (r *rawProviderConstructor) String() string {
	return fmt.Sprintf("<raw constructor for %s>", r.provider.Name())
}

// Type returns "function".
func (r *rawProviderConstructor) Type() string { return "function" }

// Freeze marks as frozen.
func (r *rawProviderConstructor) Freeze() {}

// Truth returns true.
func (r *rawProviderConstructor) Truth() starlark.Bool { return true }

// Hash returns an error.
func (r *rawProviderConstructor) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: function")
}

// Name returns the name for the callable.
func (r *rawProviderConstructor) Name() string {
	return r.provider.Name() + " (raw constructor)"
}

// CallInternal creates a provider instance directly without calling init.
func (r *rawProviderConstructor) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Raw constructor only accepts keyword arguments
	if len(args) > 0 {
		return nil, fmt.Errorf("%s: unexpected positional arguments", r.provider.Name())
	}

	// Build values dict from kwargs
	values := make(map[string]starlark.Value)
	fields := r.provider.Fields()
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))

		// Validate field if fields are specified
		if len(fields) > 0 && !slices.Contains(fields, key) {
			return nil, fmt.Errorf("%s: unexpected field %q", r.provider.Name(), key)
		}
		values[key] = kv[1]
	}

	return types.NewProviderInstance(r.provider, values), nil
}
