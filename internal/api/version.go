package api

// IsStubVersion reports whether v is a placeholder version string —
// values that surface when a MODULE.bazel has no real version, or when
// the ingest fell back to a zero value. Excludes rolling-tag
// conventions like "HEAD", which are intentional.
//
// Shared by internal/server (versionEntry shaping, listing filters)
// and internal/canopy (corpus-stat counting). The TS counterpart in
// ui/src/lib/links.ts mirrors this set; if it grows, update both.
func IsStubVersion(v string) bool {
	switch v {
	case "", "0", "0.0.0":
		return true
	}
	return false
}
