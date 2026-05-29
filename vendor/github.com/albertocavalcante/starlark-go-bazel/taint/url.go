package taint

import (
	"strings"

	"go.starlark.net/starlark"
)

// FlattenURLs accepts the value passed to ctx.download(url=...) /
// ctx.download_and_extract(url=...) — which may be a single string,
// a list of strings, or an iterable yielding strings — and returns
// the URLs plus a bool indicating whether any source was tainted.
//
// Taint sources:
//   - A *stub.Permissive value used as URL → URL "<unresolved>", tainted.
//   - A starlark.String containing the Marker substring → tainted.
//
// The Permissive type itself lives in package stub (M4); to avoid an
// import cycle, this function detects taint via interface assertion
// on the IsPermissive marker interface.
func FlattenURLs(v starlark.Value) (urls []string, tainted bool) {
	if v == nil {
		return nil, false
	}
	if isPermissive(v) {
		return []string{"<unresolved>"}, true
	}
	if s, ok := starlark.AsString(v); ok {
		return []string{s}, strings.Contains(s, Marker)
	}
	iter := starlark.Iterate(v)
	if iter == nil {
		return nil, false
	}
	defer iter.Done()
	var item starlark.Value
	for iter.Next(&item) {
		if isPermissive(item) {
			urls = append(urls, "<unresolved>")
			tainted = true
		} else if s, ok := starlark.AsString(item); ok {
			urls = append(urls, s)
			if strings.Contains(s, Marker) {
				tainted = true
			}
		}
	}
	return urls, tainted
}

// IsPermissive is the marker interface implemented by stub.Permissive.
// Defined here so taint code can detect Permissive without importing
// stub (avoiding the import cycle taint ← stub ← taint).
type IsPermissive interface {
	IsPermissive()
}

func isPermissive(v starlark.Value) bool {
	_, ok := v.(IsPermissive)
	return ok
}
