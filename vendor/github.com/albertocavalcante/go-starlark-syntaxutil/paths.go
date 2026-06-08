package syntaxutil

import (
	"path/filepath"
	"strings"
)

// TestOrExamplePathSegments lists directory basenames that callers
// typically want to skip when scanning a tree (test fixtures,
// examples, vendored third-party code).
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
