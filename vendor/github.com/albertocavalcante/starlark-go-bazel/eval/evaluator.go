// Package eval provides the Starlark evaluation engine for Bazel files.
//
// This package implements evaluation of BUILD and .bzl files following Bazel's semantics.
// It provides separate evaluation paths for:
// - BUILD files: Creates targets and collects declared rules
// - .bzl files: Exports globals (functions, providers, etc.) for loading
package eval

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/albertocavalcante/starlark-go-bazel/builtins"
	"github.com/albertocavalcante/starlark-go-bazel/loader"
	"github.com/albertocavalcante/starlark-go-bazel/types"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

// Evaluator evaluates Starlark files (BUILD and .bzl).
type Evaluator struct {
	bzlLoader        loader.BzlLoader
	fileLoader       loader.Loader
	predeclaredBzl   starlark.StringDict
	predeclaredBuild starlark.StringDict
	printHandler     func(msg string)
	loadResolver     func(*starlark.Thread, string) (starlark.StringDict, error)
	cache            map[string]*CachedModule
}

// CachedModule holds a cached module evaluation result.
type CachedModule struct {
	Globals starlark.StringDict
	Err     error
}

// Options configures the Evaluator.
type Options struct {
	BzlLoader        loader.BzlLoader
	FileLoader       loader.Loader
	PredeclaredBzl   starlark.StringDict
	PredeclaredBuild starlark.StringDict
	PrintHandler     func(msg string)

	// LoadResolver, when non-nil, REPLACES the default thread.Load
	// handler for both .bzl and BUILD evaluation. Consumers wiring
	// analysis-mode loaders (stub.LoaderFor + a tryReal hook into a
	// local mirror) supply this. When nil, the existing BzlLoader /
	// FileLoader chain handles loads.
	LoadResolver func(*starlark.Thread, string) (starlark.StringDict, error)
}

// New creates a new Evaluator.
func New(opts Options) *Evaluator {
	predeclaredBzl := makeBzlPredeclared()
	for k, v := range opts.PredeclaredBzl {
		predeclaredBzl[k] = v
	}

	predeclaredBuild := makeBuildPredeclared()
	for k, v := range opts.PredeclaredBuild {
		predeclaredBuild[k] = v
	}

	return &Evaluator{
		bzlLoader:        opts.BzlLoader,
		fileLoader:       opts.FileLoader,
		predeclaredBzl:   predeclaredBzl,
		predeclaredBuild: predeclaredBuild,
		printHandler:     opts.PrintHandler,
		loadResolver:     opts.LoadResolver,
		cache:            make(map[string]*CachedModule),
	}
}

// BzlResult contains the result of evaluating a .bzl file.
type BzlResult struct {
	Globals starlark.StringDict
}

// BuildResult contains the result of evaluating a BUILD file.
type BuildResult struct {
	Targets map[string]*types.RuleInstance
	Globals starlark.StringDict
	Package string
}

// EvalBzl evaluates a .bzl file and returns its exports.
func (e *Evaluator) EvalBzl(path string, source []byte) (*BzlResult, error) {
	dir := filepath.Dir(path)
	pkg := strings.TrimPrefix(dir, "/")
	if pkg == "." {
		pkg = ""
	}

	thread := &starlark.Thread{
		Name:  path,
		Print: e.makePrintHandler(),
	}

	switch {
	case e.loadResolver != nil:
		thread.Load = e.loadResolver
	case e.bzlLoader != nil:
		thread.Load = loader.MakeLoadFunc(e.bzlLoader)
		loader.SetBzlLoader(thread, e.bzlLoader)
	default:
		thread.Load = e.makeLoadFunc()
	}
	loader.SetCurrentPackage(thread, pkg)

	globals, err := starlark.ExecFile(thread, path, source, e.predeclaredBzl)
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", path, err)
	}

	return &BzlResult{Globals: globals}, nil
}

