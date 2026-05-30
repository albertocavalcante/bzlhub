package syntaxutil

import (
	"path/filepath"
	"strings"
)

// TestOrExamplePathSegments is the curated directory-basename list
// shared by detectors that need to ignore test fixtures, examples,
// or vendored third-party code.
//
// Used by:
//   - hermetic.BuildFromSource filter (to drop test-fixture compilation calls)
//   - bzlwalk macro emission (to drop test/example/vendor macros from
//     the registry-page-grade list)
//
// Both consumers share this exact list. NOTE: hermetic's BFS check
// also matches `tools/`/`tooling/` via the separate
// isReleaseToolingPath helper — that's a stricter set used only for
// the self-publish demotion. Macros do NOT exclude tools/, since
// rules_go's go/tools/bazel_testing/def.bzl ships consumer-facing
// macros from a path containing tools/.
var TestOrExamplePathSegments = map[string]bool{
	"test":              true,
	"tests":             true,
	"testdata":          true,
	"test-data":         true,
	"example":           true,
	"examples":          true,
	"e2e":               true,
	"integration_tests": true,
	"vendor":            true,
	"third_party":       true,
	"thirdparty":        true,
}

// IsTestOrExamplePath reports whether any segment of p matches a
// known test/example/vendor directory name. Path is split on forward
// slashes; backslashes are normalized first.
func IsTestOrExamplePath(p string) bool {
	for seg := range strings.SplitSeq(filepath.ToSlash(p), "/") {
		if TestOrExamplePathSegments[seg] {
			return true
		}
	}
	return false
}
