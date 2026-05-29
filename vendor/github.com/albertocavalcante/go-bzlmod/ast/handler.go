package ast

import (
	"github.com/albertocavalcante/go-bzlmod/label"
)

// Handler processes MODULE.bazel statements.
// Implement this interface to customize how MODULE.bazel content is handled.
// Each method returns an error to stop processing, or nil to continue.
type Handler interface {
	// Module is called for the module() declaration.
	Module(name label.Module, version label.Version, compatibilityLevel int, repoName label.ApparentRepo) error

	// BazelDep is called for each bazel_dep() declaration.
	BazelDep(name label.Module, version label.Version, maxCompatibilityLevel int, repoName label.ApparentRepo, devDependency bool) error

	// UseExtension is called for use_extension() declarations.
	UseExtension(extensionFile label.ApparentLabel, extensionName label.StarlarkIdentifier, devDependency, isolate bool) (ExtensionProxy, error)

	// UseRepo is called for use_repo() declarations.
	UseRepo(repos []string, devDependency bool) error

	// SingleVersionOverride is called for single_version_override().
	SingleVersionOverride(moduleName label.Module, version label.Version, registry string, patches []string, patchCmds []string, patchStrip int) error

	// MultipleVersionOverride is called for multiple_version_override().
	MultipleVersionOverride(moduleName label.Module, versions []label.Version, registry string) error

	// GitOverride is called for git_override().
	GitOverride(moduleName label.Module, remote, commit, tag, branch string, patches, patchCmds []string, patchStrip int, initSubmodules bool, stripPrefix string) error

	// ArchiveOverride is called for archive_override().
	ArchiveOverride(moduleName label.Module, urls []string, integrity, stripPrefix string, patches, patchCmds []string, patchStrip int) error

	// LocalPathOverride is called for local_path_override().
	LocalPathOverride(moduleName label.Module, path string) error

	// RegisterToolchains is called for register_toolchains().
	RegisterToolchains(patterns []string, devDependency bool) error

	// RegisterExecutionPlatforms is called for register_execution_platforms().
	RegisterExecutionPlatforms(patterns []string, devDependency bool) error

	// UnknownStatement is called for unrecognized function calls.
	UnknownStatement(name string, pos Position) error
}

// ExtensionProxy represents the return value of use_extension().
// It receives tag calls made on the extension.
type ExtensionProxy interface {
	// Tag is called for each tag method called on the proxy.
	Tag(name string, attrs map[string]any) error
}

// Walk traverses a ModuleFile and calls the handler for each statement.
func Walk(file *ModuleFile, handler Handler) error {
	for _, stmt := range file.Statements {
		if err := walkStatement(stmt, handler); err != nil {
			return err
		}
	}
	return nil
}

func walkStatement(stmt Statement, handler Handler) error {
	switch s := stmt.(type) {
	case *ModuleDecl:
		return handler.Module(s.Name, s.Version, s.CompatibilityLevel, s.RepoName)

	case *BazelDep:
		return handler.BazelDep(s.Name, s.Version, s.MaxCompatibilityLevel, s.RepoName, s.DevDependency)

	case *UseExtension:
		proxy, err := handler.UseExtension(s.ExtensionFile, s.ExtensionName, s.DevDependency, s.Isolate)
		if err != nil {
			return err
		}
		if proxy != nil {
			for _, tag := range s.Tags {
				if err := proxy.Tag(tag.Name, tag.Attributes); err != nil {
					return err
				}
			}
		}
		return nil

	case *UseRepo:
		return handler.UseRepo(s.Repos, s.DevDependency)

	case *SingleVersionOverride:
		return handler.SingleVersionOverride(s.Module, s.Version, s.Registry, s.Patches, s.PatchCmds, s.PatchStrip)

	case *MultipleVersionOverride:
		return handler.MultipleVersionOverride(s.Module, s.Versions, s.Registry)

	case *GitOverride:
		return handler.GitOverride(s.Module, s.Remote, s.Commit, s.Tag, s.Branch, s.Patches, s.PatchCmds, s.PatchStrip, s.InitSubmodules, s.StripPrefix)

	case *ArchiveOverride:
		return handler.ArchiveOverride(s.Module, s.URLs, s.Integrity, s.StripPrefix, s.Patches, s.PatchCmds, s.PatchStrip)

	case *LocalPathOverride:
		return handler.LocalPathOverride(s.Module, s.Path)

	case *RegisterToolchains:
		return handler.RegisterToolchains(s.Patterns, s.DevDependency)

	case *RegisterExecutionPlatforms:
		return handler.RegisterExecutionPlatforms(s.Patterns, s.DevDependency)

	case *UnknownStatement:
		return handler.UnknownStatement(s.FuncName, s.Pos)
	}

	return nil
}

// BaseHandler provides default no-op implementations of all Handler methods.
// Embed this in your handler to only implement the methods you need.
//
// Example:
//
//	type MyHandler struct {
//	    ast.BaseHandler
//	    deps []string
//	}
//
//	func (h *MyHandler) BazelDep(name label.Module, ...) error {
//	    h.deps = append(h.deps, name.String())
//	    return nil
//	}
type BaseHandler struct{}