// EvalBzlFromAST evaluates a .bzl file from a pre-parsed *syntax.File.
// Identical to EvalBzl in every respect except it skips the parse step,
// which is the dominant cost for callers that already had to parse the
// source for other reasons (load-symbol scanning before constructing a
// LoadResolver, AST walks for indexing, etc.).
//
// The passed file is mutated during resolve (per
// starlark.FileProgram's contract). Callers must not pass the same
// file twice or share it across goroutines.
func (e *Evaluator) EvalBzlFromAST(path string, parsed *syntax.File) (*BzlResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("evaluating %s: nil syntax.File", path)
	}

	dir := filepath.Dir(path)
	pkg := strings.TrimPrefix(dir, "/")
	if pkg == "." {
		pkg = ""
	}

	thread := &starlark.Thread{
		Name:  path,
		Print: e.makePrintHandler(),
	}

	switch {
	case e.loadResolver != nil:
		thread.Load = e.loadResolver
	case e.bzlLoader != nil:
		thread.Load = loader.MakeLoadFunc(e.bzlLoader)
		loader.SetBzlLoader(thread, e.bzlLoader)
	default:
		thread.Load = e.makeLoadFunc()
	}
	loader.SetCurrentPackage(thread, pkg)

	prog, err := starlark.FileProgram(parsed, e.predeclaredBzl.Has)
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", path, err)
	}
	globals, err := prog.Init(thread, e.predeclaredBzl)
	globals.Freeze()
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", path, err)
	}
	return &BzlResult{Globals: globals}, nil
}

// EvalBuild evaluates a BUILD file and returns its targets.
func (e *Evaluator) EvalBuild(path string, source []byte) (*BuildResult, error) {
	dir := filepath.Dir(path)
	pkg := strings.TrimPrefix(dir, "/")
	if pkg == "." {
		pkg = ""
	}

	thread := &starlark.Thread{
		Name:  path,
		Print: e.makePrintHandler(),
	}

	switch {
	case e.loadResolver != nil:
		thread.Load = e.loadResolver
	case e.bzlLoader != nil:
		thread.Load = loader.MakeLoadFunc(e.bzlLoader)
		loader.SetBzlLoader(thread, e.bzlLoader)
	default:
		thread.Load = e.makeLoadFunc()
	}
	loader.SetCurrentPackage(thread, pkg)

	targets := make(map[string]*types.RuleInstance)
	thread.SetLocal("targets", targets)

	globals, err := starlark.ExecFile(thread, path, source, e.predeclaredBuild)
	if err != nil {
		return nil, fmt.Errorf("evaluating %s: %w", path, err)
	}

	return &BuildResult{
		Targets: targets,
		Globals: globals,
		Package: pkg,
	}, nil
}

// EvalBzlFile loads and evaluates a .bzl file from the filesystem.
func (e *Evaluator) EvalBzlFile(path string) (*BzlResult, error) {
	if e.fileLoader == nil {
		return nil, fmt.Errorf("no file loader configured")
	}
	source, err := e.fileLoader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return e.EvalBzl(path, source)
}

// EvalBuildFile loads and evaluates a BUILD file from the filesystem.
func (e *Evaluator) EvalBuildFile(path string) (*BuildResult, error) {
	if e.fileLoader == nil {
		return nil, fmt.Errorf("no file loader configured")
	}
	source, err := e.fileLoader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return e.EvalBuild(path, source)
}

func (e *Evaluator) makePrintHandler() func(*starlark.Thread, string) {
	return func(_ *starlark.Thread, msg string) {
		if e.printHandler != nil {
			e.printHandler(msg)
		}
	}
}

func (e *Evaluator) makeLoadFunc() func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	return func(thread *starlark.Thread, module string) (starlark.StringDict, error) {
		if cached, ok := e.cache[module]; ok {
			return cached.Globals, cached.Err
		}

		if e.fileLoader == nil {
			return nil, fmt.Errorf("no loader configured for module %q", module)
		}

		source, err := e.fileLoader.Load(module)
		if err != nil {
			e.cache[module] = &CachedModule{Err: err}
			return nil, err
		}

		newThread := &starlark.Thread{
			Name:  module,
			Load:  e.makeLoadFunc(),
			Print: thread.Print,
		}

		globals, err := starlark.ExecFile(newThread, module, source, e.predeclaredBzl)
		e.cache[module] = &CachedModule{Globals: globals, Err: err}

		return globals, err
	}
}

