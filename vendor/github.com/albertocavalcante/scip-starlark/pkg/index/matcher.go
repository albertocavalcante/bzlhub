package scipstarlark

import (
	"path/filepath"
	"strings"
)

// matcherForDialect returns the default file matcher for the given dialect
// preset. An empty string is treated as "plain".
//
// All presets accept *.star and *.bzl. Each preset additively recognises the
// fixed filenames / extensions documented in docs/plans/phase-0-design.md.
func matcherForDialect(dialect string) func(relPath string) bool {
	switch dialect {
	case "", "plain":
		return matchPlain
	case "bazel":
		return matchBazel
	case "buck2":
		return matchBuck2
	case "copybara":
		return matchCopybara
	case "tilt":
		return matchTilt
	default:
		// Unknown dialect: behave like "plain" rather than reject. Phase 0
		// is permissive; callers can validate Dialect themselves.
		return matchPlain
	}
}

func matchPlain(relPath string) bool {
	return hasStarlarkExt(relPath)
}

func matchBazel(relPath string) bool {
	if hasStarlarkExt(relPath) {
		return true
	}
	switch filepath.Base(relPath) {
	case "BUILD", "BUILD.bazel", "WORKSPACE", "WORKSPACE.bazel", "MODULE.bazel":
		return true
	}
	return false
}

func matchBuck2(relPath string) bool {
	if hasStarlarkExt(relPath) {
		return true
	}
	switch filepath.Base(relPath) {
	case "BUCK", "PACKAGE":
		return true
	}
	return false
}

func matchCopybara(relPath string) bool {
	if hasStarlarkExt(relPath) {
		return true
	}
	return strings.HasSuffix(relPath, ".bara.sky")
}

func matchTilt(relPath string) bool {
	if hasStarlarkExt(relPath) {
		return true
	}
	if strings.HasSuffix(relPath, ".tiltfile") {
		return true
	}
	return filepath.Base(relPath) == "Tiltfile"
}

func hasStarlarkExt(relPath string) bool {
	ext := filepath.Ext(relPath)
	return ext == ".star" || ext == ".bzl"
}