func (h *BaseHandler) Module(label.Module, label.Version, int, label.ApparentRepo) error { return nil }
func (h *BaseHandler) BazelDep(label.Module, label.Version, int, label.ApparentRepo, bool) error {
	return nil
}
func (h *BaseHandler) UseExtension(label.ApparentLabel, label.StarlarkIdentifier, bool, bool) (ExtensionProxy, error) {
	return nil, nil
}
func (h *BaseHandler) UseRepo([]string, bool) error { return nil }
func (h *BaseHandler) SingleVersionOverride(label.Module, label.Version, string, []string, []string, int) error {
	return nil
}
func (h *BaseHandler) MultipleVersionOverride(label.Module, []label.Version, string) error {
	return nil
}
func (h *BaseHandler) GitOverride(label.Module, string, string, string, string, []string, []string, int, bool, string) error {
	return nil
}
func (h *BaseHandler) ArchiveOverride(label.Module, []string, string, string, []string, []string, int) error {
	return nil
}
func (h *BaseHandler) LocalPathOverride(label.Module, string) error    { return nil }
func (h *BaseHandler) RegisterToolchains([]string, bool) error         { return nil }
func (h *BaseHandler) RegisterExecutionPlatforms([]string, bool) error { return nil }
func (h *BaseHandler) UnknownStatement(string, Position) error         { return nil }

// DependencyCollector is a handler that collects all bazel_dep declarations.
type DependencyCollector struct {
	BaseHandler
	Dependencies []BazelDepInfo
}

// BazelDepInfo contains information about a bazel_dep.
type BazelDepInfo struct {
	Name                  label.Module
	Version               label.Version
	MaxCompatibilityLevel int
	RepoName              label.ApparentRepo
	DevDependency         bool
}

func (c *DependencyCollector) BazelDep(name label.Module, version label.Version, maxCompat int, repoName label.ApparentRepo, devDep bool) error {
	c.Dependencies = append(c.Dependencies, BazelDepInfo{
		Name:                  name,
		Version:               version,
		MaxCompatibilityLevel: maxCompat,
		RepoName:              repoName,
		DevDependency:         devDep,
	})
	return nil
}

// OverrideCollector is a handler that collects all override declarations.
// Use Walk(file, collector) to populate the slices with override information.
type OverrideCollector struct {
	BaseHandler
	SingleVersionOverrides   []SingleVersionOverrideInfo
	MultipleVersionOverrides []MultipleVersionOverrideInfo
	GitOverrides             []GitOverrideInfo
	ArchiveOverrides         []ArchiveOverrideInfo
	LocalPathOverrides       []LocalPathOverrideInfo
}

// SingleVersionOverrideInfo holds data from a single_version_override() call.
type SingleVersionOverrideInfo struct {
	ModuleName label.Module
	Version    label.Version
	Registry   string
	Patches    []string
	PatchCmds  []string
	PatchStrip int
}

// MultipleVersionOverrideInfo holds data from a multiple_version_override() call.
type MultipleVersionOverrideInfo struct {
	ModuleName label.Module
	Versions   []label.Version
	Registry   string
}

// GitOverrideInfo holds data from a git_override() call.
type GitOverrideInfo struct {
	ModuleName     label.Module
	Remote         string
	Commit         string
	Tag            string
	Branch         string
	Patches        []string
	PatchCmds      []string
	PatchStrip     int
	InitSubmodules bool
	StripPrefix    string
}

// ArchiveOverrideInfo holds data from an archive_override() call.
type ArchiveOverrideInfo struct {
	ModuleName  label.Module
	URLs        []string
	Integrity   string
	StripPrefix string
	Patches     []string
	PatchCmds   []string
	PatchStrip  int
}

// LocalPathOverrideInfo holds data from a local_path_override() call.
type LocalPathOverrideInfo struct {
	ModuleName label.Module
	Path       string
}

func (c *OverrideCollector) SingleVersionOverride(moduleName label.Module, version label.Version, registry string, patches, patchCmds []string, patchStrip int) error {
	c.SingleVersionOverrides = append(c.SingleVersionOverrides, SingleVersionOverrideInfo{
		ModuleName: moduleName,
		Version:    version,
		Registry:   registry,
		Patches:    patches,
		PatchCmds:  patchCmds,
		PatchStrip: patchStrip,
	})
	return nil
}

func (c *OverrideCollector) MultipleVersionOverride(moduleName label.Module, versions []label.Version, registry string) error {
	c.MultipleVersionOverrides = append(c.MultipleVersionOverrides, MultipleVersionOverrideInfo{
		ModuleName: moduleName,
		Versions:   versions,
		Registry:   registry,
	})
	return nil
}

func (c *OverrideCollector) GitOverride(moduleName label.Module, remote, commit, tag, branch string, patches, patchCmds []string, patchStrip int, initSubmodules bool, stripPrefix string) error {
	c.GitOverrides = append(c.GitOverrides, GitOverrideInfo{
		ModuleName:     moduleName,
		Remote:         remote,
		Commit:         commit,
		Tag:            tag,
		Branch:         branch,
		Patches:        patches,
		PatchCmds:      patchCmds,
		PatchStrip:     patchStrip,
		InitSubmodules: initSubmodules,
		StripPrefix:    stripPrefix,
	})
	return nil
}

func (c *OverrideCollector) ArchiveOverride(moduleName label.Module, urls []string, integrity, stripPrefix string, patches, patchCmds []string, patchStrip int) error {
	c.ArchiveOverrides = append(c.ArchiveOverrides, ArchiveOverrideInfo{
		ModuleName:  moduleName,
		URLs:        urls,
		Integrity:   integrity,
		StripPrefix: stripPrefix,
		Patches:     patches,
		PatchCmds:   patchCmds,
		PatchStrip:  patchStrip,
	})
	return nil
}

func (c *OverrideCollector) LocalPathOverride(moduleName label.Module, path string) error {
	c.LocalPathOverrides = append(c.LocalPathOverrides, LocalPathOverrideInfo{
		ModuleName: moduleName,
		Path:       path,
	})
	return nil
}
