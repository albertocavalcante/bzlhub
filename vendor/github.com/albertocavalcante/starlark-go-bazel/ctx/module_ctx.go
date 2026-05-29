package ctx

import (
	"fmt"

	"github.com/albertocavalcante/starlark-go-bazel/taint"
	"github.com/albertocavalcante/starlark-go-bazel/version"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// ModuleCtx is the synthetic module_ctx passed to a module_extension's
// impl. Mirrors Bazel's module_ctx surface from
// ModuleExtensionContext.java + StarlarkBaseExternalContext.java.
//
// Inherits the shared base methods (download, execute, etc.) from
// RepositoryCtx's pattern; adds module-extension-specific surface
// (modules, is_dev_dependency, extension_metadata, facts, is_isolated,
// root_module_has_non_dev_dependency).
type ModuleCtx struct {
	modules []ModuleSpec
	osName  string
	osArch  string
	osEnv   map[string]string
	ver     version.Version
	sinks   *taint.Sinks
	tainted bool
}

// ModuleSpec describes one bazel_module visible to a module_extension's
// impl. Mirrors MODULE.bazel's use_extension(...).<tag>(...) callsites.
type ModuleSpec struct {
	Name           string
	Version        string
	IsRoot         bool
	IsDevDep       bool
	Tags           map[string][]TagInstance
}

// TagInstance is one tag-class instantiation in MODULE.bazel.
type TagInstance struct {
	Attrs    map[string]starlark.Value
	IsDevDep bool
}

// ModuleCtxOptions configures a synthetic module_ctx.
type ModuleCtxOptions struct {
	Modules                       []ModuleSpec
	OSName                        string
	OSArch                        string
	OSEnv                         map[string]string
	Version                       version.Version
	Sinks                         *taint.Sinks
	RootModuleHasNonDevDependency bool
}

func NewModuleCtx(opts ModuleCtxOptions) *ModuleCtx {
	return &ModuleCtx{
		modules: opts.Modules,
		osName:  opts.OSName,
		osArch:  opts.OSArch,
		osEnv:   opts.OSEnv,
		ver:     opts.Version.Resolved(),
		sinks:   opts.Sinks,
	}
}

var (
	_ starlark.Value    = (*ModuleCtx)(nil)
	_ starlark.HasAttrs = (*ModuleCtx)(nil)
)

func (m *ModuleCtx) String() string        { return "<module_ctx>" }
func (m *ModuleCtx) Type() string          { return "module_ctx" }
func (m *ModuleCtx) Freeze()               {}
func (m *ModuleCtx) Truth() starlark.Bool  { return true }
func (m *ModuleCtx) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: module_ctx") }

func (m *ModuleCtx) Attr(name string) (starlark.Value, error) {
	switch name {
	case "modules":
		return m.modulesValue(), nil
	case "os":
		return &RepositoryOs{name: m.osName, arch: m.osArch, env: m.osEnv}, nil
	case "is_dev_dependency":
		return starlark.NewBuiltin("module_ctx.is_dev_dependency", m.isDevDependency), nil
	case "root_module_has_non_dev_dependency":
		return starlark.True, nil
	case "extension_metadata":
		return starlark.NewBuiltin("module_ctx.extension_metadata", noopNone), nil
	case "is_isolated":
		// Experimental — gated by --experimental_isolated_extension_usages.
		// For M3, return False (conservative).
		return starlark.False, nil
	case "facts":
		// Bazel 9+ only.
		if m.ver >= version.V9 || m.ver == version.VLatest {
			return starlark.NewDict(0), nil
		}
		return nil, nil
	// Shared base methods.
	case "download":
		return starlark.NewBuiltin("module_ctx.download", noopNone), nil
	case "download_and_extract":
		return starlark.NewBuiltin("module_ctx.download_and_extract", noopNone), nil
	case "execute":
		return starlark.NewBuiltin("module_ctx.execute", m.executeMethod), nil
	case "read":
		return starlark.NewBuiltin("module_ctx.read", m.readMethod), nil
	case "path":
		return starlark.NewBuiltin("module_ctx.path", passThroughFirst), nil
	case "getenv":
		if m.ver >= version.V8 || m.ver == version.VLatest {
			return starlark.NewBuiltin("module_ctx.getenv", m.getenvMethod), nil
		}
		return nil, nil
	case "watch":
		if m.ver >= version.V8 || m.ver == version.VLatest {
			return starlark.NewBuiltin("module_ctx.watch", noopNone), nil
		}
		return nil, nil
	}
	return nil, nil
}

func (m *ModuleCtx) AttrNames() []string {
	names := []string{
		"modules", "os", "is_dev_dependency", "root_module_has_non_dev_dependency",
		"extension_metadata", "is_isolated",
		"download", "download_and_extract", "execute", "read", "path",
	}
	if m.ver >= version.V8 || m.ver == version.VLatest {
		names = append(names, "getenv", "watch")
	}
	if m.ver >= version.V9 || m.ver == version.VLatest {
		names = append(names, "facts")
	}
	return names
}

func (m *ModuleCtx) modulesValue() starlark.Value {
	out := make([]starlark.Value, 0, len(m.modules))
	for _, mod := range m.modules {
		out = append(out, bazelModuleStruct(mod))
	}
	return starlark.NewList(out)
}

func (m *ModuleCtx) executeMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	m.tainted = true
	return starlark.None, nil
}

func (m *ModuleCtx) readMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	m.tainted = true
	return starlark.String(""), nil
}

func (m *ModuleCtx) getenvMethod(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) == 0 {
		return starlark.None, nil
	}
	name, ok := starlark.AsString(args[0])
	if !ok {
		return starlark.None, nil
	}
	if v, ok := m.osEnv[name]; ok {
		return starlark.String(v), nil
	}
	if len(args) > 1 {
		return args[1], nil
	}
	m.tainted = true
	return starlark.None, nil
}

func (m *ModuleCtx) isDevDependency(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// Argument is a tag instance. We can't easily look up its source
	// module here (the spike's TagInstance.IsDevDep would carry it).
	// Conservative: return false. M5 can route this through the
	// instance's parent ModuleSpec.IsDevDep.
	return starlark.False, nil
}

// bazelModuleStruct builds the Starlark struct exposed to extensions
// for one bazel_module entry. Fields: name, version, is_root,
// is_dev_dependency, tags.
func bazelModuleStruct(m ModuleSpec) starlark.Value {
	tags := starlark.StringDict{}
	for tagName, instances := range m.Tags {
		list := make([]starlark.Value, 0, len(instances))
		for _, inst := range instances {
			fields := starlark.StringDict{}
			for k, v := range inst.Attrs {
				fields[k] = v
			}
			list = append(list, starlarkstruct.FromStringDict(starlarkstruct.Default, fields))
		}
		tags[tagName] = starlark.NewList(list)
	}
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"name":              starlark.String(m.Name),
		"version":           starlark.String(m.Version),
		"is_root":           starlark.Bool(m.IsRoot),
		"is_dev_dependency": starlark.Bool(m.IsDevDep),
		"tags":              starlarkstruct.FromStringDict(starlarkstruct.Default, tags),
	})
}
