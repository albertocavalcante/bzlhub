// Package toolinfo derives an indexer's tool version from Go build metadata.
package toolinfo

import (
	"runtime/debug"
	"strings"
)

// ModuleVersion returns the tagged version of module in the running binary.
// Local builds and replacements report "dev" instead of claiming a release.
func ModuleVersion(module string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return moduleVersionFromBuildInfo(info, module)
}

func moduleVersionFromBuildInfo(info *debug.BuildInfo, module string) string {
	if info == nil {
		return "dev"
	}
	if info.Main.Path == module || strings.HasPrefix(info.Main.Path, module+"/") {
		return normalizeVersion(info.Main.Version)
	}
	for _, dep := range info.Deps {
		if dep.Path == module {
			if dep.Replace != nil {
				return "dev"
			}
			return normalizeVersion(dep.Version)
		}
	}
	return "dev"
}

func normalizeVersion(version string) string {
	if version == "" || version == "(devel)" {
		return "dev"
	}
	return strings.TrimPrefix(version, "v")
}
