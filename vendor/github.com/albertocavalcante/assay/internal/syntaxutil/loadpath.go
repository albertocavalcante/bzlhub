package syntaxutil

import (
	"path"
	"path/filepath"
	"strings"
)

// ResolveLoadedFile normalizes a Bazel load() module path against the
// calling file's location, returning the module-relative file path
// of the target. Returns ok=false for external loads (which can't be
// resolved without the external repo on disk) or for unrecognized
// syntaxes.
//
// Forms handled (caller is "pkg/sub/defs.bzl"):
//
//	":foo.bzl"              -> "pkg/sub/foo.bzl"
//	":sub/foo.bzl"          -> "pkg/sub/sub/foo.bzl"
//	"//pkg:foo.bzl"         -> "pkg/foo.bzl"
//	"//pkg/sub:foo.bzl"     -> "pkg/sub/foo.bzl"
//	"//:foo.bzl"            -> "foo.bzl"
//	"//foo.bzl"             -> "foo.bzl"
//	"@//pkg:foo.bzl"        -> "pkg/foo.bzl"        (Bazel 7+ canonical-local)
//	"@external//..."        -> bail
//	"@@external//..."       -> bail
func ResolveLoadedFile(callerRelPath, loadPath string) (string, bool) {
	// "@//pkg:..." is the canonical-name-of-this-module form Bazel 7+
	// uses internally; treat as local.
	if strings.HasPrefix(loadPath, "@//") {
		loadPath = loadPath[1:]
	}
	if strings.HasPrefix(loadPath, "@") {
		return "", false // external repository
	}

	var pkg, file string
	switch {
	case strings.HasPrefix(loadPath, "//"):
		rest := loadPath[2:]
		if before, after, ok := strings.Cut(rest, ":"); ok {
			pkg = before
			file = after
		} else {
			file = rest
		}
	case strings.HasPrefix(loadPath, ":"):
		callerDir := path.Dir(filepath.ToSlash(callerRelPath))
		if callerDir == "." {
			callerDir = ""
		}
		pkg = callerDir
		file = loadPath[1:]
	default:
		return "", false
	}

	if file == "" {
		return "", false
	}
	if pkg == "" {
		return file, true
	}
	return path.Join(pkg, file), true
}
