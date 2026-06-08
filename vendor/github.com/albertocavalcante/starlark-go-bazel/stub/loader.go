package stub

import (
	"maps"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// LoaderFor returns a *starlark.Thread.Load function that, for each
// load() target, consults tryReal first and falls back to Shared
// (Permissive) for any name tryReal didn't supply. The pre-parsed
// symbol map tells the loader which symbols each module needs to
// expose; pass nil to scan the source file at load() time instead
// (more work but no pre-parse required).
//
// Use cases:
//   - bzl.Interpreter in ModeLenient/ModeAnalysis: external @ loads
//     soft-resolve to Shared so eval doesn't abort at name resolution.
//   - canopy's airgap analysis: tryReal hooks into the local mirror;
//     anything not mirrored falls back to Permissive.
//
// tryReal returns (globals, true) when it can resolve a module;
// (nil, false) means "let Permissive fill in." When tryReal is nil,
// every load is treated as unresolvable and stubbed entirely.
func LoaderFor(symbolsByModule map[string][]string, tryReal func(module string) (starlark.StringDict, bool)) func(*starlark.Thread, string) (starlark.StringDict, error) {
	cache := map[string]starlark.StringDict{}
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		if cached, ok := cache[module]; ok {
			return cached, nil
		}
		out := starlark.StringDict{}
		if tryReal != nil {
			if resolved, ok := tryReal(module); ok {
				maps.Copy(out, resolved)
			}
		}
		for _, sym := range symbolsByModule[module] {
			if _, ok := out[sym]; !ok {
				out[sym] = Shared
			}
		}
		cache[module] = out
		return out, nil
	}
}

// ScanLoads returns the per-module list of From-side symbol names
// requested by each load() in the file. For
// `load("@x//:y.bzl", "foo", baz_local = "baz")` it stores
// "@x//:y.bzl" -> ["foo", "baz"]. Pair with LoaderFor to tell the
// permissive loader which names need stubbing.
func ScanLoads(f *syntax.File) map[string][]string {
	out := map[string][]string{}
	if f == nil {
		return out
	}
	for _, stmt := range f.Stmts {
		ls, ok := stmt.(*syntax.LoadStmt)
		if !ok {
			continue
		}
		if ls.Module == nil {
			continue
		}
		modStr, ok := ls.Module.Value.(string)
		if !ok {
			continue
		}
		for _, from := range ls.From {
			if from != nil && from.Name != "" {
				out[modStr] = append(out[modStr], from.Name)
			}
		}
	}
	return out
}
