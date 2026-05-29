package builtins

import (
	"fmt"

	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// ModuleExtension implements the module_extension() Bazel builtin.
// Kwargs match the Bazel surface (verified at
// StarlarkRepositoryModule.java:210):
//
//   implementation: callable, required
//   tag_classes:    dict<string, tag_class>, default {}
//   doc:            string, default None
//   environ:        list<string>, default [] — DEPRECATED, migrate to
//                   module_ctx.getenv
//   os_dependent:   bool, default False (Bazel 7+)
//   arch_dependent: bool, default False (Bazel 7+)
//
// Returns a *types.ModuleExtensionClass.
func ModuleExtension(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("module_extension: positional arguments not allowed")
	}

	var impl starlark.Callable
	tagClasses := map[string]*types.TagClass{}
	var opts []types.ModuleExtensionOption

	for _, kv := range kwargs {
		key, _ := starlark.AsString(kv[0])
		switch key {
		case "implementation":
			c, ok := kv[1].(starlark.Callable)
			if !ok {
				return nil, fmt.Errorf("module_extension: implementation must be callable, got %s", kv[1].Type())
			}
			impl = c
		case "tag_classes":
			if kv[1] == starlark.None {
				continue
			}
			d, ok := kv[1].(*starlark.Dict)
			if !ok {
				return nil, fmt.Errorf("module_extension: tag_classes must be dict, got %s", kv[1].Type())
			}
			for _, k := range d.Keys() {
				name, _ := starlark.AsString(k)
				v, _, _ := d.Get(k)
				tc, ok := v.(*types.TagClass)
				if !ok {
					return nil, fmt.Errorf("module_extension: tag_classes[%q] must be tag_class, got %s", name, v.Type())
				}
				tc.SetName(name)
				tagClasses[name] = tc
			}
		case "doc":
			if kv[1] == starlark.None {
				continue
			}
			s, ok := starlark.AsString(kv[1])
			if !ok {
				return nil, fmt.Errorf("module_extension: doc must be string or None")
			}
			opts = append(opts, types.WithExtDoc(s))
		case "environ":
			env, err := stringList(kv[1], "module_extension.environ")
			if err != nil {
				return nil, err
			}
			opts = append(opts, types.WithExtEnviron(env))
		case "os_dependent":
			b, ok := kv[1].(starlark.Bool)
			if !ok {
				return nil, fmt.Errorf("module_extension: os_dependent must be bool")
			}
			opts = append(opts, types.WithOsDependent(bool(b)))
		case "arch_dependent":
			b, ok := kv[1].(starlark.Bool)
			if !ok {
				return nil, fmt.Errorf("module_extension: arch_dependent must be bool")
			}
			opts = append(opts, types.WithArchDependent(bool(b)))
		}
	}

	return types.NewModuleExtensionClass(impl, tagClasses, opts...), nil
}
