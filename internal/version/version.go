// Package version holds the build metadata surfaced by /api/version
// and `bzlhub --version`. The vars are populated at link time via
//
//	go build -ldflags "-X github.com/albertocavalcante/bzlhub/internal/version.Version=v0.4.0 \
//	                   -X github.com/albertocavalcante/bzlhub/internal/version.Commit=abc1234 \
//	                   -X github.com/albertocavalcante/bzlhub/internal/version.BuiltAt=2026-05-15T12:00:00Z"
//
// The defaults below are deliberate sentinels for binaries built without
// the flag (e.g. `go install`, `go test`).
package version

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

// String returns the canonical one-line rendering used by the CLI's
// --version flag.
func String() string {
	return Version + " (" + Commit + ", built " + BuiltAt + ")"
}
