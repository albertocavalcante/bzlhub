package purge

import (
	"strings"
)

// URLsForModule returns the URLs whose cached representations should
// be invalidated when a new version of `module` is published.
//
// The list covers ONLY mutable URLs — versioned blobs and per-version
// metadata (`source.json`, `MODULE.bazel`) are content-addressed or
// version-pinned and therefore immutable; CDN caches can hold them
// indefinitely.
//
// Mutable URLs after a new-version publish:
//   - /modules/<module>/metadata.json  (lists all versions of the
//     module; gains a new entry)
//   - /bazel_registry.json              (lists all modules; gains an
//     entry only when the module itself is new — but cheaper to
//     always purge than to plumb a flag through)
//
// baseURL is the canopy origin reachable through the CDN (e.g.,
// `https://bcr.bzlhub.com`). Trailing slashes are stripped. An empty
// baseURL returns nil — purgers downstream NoOp themselves on empty
// input.
func URLsForModule(baseURL, module string) []string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	module = strings.TrimSpace(module)
	if base == "" || module == "" {
		return nil
	}
	return []string{
		base + "/modules/" + module + "/metadata.json",
		base + "/bazel_registry.json",
	}
}
