package syntaxutil

import "go.starlark.net/syntax"

// ImportedSymbol describes one local-binding name introduced via a
// top-level load() statement.
type ImportedSymbol struct {
	// ModulePath is the verbatim first-arg string of the load() call:
	// `":foo.bzl"`, `"//pkg:foo.bzl"`, `"@external//..."`, etc. Pass
	// to [ResolveLoadedFile] to normalize against the calling file's
	// directory.
	ModulePath string
	// OriginalName is the symbol's name in the loaded module (the
	// kwarg value for `load(":x.bzl", local = "remote")` cases).
	// Local-binding-name → original-name renames are common.
	OriginalName string
}

// CollectLoads returns the per-file map of local-binding name to
// import descriptor by scanning a parsed file's top-level load()
// statements.
//
// Important: [syntax.LoadStmt]'s field doc comments are misleading.
// Despite the godoc saying "From: name defined in loading module"
// and "To: name in loaded module", the parser actually populates
// them the other way: `To[i].Name` is the LOCAL-binding name (the
// alias the caller's other code references) and `From[i].Name` is
// the ORIGINAL name in the loaded module. See vendor/go.starlark.net
// syntax/parse.go where `to = append(to, id)` captures the kwarg
// key (= local) and `from = append(from, &Ident{...})` captures the
// string value (= original).
//
// This map keys on local-binding name (caller's namespace) so a
// downstream resolver can do `loads[localName]` directly.
//
// Both bzlwalk (Tier-2 attrs resolution) and hermetic (cross-file
// integrity-dict resolution) consume the same shape; one source of
// truth makes the parallel structure explicit.
func CollectLoads(f *syntax.File) map[string]ImportedSymbol {
	out := map[string]ImportedSymbol{}
	if f == nil {
		return out
	}
	for _, stmt := range f.Stmts {
		load, ok := stmt.(*syntax.LoadStmt)
		if !ok {
			continue
		}
		if load.Module == nil {
			continue
		}
		modulePath, ok := load.Module.Value.(string)
		if !ok {
			continue
		}
		// See the function godoc: To holds local-binding names,
		// From holds original names. Iterate in tandem.
		for i, to := range load.To {
			if to == nil || i >= len(load.From) {
				continue
			}
			from := load.From[i]
			if from == nil {
				continue
			}
			out[to.Name] = ImportedSymbol{
				ModulePath:   modulePath,
				OriginalName: from.Name,
			}
		}
	}
	return out
}
