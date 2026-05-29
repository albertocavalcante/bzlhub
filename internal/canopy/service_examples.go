package canopy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/codenav"
)

// Caps for ExampleFiles. Tuned to keep the response well under a
// megabyte even when the example dir is rich, while still letting
// a typical Bazel example (BUILD.bazel + one or two .bzl files +
// a README) come through intact.
const (
	exampleMaxFiles        = 25
	exampleMaxBytesPerFile = 32 * 1024
)

// exampleAllowedExt is the small set of extensions we INLINE.
// Anything else (binaries, generated files, large data) we list by
// path but don't read into the payload. Driven by what readers
// realistically want to copy-paste from a registry browser:
// build files, Starlark, scripts, prose.
var exampleAllowedExt = map[string]bool{
	".bzl":  true,
	".py":   true,
	".sh":   true,
	".md":   true,
	".yaml": true,
	".yml":  true,
	".json": true,
	".toml": true,
	".txt":  true,
	".go":   true,
	".ts":   true,
	".js":   true,
}

// exampleAllowedDotfiles is the tiny set of leading-dot filenames
// that ARE worth surfacing. Everything else dotted is editor config
// (.editorconfig, .flake8) or VCS noise — relevant to running the
// example but uninteresting from a "what does this rule LOOK like"
// reader's perspective. Better to skip than burn one of the 25
// slots showing a 13-byte .bazelignore.
var exampleAllowedDotfiles = map[string]bool{
	".bazelrc":      true,
	".bazelversion": true,
}

// exampleSkipDirs are directory basenames we never recurse into.
// Real example trees often check in node_modules / .git / bazel-out
// when a contributor accidentally committed them; surfacing those
// would either explode the response or — worse — leak partial dumps
// of irrelevant content into the registry browser.
var exampleSkipDirs = map[string]bool{
	".git":           true,
	"node_modules":   true,
	"bazel-bin":      true,
	"bazel-out":      true,
	"bazel-testlogs": true,
	".cache":         true,
	".idea":          true,
	".vscode":        true,
}

// ExampleFiles walks the named example directory under the
// module's unpacked source tree and returns up to exampleMaxFiles
// files with their contents inlined. Anything past the cap (file
// count OR per-file size) is omitted; the UI falls back to a
// "browse →" link to code-nav.
//
// Path-safety: dir must not contain `..` segments — the directory
// is joined under the sources cache without further normalization,
// so the strip is necessary.
func (s *Service) ExampleFiles(ctx context.Context, name, version, dir string) (*api.ExampleDirContents, error) {
	if s.SourcesCacheDir == "" || s.MirrorRoot == "" {
		return nil, errors.New("example-files not available: canopy was started without both --root and a sources cache")
	}
	// Reject suspicious dir values up front — the only legitimate
	// inputs come from ModuleReport.Assets.ExampleDirs which is
	// always a single path component at the module root.
	if dir == "" || strings.Contains(dir, "/") || strings.Contains(dir, "..") {
		return nil, fmt.Errorf("invalid example dir %q", dir)
	}
	sourceRoot, err := codenav.MaterializeSource(s.MirrorRoot, s.SourcesCacheDir, name, version)
	if err != nil {
		return nil, fmt.Errorf("materialize source for %s@%s: %w", name, version, err)
	}
	root := filepath.Join(sourceRoot, dir)
	res := &api.ExampleDirContents{Dir: dir, Files: []api.ExampleFile{}}

	// Walk in lexical order for stable output across runs — important
	// for any consumer caching the response or diffing across versions.
	var walked int
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			// Skip irrelevant subtrees wholesale — checking .git in
			// the walker would otherwise read every file inside it.
			// Also skip the dir root itself when its basename is in
			// the skip set; the path check works at any depth.
			if exampleSkipDirs[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if walked >= exampleMaxFiles {
			res.Truncated = true
			return filepath.SkipAll
		}
		base := d.Name()
		// Dotfile filter: surface only the explicit allowlist
		// (.bazelrc / .bazelversion). Editor configs, lint configs,
		// VCS metadata, etc. all start with a dot and waste slots —
		// readers come here for the rule shape, not the ergonomics.
		if strings.HasPrefix(base, ".") && !exampleAllowedDotfiles[base] {
			return nil
		}
		rel, _ := filepath.Rel(sourceRoot, path)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f := api.ExampleFile{Path: rel, Size: info.Size()}
		// Read contents only when the extension is on the allowed
		// list AND the file fits the per-file cap. Otherwise list
		// the path so the UI can show "view in code-nav" instead.
		ext := strings.ToLower(filepath.Ext(rel))
		inline := exampleAllowedExt[ext]
		// BUILD / BUILD.bazel / WORKSPACE.bazel have no extension but
		// are the canonical Bazel files; inline them too.
		if !inline && (base == "BUILD" || base == "BUILD.bazel" ||
			base == "WORKSPACE" || base == "WORKSPACE.bazel" ||
			base == "MODULE.bazel" || exampleAllowedDotfiles[base]) {
			inline = true
		}
		if inline && info.Size() <= exampleMaxBytesPerFile {
			bytes, err := os.ReadFile(path)
			if err == nil {
				f.Bytes = string(bytes)
			}
		} else if info.Size() > exampleMaxBytesPerFile {
			f.Truncated = true
		}
		res.Files = append(res.Files, f)
		walked++
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return nil, fmt.Errorf("walk example dir: %w", walkErr)
	}
	return res, nil
}