func makeBzlPredeclared() starlark.StringDict {
	return starlark.StringDict{
		"Label":            starlark.NewBuiltin("Label", types.LabelBuiltin),
		"provider":         starlark.NewBuiltin("provider", providerBuiltin),
		"struct":           starlark.NewBuiltin("struct", starlarkstruct.Make),
		"depset":           starlark.NewBuiltin("depset", types.DepsetBuiltin),
		"rule":             starlark.NewBuiltin("rule", types.RuleBuiltin),
		"repository_rule":  starlark.NewBuiltin("repository_rule", builtins.RepositoryRule),
		"module_extension": starlark.NewBuiltin("module_extension", builtins.ModuleExtension),
		"tag_class":        starlark.NewBuiltin("tag_class", builtins.TagClass),
		"attr":             newAttrModule(),
		// .bzl files routinely reference `native.*` inside helper /
		// macro function bodies (native.package_name, native.glob,
		// native.existing_rule, etc.) even though those calls are
		// only legal when invoked from a BUILD context. Starlark
		// resolves identifiers at compile time, so an undefined
		// `native` makes the .bzl fail to load even if the helpers
		// never actually run.
		//
		// Provide `native` as a permissive stub: it answers any
		// attribute lookup with a callable that returns None / empty
		// string / empty list, depending on the surface most-likely
		// shape. That keeps module-load eval succeeding (rule()
		// registration completes) without pretending to faithfully
		// execute BUILD-context functions.
		"native": newNativeStub(),
		"True":   starlark.True,
		"False":  starlark.False,
		"None":   starlark.None,
	}
}

// nativeStub is a starlark.HasAttrs implementation that returns a
// stub builtin for any attribute name. Calling that builtin returns
// None — enough to keep .bzl evaluation going past `native.foo(...)`
// expressions even when foo isn't a recognized native function.
type nativeStub struct{}

func newNativeStub() *nativeStub { return &nativeStub{} }

func (*nativeStub) String() string                           { return "<native (stub)>" }
func (*nativeStub) Type() string                             { return "native" }
func (*nativeStub) Freeze()                                  {}
func (*nativeStub) Truth() starlark.Bool                     { return starlark.True }
func (*nativeStub) Hash() (uint32, error)                    { return 0, fmt.Errorf("unhashable: native") }
func (*nativeStub) AttrNames() []string                      { return nil }
func (*nativeStub) Attr(name string) (starlark.Value, error) {
	return starlark.NewBuiltin("native."+name, func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
		// Conservative return: None is safe in any expression
		// position. Callers that ASSIGN the result and then iterate
		// will fail at iter time, which is the same outcome as a
		// real BUILD-context call returning unexpected shape — not
		// the introspection bug we care about preventing.
		return starlark.None, nil
	}), nil
}

func makeBuildPredeclared() starlark.StringDict {
	return starlark.StringDict{
		"Label":         starlark.NewBuiltin("Label", types.LabelBuiltin),
		"struct":        starlark.NewBuiltin("struct", starlarkstruct.Make),
		"depset":        starlark.NewBuiltin("depset", types.DepsetBuiltin),
		"package":       starlark.NewBuiltin("package", PackageBuiltin),
		"licenses":      starlark.NewBuiltin("licenses", LicensesBuiltin),
		"exports_files": starlark.NewBuiltin("exports_files", ExportsFilesBuiltin),
		"glob":          starlark.NewBuiltin("glob", GlobBuiltin),
		"True":          starlark.True,
		"False":         starlark.False,
		"None":          starlark.None,
	}
}

func providerBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		doc    string
		fields *starlark.List
		init   starlark.Callable
	)

	if err := starlark.UnpackArgs("provider", args, kwargs,
		"doc?", &doc,
		"fields?", &fields,
		"init?", &init,
	); err != nil {
		return nil, err
	}

	var fieldNames []string
	if fields != nil {
		iter := fields.Iterate()
		defer iter.Done()
		var v starlark.Value
		for iter.Next(&v) {
			s, ok := v.(starlark.String)
			if !ok {
				return nil, fmt.Errorf("provider: fields must be strings, got %s", v.Type())
			}
			fieldNames = append(fieldNames, string(s))
		}
	}

	return types.NewProvider("", fieldNames, doc, init), nil
}

