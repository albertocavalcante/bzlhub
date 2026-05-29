// Package conv converts Go values to starlark.Value for invocation of
// Bazel-dialect Starlark code from Go callers.
//
// The Bazel ecosystem routinely round-trips tag attributes, BCR
// metadata, and repo-rule attrs through JSON — but `encoding/json`
// only produces the loose set {string, bool, float64, []any,
// map[string]any, nil}. Drivers that need to hand these values to a
// Starlark function must rebuild them as starlark.Value first;
// FromGo is the canonical converter for that path.
package conv

import (
	"go.starlark.net/starlark"
)

// FromGo converts a JSON-decoded Go value into a starlark.Value.
//
// Handled types: string, bool, float64 (JSON's number type, integer-shaped
// values are coerced to starlark.Int), []any (→ *starlark.List),
// map[string]any (→ *starlark.Dict), nil (→ starlark.None).
//
// Unknown Go types fall back to starlark.None rather than silently
// stringifying — a wrong-type-but-shaped value could change eval results
// in subtle ways, so it's safer to deny than to coerce. Callers with
// typed numeric inputs (json.Number, int64, …) should normalize to the
// JSON-decoded shape before calling FromGo, or extend this package.
func FromGo(v any) starlark.Value {
	switch t := v.(type) {
	case string:
		return starlark.String(t)
	case bool:
		return starlark.Bool(t)
	case float64:
		// JSON numbers come back as float64; round-trip the int64 cast
		// to detect integer-shaped values. MakeInt64 avoids the silent
		// truncation that int(t) would cause on 32-bit GOARCH for
		// values above MaxInt32 (10-digit unix timestamps are routine).
		if i := int64(t); t == float64(i) {
			return starlark.MakeInt64(i)
		}
		return starlark.Float(t)
	case []any:
		list := starlark.NewList(make([]starlark.Value, 0, len(t)))
		for _, item := range t {
			_ = list.Append(FromGo(item))
		}
		return list
	case map[string]any:
		d := starlark.NewDict(len(t))
		for k, vv := range t {
			_ = d.SetKey(starlark.String(k), FromGo(vv))
		}
		return d
	case nil:
		return starlark.None
	}
	return starlark.None
}
