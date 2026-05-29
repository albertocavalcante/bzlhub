package builtins

import (
	"fmt"

	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// RepositoryRule implements the repository_rule() Bazel builtin.
// Kwargs match the Bazel surface (verified at
// StarlarkRepositoryModule.java:57):
//
//   implementation: callable, required
//   attrs:          dict<string, attr.*>, default None
//   local:          bool, default False
//   environ:        list<string>, default [] — DEPRECATED, migrate to
//                   repository_ctx.getenv
//   configure:      bool, default False
//   remotable:      bool, default False — experimental
//   doc:            string, default None
//
// Returns a *types.RepositoryRuleClass.
func RepositoryRule(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("repository_rule: positional arguments not allowed")
	}

	var impl starlark.Callable
	attrs := map[string]starlark.Value{}
	var opts []types.RepositoryRuleOption

	for _, kv := range kwargs {
		key, _ := starlark.AsString(kv[0])
		switch key {
		case "implementation":
			c, ok := kv[1].(starlark.Callable)
			if !ok {
				return nil, fmt.Errorf("repository_rule: implementation must be callable, got %s", kv[1].Type())
			}
			impl = c
		case "attrs":
			if kv[1] == starlark.None {
				continue
			}
			d, ok := kv[1].(*starlark.Dict)
			if !ok {
				return nil, fmt.Errorf("repository_rule: attrs must be dict, got %s", kv[1].Type())
			}
			for _, k := range d.Keys() {
				name, _ := starlark.AsString(k)
				v, _, _ := d.Get(k)
				// attr.* values are wrapped privately by the eval
				// package; we store them by value here and let M3/M5
				// add typed extraction helpers.
				attrs[name] = v
			}
		case "local":
			b, ok := kv[1].(starlark.Bool)
			if !ok {
				return nil, fmt.Errorf("repository_rule: local must be bool")
			}
			opts = append(opts, types.WithLocal(bool(b)))
		case "configure":
			b, ok := kv[1].(starlark.Bool)
			if !ok {
				return nil, fmt.Errorf("repository_rule: configure must be bool")
			}
			opts = append(opts, types.WithConfigure(bool(b)))
		case "remotable":
			b, ok := kv[1].(starlark.Bool)
			if !ok {
				return nil, fmt.Errorf("repository_rule: remotable must be bool")
			}
			opts = append(opts, types.WithRemotable(bool(b)))
		case "environ":
			env, err := stringList(kv[1], "repository_rule.environ")
			if err != nil {
				return nil, err
			}
			opts = append(opts, types.WithRepoEnviron(env))
		case "doc":
			if kv[1] == starlark.None {
				continue
			}
			s, ok := starlark.AsString(kv[1])
			if !ok {
				return nil, fmt.Errorf("repository_rule: doc must be string or None")
			}
			opts = append(opts, types.WithRepoDoc(s))
		}
	}

	return types.NewRepositoryRuleClass(impl, attrs, opts...), nil
}

// stringList extracts a list of strings from a Starlark value.
// Used by builtins that accept list<string> kwargs.
func stringList(v starlark.Value, name string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	iter := starlark.Iterate(v)
	if iter == nil {
		return nil, fmt.Errorf("%s: expected iterable of strings, got %s", name, v.Type())
	}
	defer iter.Done()
	var out []string
	var item starlark.Value
	for iter.Next(&item) {
		s, ok := starlark.AsString(item)
		if !ok {
			return nil, fmt.Errorf("%s: element is not a string (got %s)", name, item.Type())
		}
		out = append(out, s)
	}
	return out, nil
}
