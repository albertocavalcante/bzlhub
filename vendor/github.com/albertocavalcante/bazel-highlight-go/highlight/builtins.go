package highlight

// BuiltinInfo carries the renderable metadata for a curated Bazel
// builtin: the canonical name (sometimes used as a dispatch key),
// the bazel.build documentation URL, and a short blurb suitable for
// hover/tooltip surfaces.
//
// We embed these directly in Token.Meta when promoting an Identifier
// to KindBuiltin, so consumers don't need a parallel lookup table.
type BuiltinInfo struct {
	Name        string
	URL         string
	Description string
}

// Builtins is the curated registry of well-known Bazel global
// functions, rules, and helpers. The set is small on purpose — these
// are the names every BUILD file uses. Rule-set-specific names
// (cc_binary, java_library) come from the rule sets themselves and
// don't belong here; consumers needing those should layer SCIP
// indexing on top.
//
// HEURISTIC — curated list.
//
//	Why it exists: there's no single source of truth for "is this a
//	Bazel builtin?" Bazel's own docs page lists them but the format
//	isn't machine-consumable; the alternative is parsing the page
//	or running Bazel itself (heavyweight).
//
//	Why deferred: a deterministic alternative would scrape and
//	import bazel.build's reference docs at library build time.
//	Doable but adds CI complexity for marginal gain — the curated
//	set covers the common case and gets updated in one place.
//
//	Why acceptable: this is the ONLY heuristic in the bazel
//	dialect. Renderers degrade gracefully when a name isn't in the
//	set (it stays a plain Identifier, no broken UX). Tests pin the
//	current contents.
var Builtins = map[string]BuiltinInfo{
	"filegroup":           {"filegroup", "https://bazel.build/reference/be/general#filegroup", "Groups a collection of source files together for use in rules."},
	"genrule":             {"genrule", "https://bazel.build/reference/be/general#genrule", "A generic rule that runs an arbitrary command."},
	"glob":                {"glob", "https://bazel.build/reference/be/functions#glob", "Returns a list of file paths matching include and exclude patterns."},
	"select":              {"select", "https://bazel.build/reference/be/functions#select", "Helper function for configurable attributes."},
	"package":             {"package", "https://bazel.build/reference/be/functions#package", "Declares package-level metadata applying to every rule in the file."},
	"package_group":       {"package_group", "https://bazel.build/reference/be/functions#package_group", "Declares a named set of packages for visibility lists."},
	"licenses":            {"licenses", "https://bazel.build/reference/be/functions#licenses", "Declares the license types of the targets in the package."},
	"exports_files":       {"exports_files", "https://bazel.build/reference/be/functions#exports_files", "Allows source files to be referenced from other packages."},
	"existing_rule":       {"existing_rule", "https://bazel.build/rules/lib/native#existing_rule", "Returns a dict describing one previously-declared rule in the package, or None."},
	"existing_rules":      {"existing_rules", "https://bazel.build/rules/lib/native#existing_rules", "Returns a dict describing every previously-declared rule in the package."},
	"native":              {"native", "https://bazel.build/rules/lib/native", "Module providing access to native rules from Starlark."},
	"rule":                {"rule", "https://bazel.build/rules/lib/globals/bzl#rule", "Creates a new Starlark rule."},
	"provider":            {"provider", "https://bazel.build/rules/lib/globals/bzl#provider", "Defines a new Starlark provider."},
	"aspect":              {"aspect", "https://bazel.build/rules/lib/globals/bzl#aspect", "Creates a new aspect."},
	"attr":                {"attr", "https://bazel.build/rules/lib/toplevel/attr", "Module declaring attribute schemas for rule definitions."},
	"depset":              {"depset", "https://bazel.build/rules/lib/builtins/depset", "Constructs an efficiently-mergeable set."},
	"struct":              {"struct", "https://bazel.build/rules/lib/builtins/struct", "Creates a new struct with the given field values."},
	"repository_rule":     {"repository_rule", "https://bazel.build/rules/lib/globals/bzl#repository_rule", "Creates a new repository rule."},
	"module_extension":    {"module_extension", "https://bazel.build/rules/lib/globals/bzl#module_extension", "Creates a new module extension."},
	"tag_class":           {"tag_class", "https://bazel.build/rules/lib/globals/bzl#tag_class", "Creates a new tag class for use in module extensions."},
	"register_toolchains": {"register_toolchains", "https://bazel.build/rules/lib/globals/module#register_toolchains", "Registers one or more toolchains."},
	"register_execution_platforms": {"register_execution_platforms", "https://bazel.build/rules/lib/globals/module#register_execution_platforms", "Registers execution platforms."},
	"bazel_dep":           {"bazel_dep", "https://bazel.build/rules/lib/globals/module#bazel_dep", "Declares a direct dependency on another Bazel module."},
	"module":              {"module", "https://bazel.build/rules/lib/globals/module#module", "Declares properties of the current Bazel module."},
	"use_extension":       {"use_extension", "https://bazel.build/rules/lib/globals/module#use_extension", "Returns a proxy object for a module extension."},
	"use_repo":            {"use_repo", "https://bazel.build/rules/lib/globals/module#use_repo", "Imports one or more repos generated by a module extension."},
	"override_dep":        {"override_dep", "https://bazel.build/rules/lib/globals/module#single_version_override", "Overrides a transitive dependency."},
	"single_version_override": {"single_version_override", "https://bazel.build/rules/lib/globals/module#single_version_override", "Pins a transitive dependency to a single version."},
	"git_override":        {"git_override", "https://bazel.build/rules/lib/globals/module#git_override", "Overrides a dependency to use a Git source."},
	"archive_override":    {"archive_override", "https://bazel.build/rules/lib/globals/module#archive_override", "Overrides a dependency to use an archive source."},
	"local_path_override": {"local_path_override", "https://bazel.build/rules/lib/globals/module#local_path_override", "Overrides a dependency to use a local checkout."},
}
