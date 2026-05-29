package builtins

import (
	"fmt"

	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
)

// TagClass implements the tag_class() Bazel builtin. Kwargs:
//
//   attrs: dict<string, attr.*>, default {}
//   doc:   string, default None
//
// Returns a *types.TagClass. The class is given its identifier name
// at module_extension registration time (when the dict key it's
// associated with is known).
func TagClass(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("tag_class: positional arguments not allowed")
	}

	attrs := map[string]starlark.Value{}
	var doc string

	for _, kv := range kwargs {
		key, _ := starlark.AsString(kv[0])
		switch key {
		case "attrs":
			if kv[1] == starlark.None {
				continue
			}
			d, ok := kv[1].(*starlark.Dict)
			if !ok {
				return nil, fmt.Errorf("tag_class: attrs must be dict, got %s", kv[1].Type())
			}
			for _, k := range d.Keys() {
				name, _ := starlark.AsString(k)
				v, _, _ := d.Get(k)
				attrs[name] = v
			}
		case "doc":
			if kv[1] == starlark.None {
				continue
			}
			s, ok := starlark.AsString(kv[1])
			if !ok {
				return nil, fmt.Errorf("tag_class: doc must be string or None")
			}
			doc = s
		}
	}

	return types.NewTagClass(attrs, doc), nil
}
