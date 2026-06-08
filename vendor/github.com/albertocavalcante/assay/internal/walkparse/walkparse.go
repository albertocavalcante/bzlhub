// Package walkparse owns the single shared filesystem walk + Starlark
// parse pass for assay's module-analysis pipeline. Both bzlwalk and
// hermetic consume the result so each .bzl file is parsed exactly once
// per Analyze() invocation regardless of how many extractors run.
//
// The two-walk historical layout (one each in bzlwalk + hermetic, plus
// their pre-passes — four walks total) is collapsed here. Callers that
// want standalone behavior still get one walk via bzlwalk.Walk /
// hermetic.Classify wrappers; the win comes when assay.Analyze drives
// both extractors with the same slice.
package walkparse

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"go.starlark.net/syntax"
)

// File describes one file encountered during the walk plus its parsed
// AST when applicable. Per-file parse failures don't abort — File.AST
// is nil and File.ParseErr carries the cause so downstream code can
// decide whether to log, skip, or fail.
type File struct {
	// Path is the module-relative path with forward slashes.
	Path string
	// Kind is "bzl", "build", "module", or "other".
	Kind string
	// Size is the file size in bytes from os.Stat.
	Size int64
	// AST is the parsed Starlark file when Kind is bzl/build/module
	// AND the parse succeeded. Nil otherwise.
	AST *syntax.File
	// ParseErr is the parse failure when AST is nil because parsing
	// (not file-kind filtering) prevented it.
	ParseErr error
}

// Walk traverses rootDir, classifies each file by kind, parses .bzl /
// BUILD / BUILD.bazel / MODULE.bazel via go.starlark.net, and returns
// the result. Hidden dirs and Bazel build-output dirs are skipped.
//
// Errors returned from this function are filesystem-traversal errors
// (the WalkDir callback returning a non-nil error). Per-file parse
// errors are recorded in File.ParseErr; the walk continues past them.
//
// ctx is checked at every directory entry — a canceled context aborts
// the walk with ctx.Err() (wraps to context.Canceled / DeadlineExceeded
// via errors.Is).
func Walk(ctx context.Context, rootDir string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(rootDir, func(p string, dirent os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if dirent.IsDir() {
			if shouldSkipDir(dirent.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		kind := FileKind(p)
		info, statErr := os.Stat(p)
		if statErr != nil {
			// Best-effort: a Stat failure (transient FS issue, perms)
			// shouldn't abort an otherwise-clean walk.
			return nil
		}
		rel, _ := filepath.Rel(rootDir, p)
		entry := File{
			Path: filepath.ToSlash(rel),
			Kind: kind,
			Size: info.Size(),
		}
		if kind == "bzl" || kind == "build" || kind == "module" {
			f, perr := (&syntax.FileOptions{}).Parse(p, nil, syntax.RetainComments)
			if perr != nil {
				entry.ParseErr = perr
			} else {
				entry.AST = f
			}
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FileKind classifies path by basename. Single source of truth — both
// bzlwalk and hermetic used to compute this independently with slightly
// different rules; this consolidates them.
func FileKind(p string) string {
	base := filepath.Base(p)
	switch {
	case base == "MODULE.bazel":
		return "module"
	case base == "BUILD" || base == "BUILD.bazel":
		return "build"
	case strings.HasSuffix(base, ".bzl"):
		return "bzl"
	default:
		return "other"
	}
}

// shouldSkipDir returns true for directory basenames the walker
// always descends past. Hidden dirs (leading dot), Bazel build
// outputs, and node_modules — same list bzlwalk + hermetic used
// pre-merge.
func shouldSkipDir(name string) bool {
	if name == "." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "bazel-bin", "bazel-out", "bazel-testlogs", "node_modules":
		return true
	}
	return false
}
