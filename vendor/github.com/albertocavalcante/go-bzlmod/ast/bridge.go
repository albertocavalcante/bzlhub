package ast

import (
	"github.com/albertocavalcante/go-bzlmod/label"
)

// LegacyModuleInfo represents the information extracted from a MODULE.bazel file.
// This type bridges the new AST to the legacy API.
type LegacyModuleInfo struct {
	Name               string             `json:"name"`
	Version            string             `json:"version"`
	CompatibilityLevel int                `json:"compatibility_level"`
	Dependencies       []LegacyDependency `json:"dependencies"`
	Overrides          []LegacyOverride   `json:"overrides"`
}

// LegacyDependency represents a bazel_dep declaration.
type LegacyDependency struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	RepoName      string `json:"repo_name,omitempty"`
	DevDependency bool   `json:"dev_dependency"`
}

// LegacyOverride represents various override types.
type LegacyOverride struct {
	Type       string `json:"type"`
	ModuleName string `json:"module_name"`
	Version    string `json:"version,omitempty"`
	Registry   string `json:"registry,omitempty"`
	// Git override fields
	Remote         string `json:"remote,omitempty"`
	Commit         string `json:"commit,omitempty"`
	Tag            string `json:"tag,omitempty"`
	Branch         string `json:"branch,omitempty"`
	InitSubmodules bool   `json:"init_submodules,omitempty"`
	// Archive override fields
	URLs      []string `json:"urls,omitempty"`
	Integrity string   `json:"integrity,omitempty"`
	// Local path override fields
	Path string `json:"path,omitempty"`
	// Common patch fields
	StripPrefix string   `json:"strip_prefix,omitempty"`
	Patches     []string `json:"patches,omitempty"`
	PatchCmds   []string `json:"patch_cmds,omitempty"`
	PatchStrip  int      `json:"patch_strip,omitempty"`
}

// ToLegacyModuleInfo converts a parsed ModuleFile to the legacy ModuleInfo format.
func (f *ModuleFile) ToLegacyModuleInfo() *LegacyModuleInfo {
	info := &LegacyModuleInfo{
		Dependencies: make([]LegacyDependency, 0),
		Overrides:    make([]LegacyOverride, 0),
	}

	for _, stmt := range f.Statements {
		switch s := stmt.(type) {
		case *ModuleDecl:
			info.Name = s.Name.String()
			info.Version = s.Version.String()
			info.CompatibilityLevel = s.CompatibilityLevel

		case *BazelDep:
			info.Dependencies = append(info.Dependencies, LegacyDependency{
				Name:          s.Name.String(),
				Version:       s.Version.String(),
				RepoName:      s.RepoName.String(),
				DevDependency: s.DevDependency,
			})

		case *SingleVersionOverride:
			info.Overrides = append(info.Overrides, LegacyOverride{
				Type:       "single_version",
				ModuleName: s.Module.String(),
				Version:    s.Version.String(),
				Registry:   s.Registry,
				Patches:    s.Patches,
				PatchCmds:  s.PatchCmds,
				PatchStrip: s.PatchStrip,
			})

		case *MultipleVersionOverride:
			info.Overrides = append(info.Overrides, LegacyOverride{
				Type:       "multiple_version",
				ModuleName: s.Module.String(),
				// Note: multiple versions aren't easily represented in flat structure
			})

		case *GitOverride:
			info.Overrides = append(info.Overrides, LegacyOverride{
				Type:           "git",
				ModuleName:     s.Module.String(),
				Remote:         s.Remote,
				Commit:         s.Commit,
				Tag:            s.Tag,
				Branch:         s.Branch,
				InitSubmodules: s.InitSubmodules,
				StripPrefix:    s.StripPrefix,
				Patches:        s.Patches,
				PatchCmds:      s.PatchCmds,
				PatchStrip:     s.PatchStrip,
			})

		case *ArchiveOverride:
			info.Overrides = append(info.Overrides, LegacyOverride{
				Type:        "archive",
				ModuleName:  s.Module.String(),
				URLs:        s.URLs,
				Integrity:   s.Integrity,
				StripPrefix: s.StripPrefix,
				Patches:     s.Patches,
				PatchCmds:   s.PatchCmds,
				PatchStrip:  s.PatchStrip,
			})

		case *LocalPathOverride:
			info.Overrides = append(info.Overrides, LegacyOverride{
				Type:       "local_path",
				ModuleName: s.Module.String(),
				Path:       s.Path,
			})
		}
	}

	return info
}

// ModuleInfoCollector is a handler that collects all information into a LegacyModuleInfo struct.
type ModuleInfoCollector struct {
	BaseHandler
	Info *LegacyModuleInfo
}

// NewModuleInfoCollector creates a new collector.
func NewModuleInfoCollector() *ModuleInfoCollector {
	return &ModuleInfoCollector{
		Info: &LegacyModuleInfo{
			Dependencies: make([]LegacyDependency, 0),
			Overrides:    make([]LegacyOverride, 0),
		},
	}
}

func (c *ModuleInfoCollector) Module(name label.Module, version label.Version, compatLevel int, repoName label.ApparentRepo) error {
	c.Info.Name = name.String()
	c.Info.Version = version.String()
	c.Info.CompatibilityLevel = compatLevel
	return nil
}

func (c *ModuleInfoCollector) BazelDep(name label.Module, version label.Version, maxCompat int, repoName label.ApparentRepo, devDep bool) error {
	c.Info.Dependencies = append(c.Info.Dependencies, LegacyDependency{
		Name:          name.String(),
		Version:       version.String(),
		RepoName:      repoName.String(),
		DevDependency: devDep,
	})
	return nil
}

func (c *ModuleInfoCollector) SingleVersionOverride(moduleName label.Module, version label.Version, registry string, patches, patchCmds []string, patchStrip int) error {
	c.Info.Overrides = append(c.Info.Overrides, LegacyOverride{
		Type:       "single_version",
		ModuleName: moduleName.String(),
		Version:    version.String(),
		Registry:   registry,
		Patches:    patches,
		PatchCmds:  patchCmds,
		PatchStrip: patchStrip,
	})
	return nil
}

func (c *ModuleInfoCollector) GitOverride(moduleName label.Module, remote, commit, tag, branch string, patches, patchCmds []string, patchStrip int, initSubmodules bool, stripPrefix string) error {
	c.Info.Overrides = append(c.Info.Overrides, LegacyOverride{
		Type:           "git",
		ModuleName:     moduleName.String(),
		Remote:         remote,
		Commit:         commit,
		Tag:            tag,
		Branch:         branch,
		InitSubmodules: initSubmodules,
		StripPrefix:    stripPrefix,
		Patches:        patches,
		PatchCmds:      patchCmds,
		PatchStrip:     patchStrip,
	})
	return nil
}

func (c *ModuleInfoCollector) ArchiveOverride(moduleName label.Module, urls []string, integrity, stripPrefix string, patches, patchCmds []string, patchStrip int) error {
	c.Info.Overrides = append(c.Info.Overrides, LegacyOverride{
		Type:        "archive",
		ModuleName:  moduleName.String(),
		URLs:        urls,
		Integrity:   integrity,
		StripPrefix: stripPrefix,
		Patches:     patches,
		PatchCmds:   patchCmds,
		PatchStrip:  patchStrip,
	})
	return nil
}

func (c *ModuleInfoCollector) LocalPathOverride(moduleName label.Module, path string) error {
	c.Info.Overrides = append(c.Info.Overrides, LegacyOverride{
		Type:       "local_path",
		ModuleName: moduleName.String(),
		Path:       path,
	})
	return nil
}