type attrModule struct{}

var _ starlark.HasAttrs = (*attrModule)(nil)

func newAttrModule() *attrModule {
	return &attrModule{}
}

func (m *attrModule) String() string        { return "<module attr>" }
func (m *attrModule) Type() string          { return "module" }
func (m *attrModule) Freeze()               {}
func (m *attrModule) Truth() starlark.Bool  { return true }
func (m *attrModule) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: module") }

func (m *attrModule) Attr(name string) (starlark.Value, error) {
	switch name {
	case "string":
		return starlark.NewBuiltin("attr.string", attrFactory(types.AttrTypeString)), nil
	case "string_list":
		return starlark.NewBuiltin("attr.string_list", attrFactory(types.AttrTypeStringList)), nil
	case "string_dict":
		return starlark.NewBuiltin("attr.string_dict", attrFactory(types.AttrTypeStringDict)), nil
	case "string_list_dict":
		return starlark.NewBuiltin("attr.string_list_dict", attrFactory(types.AttrTypeStringDict)), nil
	case "int":
		return starlark.NewBuiltin("attr.int", attrFactory(types.AttrTypeInt)), nil
	case "int_list":
		return starlark.NewBuiltin("attr.int_list", attrFactory(types.AttrTypeInt)), nil
	case "bool":
		return starlark.NewBuiltin("attr.bool", attrFactory(types.AttrTypeBool)), nil
	case "label":
		return starlark.NewBuiltin("attr.label", attrFactory(types.AttrTypeLabel)), nil
	case "label_list":
		return starlark.NewBuiltin("attr.label_list", attrFactory(types.AttrTypeLabelList)), nil
	case "label_keyed_string_dict":
		return starlark.NewBuiltin("attr.label_keyed_string_dict", attrFactory(types.AttrTypeLabel)), nil
	case "output":
		return starlark.NewBuiltin("attr.output", attrFactory(types.AttrTypeOutput)), nil
	case "output_list":
		return starlark.NewBuiltin("attr.output_list", attrFactory(types.AttrTypeOutputList)), nil
	default:
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("attr has no attribute %q", name))
	}
}

func (m *attrModule) AttrNames() []string {
	return []string{
		"bool", "int", "int_list", "label", "label_keyed_string_dict", "label_list",
		"output", "output_list", "string", "string_dict", "string_list", "string_list_dict",
	}
}

func attrFactory(attrType types.AttrType) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			return nil, fmt.Errorf("%s: unexpected positional arguments", b.Name())
		}

		desc := &types.AttrDescriptor{
			Type:       attrType,
			AllowEmpty: true,
		}

		for _, kv := range kwargs {
			key := string(kv[0].(starlark.String))
			val := kv[1]

			switch key {
			case "mandatory":
				if b, ok := val.(starlark.Bool); ok {
					desc.Mandatory = bool(b)
				}
			case "default":
				desc.Default = val
			case "doc":
				if s, ok := val.(starlark.String); ok {
					desc.Doc = string(s)
				}
			case "allow_empty":
				if b, ok := val.(starlark.Bool); ok {
					desc.AllowEmpty = bool(b)
				}
			case "allow_files", "allow_single_file", "executable", "providers":
				// Handle these options
			}
		}

		return &attrDescriptorValue{desc: desc}, nil
	}
}

type attrDescriptorValue struct {
	desc *types.AttrDescriptor
}

var _ starlark.Value = (*attrDescriptorValue)(nil)

func (a *attrDescriptorValue) String() string       { return fmt.Sprintf("<attr.%s>", a.desc.Type) }
func (a *attrDescriptorValue) Type() string         { return "Attribute" }
func (a *attrDescriptorValue) Freeze()              {}
func (a *attrDescriptorValue) Truth() starlark.Bool { return true }
func (a *attrDescriptorValue) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: Attribute")
}
func (a *attrDescriptorValue) Descriptor() *types.AttrDescriptor { return a.desc }
