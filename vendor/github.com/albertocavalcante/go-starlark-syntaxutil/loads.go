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
// Note: despite what the [syntax.LoadStmt] field godocs suggest, the
// parser populates To[i].Name with the LOCAL-binding name (the alias
// the caller references) and From[i].Name with the ORIGINAL name in
// the loaded module. This function keys on the local-binding name.
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
